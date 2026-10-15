package vdi_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/vdi"
	"github.com/tuna-os/corral/pkg/vdi/vditest"
)

var (
	t0      = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	vmPool  = vdi.PoolRef{Kind: vdi.PoolKindVM, Namespace: "vdi", Name: "desk"}
	ctPool  = vdi.PoolRef{Kind: vdi.PoolKindCT, Namespace: "vdi", Name: "lab"}
	alice   = vdi.Actor{Identity: "alice@example.com"}
	bob     = vdi.Actor{Identity: "bob@example.com"}
	admin   = vdi.Actor{Identity: "root@example.com", Admin: true}
	desk1   = vdi.MemberRef{Kind: vdi.PoolKindVM, Namespace: "vdi", Name: "desk-1"}
	desk2   = vdi.MemberRef{Kind: vdi.PoolKindVM, Namespace: "vdi", Name: "desk-2"}
	elsewhr = vdi.PoolRef{Kind: vdi.PoolKindVM, Namespace: "kube-system", Name: "desk"}
)

func newBroker(t *testing.T) (*vdi.Broker, *vditest.Cluster, *[]vdi.AuditEvent) {
	t.Helper()
	now := func() time.Time { return t0 }
	c := vditest.New(now)
	c.AddPool(vmPool, vdi.PoolModePersistent, 2)
	c.AddPool(ctPool, vdi.PoolModeEphemeral, 1)
	c.AddPool(elsewhr, vdi.PoolModePersistent, 1)
	var mu sync.Mutex
	events := &[]vdi.AuditEvent{}
	b := vdi.NewBroker(c, vdi.BrokerConfig{Namespaces: []string{"vdi", "vdi", " "}, Now: now, Audit: func(e vdi.AuditEvent) {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, e)
	}})
	return b, c, events
}

func mustClaim(t *testing.T, b *vdi.Broker, a vdi.Actor, p vdi.PoolRef) vdi.Desktop {
	t.Helper()
	d, err := b.Claim(a, p)
	if err != nil {
		t.Fatalf("Claim(%s, %s): %v", a.Identity, p.ID(), err)
	}
	return d
}

func release(t *testing.T, b *vdi.Broker, a vdi.Actor, ref vdi.MemberRef) error {
	t.Helper()
	run, err := b.BeginRelease(a, ref)
	if err != nil {
		return err
	}
	return run()
}

func TestBroker_ClaimReportsStableIdentifiersAndStarts(t *testing.T) {
	b, c, events := newBroker(t)
	d := mustClaim(t, b, alice, vmPool)
	if d.ID != "vm/vdi/desk-1" || d.PoolID != "vm/vdi/desk" || d.Owner != alice.Identity || d.Member != "desk-1" || d.Pool != "desk" {
		t.Fatalf("desktop = %+v", d)
	}
	if d.SessionID == "" || !d.Running || d.Phase != "Running" || d.ClaimExpired || d.StartError != "" {
		t.Fatalf("desktop state = %+v", d)
	}
	if !c.Running(desk1) || c.Holder(desk1) != alice.Identity {
		t.Fatal("claim did not start the member under alice's claim")
	}
	if len(*events) != 1 || (*events)[0].Action != "claim" || (*events)[0].Target != d.ID || (*events)[0].Actor != alice.Identity {
		t.Fatalf("audit = %+v", *events)
	}
}

func TestBroker_ClaimRetryReturnsTheSameDesktop(t *testing.T) {
	b, _, _ := newBroker(t)
	first := mustClaim(t, b, alice, vmPool)
	again := mustClaim(t, b, alice, vmPool)
	if again.ID != first.ID || again.SessionID != first.SessionID {
		t.Fatalf("retry claimed %s (%s), want %s (%s)", again.ID, again.SessionID, first.ID, first.SessionID)
	}
	if other := mustClaim(t, b, bob, vmPool); other.ID == first.ID {
		t.Fatal("bob got alice's desktop")
	}
}

func TestBroker_PoolExhaustion(t *testing.T) {
	b, _, events := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	mustClaim(t, b, bob, vmPool)
	_, err := b.Claim(vdi.Actor{Identity: "carol"}, vmPool)
	if !errors.Is(err, vdi.ErrPoolExhausted) {
		t.Fatalf("third claim err = %v, want ErrPoolExhausted", err)
	}
	last := (*events)[len(*events)-1]
	if last.Action != "claim" || last.Err == nil || last.Target != vmPool.ID() {
		t.Fatalf("exhaustion was not audited: %+v", last)
	}
}

func TestBroker_ConcurrentClaimsGetDistinctDesktops(t *testing.T) {
	b, _, _ := newBroker(t)
	var wg sync.WaitGroup
	results := make(chan string, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := b.Claim(vdi.Actor{Identity: fmt.Sprintf("user%d", i)}, vmPool)
			if err == nil {
				results <- d.ID
			}
		}()
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for id := range results {
		if seen[id] {
			t.Fatalf("%s was handed out twice", id)
		}
		seen[id] = true
	}
	if len(seen) != 2 {
		t.Fatalf("%d desktops handed out, want 2", len(seen))
	}
}

func TestBroker_StartFailureKeepsTheClaimAndCanBeRetried(t *testing.T) {
	b, c, _ := newBroker(t)
	c.FailStarts(desk1, 1)
	d := mustClaim(t, b, alice, vmPool)
	if d.StartError == "" || d.Running || c.Holder(desk1) != alice.Identity {
		t.Fatalf("failed start: desktop %+v, holder %q", d, c.Holder(desk1))
	}
	d, err := b.Start(alice, desk1)
	if err != nil || !d.Running || d.StartError != "" {
		t.Fatalf("Start = %+v, %v", d, err)
	}
	// A start of a running desktop is a no-op.
	if _, err := b.Start(alice, desk1); err != nil {
		t.Fatal(err)
	}
	if starts, _, _ := c.Counts(desk1); starts != 1 {
		t.Fatalf("starts = %d, want 1", starts)
	}
}

func TestBroker_StartFailureOnRetryIsReported(t *testing.T) {
	b, c, _ := newBroker(t)
	c.FailStarts(desk1, 2)
	mustClaim(t, b, alice, vmPool)
	d, err := b.Start(alice, desk1)
	if err != nil || d.StartError == "" {
		t.Fatalf("Start = %+v, %v; want a reported start error", d, err)
	}
}

func TestBroker_OwnerReleasesAndPersistentMemberStops(t *testing.T) {
	b, c, events := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	if err := release(t, b, alice, desk1); err != nil {
		t.Fatal(err)
	}
	_, stops, recreates := c.Counts(desk1)
	if c.Holder(desk1) != "" || c.Running(desk1) || stops != 1 || recreates != 0 {
		t.Fatalf("after release: holder %q running %v stops %d recreates %d", c.Holder(desk1), c.Running(desk1), stops, recreates)
	}
	last := (*events)[len(*events)-1]
	if last.Action != "release" || last.Err != nil || last.Holder != "" {
		t.Fatalf("audit = %+v", last)
	}
	if _, err := b.Desktop(alice, desk1); !errors.Is(err, vdi.ErrNotFound) {
		t.Fatalf("released desktop still inspectable: %v", err)
	}
}

func TestBroker_EphemeralCTMemberIsRecreated(t *testing.T) {
	b, c, _ := newBroker(t)
	d := mustClaim(t, b, alice, ctPool)
	ref := vdi.MemberRef{Kind: vdi.PoolKindCT, Namespace: "vdi", Name: d.Member}
	if err := release(t, b, alice, ref); err != nil {
		t.Fatal(err)
	}
	if _, _, recreates := c.Counts(ref); recreates != 1 {
		t.Fatalf("recreates = %d, want 1", recreates)
	}
}

func TestBroker_OthersCannotTouchADesktop(t *testing.T) {
	b, c, _ := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	if _, err := b.Desktop(bob, desk1); !errors.Is(err, vdi.ErrForbidden) {
		t.Errorf("bob inspected alice's desktop: %v", err)
	}
	if _, err := b.Start(bob, desk1); !errors.Is(err, vdi.ErrForbidden) {
		t.Errorf("bob started alice's desktop: %v", err)
	}
	if _, err := b.BeginRelease(bob, desk1); !errors.Is(err, vdi.ErrForbidden) {
		t.Errorf("bob released alice's desktop: %v", err)
	}
	if c.Holder(desk1) != alice.Identity || !c.Running(desk1) {
		t.Fatal("alice lost her desktop to bob")
	}
	if _, err := b.Desktops(bob, true); !errors.Is(err, vdi.ErrForbidden) {
		t.Errorf("bob listed every desktop: %v", err)
	}
	mine, err := b.Desktops(bob, false)
	if err != nil || len(mine) != 0 {
		t.Errorf("bob's desktops = %+v, %v; want none", mine, err)
	}
}

func TestBroker_AdminForceReleaseIsAudited(t *testing.T) {
	b, c, events := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	all, err := b.Desktops(admin, true)
	if err != nil || len(all) != 1 || all[0].Owner != alice.Identity {
		t.Fatalf("admin list = %+v, %v", all, err)
	}
	if _, err := b.Desktop(admin, desk1); err != nil {
		t.Fatal(err)
	}
	if err := release(t, b, admin, desk1); err != nil {
		t.Fatal(err)
	}
	if c.Holder(desk1) != "" {
		t.Fatal("force release kept the claim")
	}
	last := (*events)[len(*events)-1]
	if last.Action != "force-release" || last.Actor != admin.Identity || last.Holder != alice.Identity {
		t.Fatalf("audit = %+v", last)
	}
}

func TestBroker_ReleaseFailureKeepsOwnershipForRetry(t *testing.T) {
	b, c, events := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	c.FailReleases(desk1, 1)
	if err := release(t, b, alice, desk1); err == nil {
		t.Fatal("expected the injected release failure")
	}
	if (*events)[len(*events)-1].Err == nil {
		t.Fatal("the failed release was not audited as a failure")
	}
	if d, err := b.Desktop(alice, desk1); err != nil || d.Owner != alice.Identity {
		t.Fatalf("after a failed release alice lost her desktop: %+v, %v", d, err)
	}
	if err := release(t, b, alice, desk1); err != nil {
		t.Fatalf("retried release: %v", err)
	}
}

func TestBroker_StaleReleaseLosesToANewClaim(t *testing.T) {
	b, _, _ := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	run, err := b.BeginRelease(alice, desk1)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(t, b, admin, desk1); err != nil {
		t.Fatal(err)
	}
	mustClaim(t, b, bob, vmPool)
	if err := run(); err == nil {
		t.Fatal("alice's stale release ended bob's new claim")
	}
	if d, err := b.Desktop(bob, desk1); err != nil || d.Owner != bob.Identity {
		t.Fatalf("bob's claim = %+v, %v", d, err)
	}
}

func TestBroker_RejectsAnonymousAndReservedIdentities(t *testing.T) {
	b, _, _ := newBroker(t)
	if _, err := b.Claim(vdi.Actor{Identity: "  "}, vmPool); !errors.Is(err, vdi.ErrUnauthenticated) {
		t.Errorf("anonymous claim: %v", err)
	}
	if _, err := b.Claim(vdi.Actor{Identity: vdi.CTReconcilerHolder}, vmPool); !errors.Is(err, vdi.ErrForbidden) {
		t.Errorf("reserved claim: %v", err)
	}
	if _, err := b.Desktops(vdi.Actor{}, false); !errors.Is(err, vdi.ErrUnauthenticated) {
		t.Errorf("anonymous list: %v", err)
	}
	if _, err := b.BeginRelease(vdi.Actor{}, desk1); !errors.Is(err, vdi.ErrUnauthenticated) {
		t.Errorf("anonymous release: %v", err)
	}
}

func TestBroker_OnlyServesConfiguredNamespacesAndKinds(t *testing.T) {
	b, _, _ := newBroker(t)
	if got := b.Namespaces(); len(got) != 1 || got[0] != "vdi" {
		t.Fatalf("Namespaces = %v", got)
	}
	if _, err := b.Claim(alice, elsewhr); !errors.Is(err, vdi.ErrPoolNotFound) {
		t.Errorf("claim outside served namespaces: %v", err)
	}
	if _, err := b.Claim(alice, vdi.PoolRef{Kind: "pod", Namespace: "vdi", Name: "desk"}); !errors.Is(err, vdi.ErrPoolNotFound) {
		t.Errorf("claim of an unknown kind: %v", err)
	}
	if _, err := b.Claim(alice, vdi.PoolRef{Kind: vdi.PoolKindVM, Namespace: "vdi", Name: "ghost"}); !errors.Is(err, vdi.ErrPoolNotFound) {
		t.Errorf("claim of a missing pool: %v", err)
	}
	if _, err := b.Desktop(admin, vdi.MemberRef{Kind: vdi.PoolKindVM, Namespace: "kube-system", Name: "desk-1"}); !errors.Is(err, vdi.ErrNotFound) {
		t.Errorf("inspect outside served namespaces: %v", err)
	}
	if _, err := b.Desktop(alice, desk2); !errors.Is(err, vdi.ErrNotFound) {
		t.Errorf("inspect of an unclaimed member: %v", err)
	}
	pools, err := b.Pools()
	if err != nil || len(pools) != 2 || pools[0].ID != "ct/vdi/lab" || pools[1].ID != "vm/vdi/desk" || pools[1].Free != 2 {
		t.Fatalf("Pools = %+v, %v", pools, err)
	}
}

func TestClaim_SessionIDChangesWithEachClaim(t *testing.T) {
	c := vdi.MemberClaim{Member: desk1, Holder: "alice", AcquiredAt: t0}
	same := c
	later := c
	later.AcquiredAt = t0.Add(time.Second)
	other := c
	other.Holder = "bob"
	if c.SessionID() != same.SessionID() || c.SessionID() == later.SessionID() || c.SessionID() == other.SessionID() {
		t.Fatal("SessionID must be stable per claim and differ between claims")
	}
	if !c.Expired(t0) {
		t.Fatal("a claim without a duration is expired")
	}
	c.Duration, c.RenewedAt = time.Hour, t0
	if c.Expired(t0.Add(time.Hour)) || !c.Expired(t0.Add(time.Hour+time.Second)) {
		t.Fatal("expiry boundary")
	}
}
