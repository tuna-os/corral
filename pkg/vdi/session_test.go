package vdi_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/vdi"
	"github.com/tuna-os/corral/pkg/vdi/vditest"
)

// Fake-clock tests of session tracking and reclaim (#228).

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type sessionEnv struct {
	clk     *clock
	cluster *vditest.Cluster
	broker  *vdi.Broker
	events  []vdi.AuditEvent
	mu      sync.Mutex
}

const grace = 15 * time.Minute

func newSessionEnv(t *testing.T, maxSession time.Duration) *sessionEnv {
	t.Helper()
	env := &sessionEnv{clk: &clock{now: t0}}
	env.cluster = vditest.New(env.clk.Now)
	env.cluster.AddPool(vmPool, vdi.PoolModePersistent, 2)
	env.cluster.AddPool(ctPool, vdi.PoolModeEphemeral, 1)
	env.broker = env.newBroker(maxSession)
	return env
}

// newBroker starts a broker process over the env's cluster, as a restart
// would.
func (env *sessionEnv) newBroker(maxSession time.Duration) *vdi.Broker {
	return vdi.NewBroker(env.cluster, vdi.BrokerConfig{
		Namespaces: []string{"vdi"}, Now: env.clk.Now, DisconnectGrace: grace, MaxSession: maxSession,
		Audit: func(e vdi.AuditEvent) {
			env.mu.Lock()
			defer env.mu.Unlock()
			env.events = append(env.events, e)
		},
	})
}

func (env *sessionEnv) reclaims() []vdi.AuditEvent {
	env.mu.Lock()
	defer env.mu.Unlock()
	var out []vdi.AuditEvent
	for _, e := range env.events {
		if e.Action == "reclaim" {
			out = append(out, e)
		}
	}
	return out
}

func (env *sessionEnv) reconcile(t *testing.T) {
	t.Helper()
	if err := env.broker.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestSession_DisconnectGraceReclaims(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	closeConsole := env.broker.ConsoleOpened("vdi", "desk-1")
	env.clk.Advance(time.Hour) // an hour connected: age alone never reclaims
	env.reconcile(t)
	if env.cluster.Holder(desk1) == "" {
		t.Fatal("a connected session was reclaimed")
	}
	closeConsole()
	env.clk.Advance(grace - time.Second)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != alice.Identity {
		t.Fatal("reclaimed before the grace period ended")
	}
	d, err := env.broker.Desktop(alice, desk1)
	if err != nil || d.Connected || d.DisconnectedAt == nil || d.ReclaimReason != vdi.ReclaimDisconnected || !d.ReclaimAt.Equal(t0.Add(time.Hour+grace)) {
		t.Fatalf("desktop = %+v, %v", d, err)
	}
	env.clk.Advance(time.Second)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != "" || env.cluster.Running(desk1) {
		t.Fatal("not reclaimed when the grace period ended")
	}
	r := env.reclaims()
	if len(r) != 1 || r[0].Reason != vdi.ReclaimDisconnected || r[0].Holder != alice.Identity || r[0].Actor != vdi.ReclaimActor || r[0].Err != nil {
		t.Fatalf("reclaim audit = %+v", r)
	}
}

func TestSession_ReconnectCancelsPendingReclaim(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	env.broker.ConsoleOpened("vdi", "desk-1")()
	env.clk.Advance(10 * time.Minute)
	env.reconcile(t) // persists the disconnect
	if c, _ := env.cluster.Claim(desk1); c.Session.DisconnectedAt.IsZero() {
		t.Fatal("the disconnect was not recorded on the claim")
	}
	reopen := env.broker.ConsoleOpened("vdi", "desk-1")
	env.clk.Advance(time.Hour)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != alice.Identity {
		t.Fatal("a reconnected session was reclaimed")
	}
	if c, _ := env.cluster.Claim(desk1); !c.Session.DisconnectedAt.IsZero() || c.Session.ConnectedAt.IsZero() {
		t.Fatalf("the reconnect was not recorded: %+v", c.Session)
	}
	if d, _ := env.broker.Desktop(alice, desk1); !d.Connected || d.ReclaimAt != nil {
		t.Fatalf("connected desktop = %+v", d)
	}
	reopen()
	env.clk.Advance(grace)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != "" {
		t.Fatal("the second disconnect did not start a new grace period")
	}
}

func TestSession_DuplicateAndOverlappingConsoleEvents(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	first := env.broker.ConsoleOpened("vdi", "desk-1")
	second := env.broker.ConsoleOpened("vdi", "desk-1") // a second tab
	first()
	first() // a duplicate close counts once
	env.clk.Advance(2 * grace)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != alice.Identity {
		t.Fatal("reclaimed while the second console was still open")
	}
	second()
	second()
	env.clk.Advance(grace)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != "" {
		t.Fatal("not reclaimed after the last console closed")
	}
	// Duplicate reconciles after the reclaim change nothing.
	env.reconcile(t)
	env.reconcile(t)
	if n := len(env.reclaims()); n != 1 {
		t.Fatalf("%d reclaims, want 1", n)
	}
}

func TestSession_NeverConnectedClaimIsKeptAndRenewed(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	for range 8 {
		env.clk.Advance(20 * time.Minute)
		env.reconcile(t)
	}
	c, ok := env.cluster.Claim(desk1)
	if !ok || c.Holder != alice.Identity {
		t.Fatal("a claim without a console session was reclaimed")
	}
	if c.Expired(env.clk.Now()) || !c.AcquiredAt.Equal(t0) {
		t.Fatalf("claim not renewed or its acquisition time changed: %+v", c)
	}
	if env.cluster.Updates() == 0 {
		t.Fatal("no renewals")
	}
}

func TestSession_MaxDurationAppliesWhileConnected(t *testing.T) {
	env := newSessionEnv(t, 8*time.Hour)
	mustClaim(t, env.broker, alice, vmPool)
	env.broker.ConsoleOpened("vdi", "desk-1")
	env.clk.Advance(8*time.Hour - time.Minute)
	env.reconcile(t)
	if d, _ := env.broker.Desktop(alice, desk1); d.ReclaimReason != vdi.ReclaimMaxDuration || !d.ReclaimAt.Equal(t0.Add(8*time.Hour)) {
		t.Fatalf("desktop = %+v", d)
	}
	env.clk.Advance(time.Minute)
	env.reconcile(t)
	r := env.reclaims()
	if env.cluster.Holder(desk1) != "" || len(r) != 1 || r[0].Reason != vdi.ReclaimMaxDuration {
		t.Fatalf("max duration: holder %q, reclaims %+v", env.cluster.Holder(desk1), r)
	}
}

func TestSession_EarlierDeadlineWins(t *testing.T) {
	env := newSessionEnv(t, 30*time.Minute)
	mustClaim(t, env.broker, alice, vmPool)
	env.broker.ConsoleOpened("vdi", "desk-1")()
	env.clk.Advance(time.Minute)
	if d, _ := env.broker.Desktop(alice, desk1); d.ReclaimReason != vdi.ReclaimDisconnected || !d.ReclaimAt.Equal(t0.Add(grace)) {
		t.Fatalf("desktop = %+v; want the disconnect deadline, which is earlier", d)
	}
}

func TestSession_LifecycleFailureKeepsTheClaimAndRetries(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	env.broker.ConsoleOpened("vdi", "desk-1")()
	env.cluster.FailReleases(desk1, 1)
	env.clk.Advance(grace)
	if err := env.broker.Reconcile(); err == nil {
		t.Fatal("expected the failed reclaim to be reported")
	}
	if env.cluster.Holder(desk1) != alice.Identity {
		t.Fatal("a failed reclaim lost the claim")
	}
	if r := env.reclaims(); len(r) != 1 || r[0].Err == nil {
		t.Fatalf("failed reclaim audit = %+v", r)
	}
	env.clk.Advance(time.Minute)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != "" {
		t.Fatal("the reclaim was not retried")
	}
}

func TestSession_RenewFailureIsReported(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	env.broker.ConsoleOpened("vdi", "desk-1")
	env.cluster.FailUpdates(1)
	if err := env.broker.Reconcile(); err == nil {
		t.Fatal("expected the failed renewal to be reported")
	}
	env.reconcile(t)
	if c, _ := env.cluster.Claim(desk1); c.Session.ConnectedAt.IsZero() {
		t.Fatal("the presence was not recorded on the retry")
	}
}

func TestSession_RestartDoesNotReclaimALiveClaim(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	env.broker.ConsoleOpened("vdi", "desk-1") // open when the broker dies
	env.reconcile(t)

	// Restart an hour later: the console died with the old process.
	env.clk.Advance(time.Hour)
	restartedAt := env.clk.Now()
	env.broker = env.newBroker(0)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != alice.Identity {
		t.Fatal("the restart reclaimed a live claim")
	}
	c, _ := env.cluster.Claim(desk1)
	if !c.Session.DisconnectedAt.Equal(restartedAt) {
		t.Fatalf("grace must start at the restart, got disconnect %v", c.Session.DisconnectedAt)
	}
	// A second restart keeps the recorded disconnect time.
	env.clk.Advance(grace / 2)
	env.broker = env.newBroker(0)
	env.reconcile(t)
	if c, _ := env.cluster.Claim(desk1); !c.Session.DisconnectedAt.Equal(restartedAt) {
		t.Fatalf("a restart moved the disconnect time to %v", c.Session.DisconnectedAt)
	}
	// The user reconnects to the new process in time: no reclaim.
	env.broker.ConsoleOpened("vdi", "desk-1")
	env.clk.Advance(grace)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != alice.Identity {
		t.Fatal("a reconnect after the restart did not cancel the reclaim")
	}
}

func TestSession_RestartReclaimsAnExpiredGrace(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	env.broker.ConsoleOpened("vdi", "desk-1")()
	env.reconcile(t)
	env.clk.Advance(time.Hour)
	env.broker = env.newBroker(0)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != "" {
		t.Fatal("a claim disconnected for longer than the grace period survived a restart")
	}
}

func TestSession_PresenceOfAnEarlierClaimIsIgnored(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	env.broker.ConsoleOpened("vdi", "desk-1")()
	if err := release(t, env.broker, alice, desk1); err != nil {
		t.Fatal(err)
	}
	env.clk.Advance(time.Minute)
	mustClaim(t, env.broker, bob, vmPool)
	env.clk.Advance(2 * grace)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != bob.Identity {
		t.Fatal("alice's disconnect reclaimed bob's new claim")
	}
}

func TestSession_EphemeralCTMemberIsRecreatedOnReclaim(t *testing.T) {
	env := newSessionEnv(t, 0)
	d := mustClaim(t, env.broker, alice, ctPool)
	ref := vdi.MemberRef{Kind: vdi.PoolKindCT, Namespace: "vdi", Name: d.Member}
	env.broker.ConsoleOpened("vdi", d.Member)()
	env.clk.Advance(grace)
	env.reconcile(t)
	if _, _, recreates := env.cluster.Counts(ref); recreates != 1 || env.cluster.Holder(ref) != "" {
		t.Fatalf("recreates = %d, holder %q", recreates, env.cluster.Holder(ref))
	}
}

func TestSession_UnservedNamespaceConsolesAreIgnored(t *testing.T) {
	env := newSessionEnv(t, 0)
	env.broker.ConsoleOpened("kube-system", "desk-1")()
	env.reconcile(t)
}

func TestSession_ConcurrentConsolesAndReconcile(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 20 {
				env.broker.ConsoleOpened("vdi", "desk-1")()
			}
		}()
		go func() {
			defer wg.Done()
			_ = env.broker.Reconcile()
		}()
	}
	keep := env.broker.ConsoleOpened("vdi", "desk-1")
	wg.Wait()
	env.clk.Advance(2 * grace)
	env.reconcile(t)
	if env.cluster.Holder(desk1) != alice.Identity {
		t.Fatal("reclaimed while a console was open")
	}
	keep()
}

func TestSession_DisabledGraceNeverReclaimsOnDisconnect(t *testing.T) {
	clk := &clock{now: t0}
	c := vditest.New(clk.Now)
	c.AddPool(vmPool, vdi.PoolModePersistent, 1)
	b := vdi.NewBroker(c, vdi.BrokerConfig{Namespaces: []string{"vdi"}, Now: clk.Now})
	mustClaim(t, b, alice, vmPool)
	b.ConsoleOpened("vdi", "desk-1")()
	clk.Advance(48 * time.Hour)
	if err := b.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if c.Holder(desk1) != alice.Identity {
		t.Fatal("reclaimed with the disconnect grace disabled")
	}
}

func TestSession_RunReconcilesUntilCancelled(t *testing.T) {
	env := newSessionEnv(t, 0)
	mustClaim(t, env.broker, alice, vmPool)
	env.broker.ConsoleOpened("vdi", "desk-1")
	env.cluster.FailUpdates(1)
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		env.broker.Run(ctx, time.Millisecond, func(err error) {
			select {
			case errs <- err:
			default:
			}
		})
		close(done)
	}()
	if err := <-errs; err == nil {
		t.Fatal("Run passed a nil error")
	}
	cancel()
	<-done
	if env.cluster.Holder(desk1) != alice.Identity {
		t.Fatal("Run reclaimed a connected claim")
	}
}
