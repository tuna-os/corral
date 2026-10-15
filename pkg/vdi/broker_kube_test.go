package vdi

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Tests of KubeCluster, the kubectl-backed broker Cluster. VM pool calls
// go through the scripted shell fake; CT pool calls through fakeCT.

const brokerLeases = `{"items":[
	{"metadata":{"name":"corral-vdi-desk-1","resourceVersion":"11","labels":{"corral.dev/vdi-pool":"desk","corral.dev/vdi-member":"desk-1"}},
	 "spec":{"holderIdentity":"alice@example.com","acquireTime":"2026-10-15T09:00:00Z","renewTime":"2099-10-15T09:00:00Z","leaseDurationSeconds":3600}},
	{"metadata":{"name":"corral-vdi-ct-lab-1","resourceVersion":"12","labels":{"corral.dev/vdi-ct-pool":"lab","corral.dev/vdi-member":"lab-1"}},
	 "spec":{"holderIdentity":"bob@example.com","acquireTime":"2026-10-15T09:30:00Z","renewTime":"2026-10-15T09:30:00Z","leaseDurationSeconds":3600}},
	{"metadata":{"name":"corral-vdi-ct-lab-2","resourceVersion":"13","labels":{"corral.dev/vdi-ct-pool":"lab","corral.dev/vdi-member":"lab-2"}},
	 "spec":{"holderIdentity":"corral-vdi/reconciler","acquireTime":"2026-10-15T09:30:00Z","renewTime":"2026-10-15T09:30:00Z","leaseDurationSeconds":600}}
]}`

func TestKubeCluster_ClaimsParsesVMAndCTLeasesAndSkipsTheReconciler(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "lease", "-n", "vdi", "-l", labelVDIMember, "-o", "json"}, brokerLeases, nil)

	claims, err := KubeCluster().Claims("vdi")
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 {
		t.Fatalf("claims = %+v, want the VM and the user CT claim", claims)
	}
	vm, ct := claims[0], claims[1]
	if vm.Member != (MemberRef{Kind: PoolKindVM, Namespace: "vdi", Name: "desk-1"}) || vm.Pool != "desk" || vm.Holder != "alice@example.com" || vm.Version != "11" || vm.Duration != time.Hour {
		t.Errorf("VM claim = %+v", vm)
	}
	if !vm.AcquiredAt.Equal(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("VM acquired at %v", vm.AcquiredAt)
	}
	if ct.Member.Kind != PoolKindCT || ct.Pool != "lab" || ct.Member.Name != "lab-1" || ct.Holder != "bob@example.com" {
		t.Errorf("CT claim = %+v", ct)
	}
}

func TestKubeCluster_GetClaimChecksTheKind(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "lease", "corral-vdi-desk-1", "-n", "vdi", "--ignore-not-found", "-o", "json"},
		`{"metadata":{"name":"corral-vdi-desk-1","resourceVersion":"11","labels":{"corral.dev/vdi-pool":"desk","corral.dev/vdi-member":"desk-1"}},"spec":{"holderIdentity":"alice"}}`, nil)
	fake.AddResponseKV("kubectl", []string{"get", "lease", "corral-vdi-ct-desk-1", "-n", "vdi", "--ignore-not-found", "-o", "json"}, "", nil)
	// A VM member named like a CT Lease: corral-vdi-ct-x is VM member "ct-x"'s Lease.
	fake.AddResponseKV("kubectl", []string{"get", "lease", "corral-vdi-ct-x", "-n", "vdi", "--ignore-not-found", "-o", "json"},
		`{"metadata":{"name":"corral-vdi-ct-x","labels":{"corral.dev/vdi-pool":"p","corral.dev/vdi-member":"ct-x"}},"spec":{"holderIdentity":"mallory"}}`, nil)

	c, found, err := KubeCluster().GetClaim(MemberRef{Kind: PoolKindVM, Namespace: "vdi", Name: "desk-1"})
	if err != nil || !found || c.Holder != "alice" || c.Pool != "desk" {
		t.Fatalf("VM GetClaim = %+v, %v, %v", c, found, err)
	}
	if _, found, err := KubeCluster().GetClaim(MemberRef{Kind: PoolKindCT, Namespace: "vdi", Name: "desk-1"}); err != nil || found {
		t.Fatalf("absent CT claim: found=%v err=%v", found, err)
	}
	if _, found, _ := KubeCluster().GetClaim(MemberRef{Kind: PoolKindCT, Namespace: "vdi", Name: "x"}); found {
		t.Fatal("a VM member's Lease was read as CT member x's claim")
	}
}

func TestKubeCluster_PoolsCountsVMAndCTPools(t *testing.T) {
	fake := withFake(t)
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(2))
	f.putLease("desk-1", "carol", time.Hour)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "-n", "vdi", "-o", "json", "-l", labelPool}, `{"items":[
		{"metadata":{"name":"vmdesk-1","labels":{"corral.dev/vdi-pool":"vmdesk"}}},
		{"metadata":{"name":"vmdesk-2","labels":{"corral.dev/vdi-pool":"vmdesk"}}},
		{"metadata":{"name":"vmdesk-3","labels":{"corral.dev/vdi-pool":"vmdesk","corral.dev/vdi-assigned-to":"legacy"}}}
	]}`, nil)
	fake.AddResponseKV("kubectl", []string{"get", "lease", "-n", "vdi", "-l", labelVDIMember, "-o", "json"}, `{"items":[
		{"metadata":{"labels":{"corral.dev/vdi-pool":"vmdesk","corral.dev/vdi-member":"vmdesk-1"}},
		 "spec":{"holderIdentity":"alice","renewTime":"2099-01-01T00:00:00Z","leaseDurationSeconds":3600}}
	]}`, nil)

	pools, err := KubeCluster().Pools("vdi")
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 2 {
		t.Fatalf("pools = %+v", pools)
	}
	ct, vm := pools[0], pools[1]
	if ct.ID != "ct/vdi/desk" || ct.Mode != PoolModeEphemeral || ct.Members != 2 || ct.Claimed != 1 || ct.Free != 1 {
		t.Errorf("CT pool = %+v", ct)
	}
	// vmdesk-3 carries a legacy label-only assignment: neither claimed nor free.
	if vm.ID != "vm/vdi/vmdesk" || vm.Mode != PoolModePersistent || vm.Members != 3 || vm.Claimed != 1 || vm.Free != 1 {
		t.Errorf("VM pool = %+v", vm)
	}
}

func TestKubeCluster_MemberStateAndStart(t *testing.T) {
	fake := withFake(t)
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(1))
	fake.AddResponseKV("kubectl", []string{"get", "vm", "vmdesk-1", "-n", "vdi", "-o", "json"}, `{"status":{"printableStatus":"Stopped"}}`, nil)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"start", "vmdesk-1", "-n", "vdi"}, "", nil)

	k := KubeCluster()
	vm := MemberClaim{Member: MemberRef{Kind: PoolKindVM, Namespace: "vdi", Name: "vmdesk-1"}, Pool: "vmdesk"}
	if s, err := k.MemberState(vm); err != nil || s.Running || s.Phase != "Stopped" {
		t.Fatalf("VM state = %+v, %v", s, err)
	}
	if err := k.StartMember(vm); err != nil {
		t.Fatal(err)
	}
	ct := MemberClaim{Member: MemberRef{Kind: PoolKindCT, Namespace: "vdi", Name: "desk-1"}, Pool: "desk"}
	if s, err := k.MemberState(ct); err != nil || !s.Running || s.Phase != string(CTMemberReady) {
		t.Fatalf("CT state = %+v, %v", s, err)
	}
	gone := MemberClaim{Member: MemberRef{Kind: PoolKindCT, Namespace: "vdi", Name: "desk-9"}, Pool: "desk"}
	if s, _ := k.MemberState(gone); s.Phase != string(CTMemberMissing) {
		t.Fatalf("missing CT state = %+v", s)
	}
	f.member("desk-1").PodPhase = ""
	if err := k.StartMember(ct); err != nil || f.starts["desk-1"] != 1 {
		t.Fatalf("CT start: %v (starts %d)", err, f.starts["desk-1"])
	}
}

func TestKubeCluster_ClaimAndReleaseCTMember(t *testing.T) {
	withFake(t)
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(1))
	k := KubeCluster()
	member, err := k.ClaimMember(PoolRef{Kind: PoolKindCT, Namespace: "vdi", Name: "desk"}, "alice")
	if err != nil || member != "desk-1" || f.leaseHolder("desk-1") != "alice" {
		t.Fatalf("CT claim = %q, %v", member, err)
	}
	c := MemberClaim{Member: MemberRef{Kind: PoolKindCT, Namespace: "vdi", Name: "desk-1"}, Pool: "desk", Holder: "mallory"}
	if err := k.ReleaseMember(c); err == nil || f.leaseHolder("desk-1") != "alice" {
		t.Fatalf("a release naming the wrong holder ended alice's claim: %v", err)
	}
	c.Holder = "alice"
	if err := k.ReleaseMember(c); err != nil || f.leaseCount() != 0 || f.creates["desk-1"] != 2 {
		t.Fatalf("release: %v (leases %d, creates %d)", err, f.leaseCount(), f.creates["desk-1"])
	}
}

func TestClaim_ReturnsTheMemberAlreadyHeldFirst(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "-n", "corral-vms", "-o", "json", "-l", labelPool + "=devpool"}, `{"items":[
		{"metadata":{"name":"devpool-1","labels":{"corral.dev/vdi-pool":"devpool"}},"status":{"printableStatus":"Running"}},
		{"metadata":{"name":"devpool-2","labels":{"corral.dev/vdi-pool":"devpool","corral.dev/vdi-assigned-to":"alice_example.com"},
			"annotations":{"corral.dev/vdi-assigned-to":"alice@example.com"}},"status":{"printableStatus":"Running"}}
	]}`, nil)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", &fakeErr{"AlreadyExists"})
	fake.AddResponseKV("kubectl", []string{"get", "lease", leaseName("devpool-2"), "-n", "corral-vms", "-o", "json"},
		`{"metadata":{"resourceVersion":"4"},"spec":{"holderIdentity":"alice@example.com","renewTime":"2099-01-01T00:00:00Z","leaseDurationSeconds":3600}}`, nil)
	fake.AddResponseKV("kubectl", []string{"label", "vm", "devpool-2", "-n", "corral-vms", labelPool + "=devpool", labelAssignedTo + "=alice_example.com", "--overwrite"}, "", nil)
	fake.AddPrefixResponse("kubectl annotate vm devpool-2 -n corral-vms "+annoClaimedAt+"=", "", nil)

	got, err := Claim("corral-vms", "devpool", "alice@example.com")
	if err != nil || got != "devpool-2" {
		t.Fatalf("Claim = %q, %v; want alice's existing devpool-2", got, err)
	}
	var annotated bool
	for _, c := range fake.Calls() {
		if c.Args[0] == "annotate" && strings.Contains(strings.Join(c.Args, " "), annoAssignedTo+"=alice@example.com") {
			annotated = true
		}
	}
	if !annotated {
		t.Error("the exact identity was not recorded in the assigned-to annotation")
	}
}

func TestClaim_StartFailureKeepsTheClaim(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "-n", "corral-vms", "-o", "json", "-l", labelPool + "=devpool"},
		`{"items":[{"metadata":{"name":"devpool-1","labels":{"corral.dev/vdi-pool":"devpool"}},"status":{"printableStatus":"Stopped"}}]}`, nil)
	fake.AddResponseKV("kubectl", []string{"create", "-f", "-"}, "", nil)
	fake.AddResponseKV("kubectl", []string{"label", "vm", "devpool-1", "-n", "corral-vms", labelPool + "=devpool", labelAssignedTo + "=alice", "--overwrite"}, "", nil)
	fake.AddPrefixResponse("kubectl annotate vm devpool-1 -n corral-vms "+annoClaimedAt+"=", "", nil)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"start", "devpool-1", "-n", "corral-vms"}, "", &fakeErr{"insufficient memory"})

	got, err := Claim("corral-vms", "devpool", "alice")
	var startErr *StartError
	if got != "devpool-1" || !errors.As(err, &startErr) || startErr.Member != "devpool-1" {
		t.Fatalf("Claim = %q, %v; want devpool-1 with a StartError", got, err)
	}
	for _, c := range fake.Calls() {
		if c.Args[0] == "delete" {
			t.Fatalf("a failed start withdrew the claim: %+v", c)
		}
	}
}

func TestClaim_ErrorsAreTyped(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "-n", "corral-vms", "-o", "json", "-l", labelPool + "=ghost"}, `{"items":[]}`, nil)
	if _, err := Claim("corral-vms", "ghost", "alice"); !errors.Is(err, ErrPoolNotFound) {
		t.Errorf("missing pool: %v", err)
	}
	if _, err := Claim("corral-vms", "ghost", CTReconcilerHolder); err == nil {
		t.Error("claimed as the reconciler")
	}
}

func TestRelease_StopFailureKeepsTheClaim(t *testing.T) {
	fake := withFake(t)
	fake.AddResponse("kubectl "+leaseGetArgs, leaseJSON("alice", time.Now().UTC().Format(time.RFC3339Nano), "7"), nil)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"stop", "devpool-1", "-n", "corral-vms"}, "", &fakeErr{"connection refused"})
	err := Release("corral-vms", "devpool-1", "alice")
	if err == nil || !strings.Contains(err.Error(), "claim is kept") {
		t.Fatalf("Release = %v, want a retryable stop failure", err)
	}
	for _, c := range fake.Calls() {
		if c.Args[0] == "delete" || c.Args[0] == "label" || c.Args[0] == "annotate" {
			t.Fatalf("a failed stop changed the claim: %+v", c)
		}
	}
}

func TestRelease_AlreadyStoppedMemberStillReleases(t *testing.T) {
	fake := withFake(t)
	fake.AddResponse("kubectl "+leaseGetArgs, leaseJSON("alice", time.Now().UTC().Format(time.RFC3339Nano), "7"), nil)
	fake.AddResponseKV("/fake/bin/virtctl", []string{"stop", "devpool-1", "-n", "corral-vms"}, "", &fakeErr{"VM is not running"})
	fake.AddResponseKV("kubectl", []string{"get", "vm", "devpool-1", "-n", "corral-vms", "-o",
		`jsonpath={.metadata.labels.corral\.dev/vdi-pool}`}, "devpool", nil)
	fake.AddResponseKV("kubectl", []string{"label", "vm", "devpool-1", "-n", "corral-vms", labelPool + "=devpool", labelAssignedTo + "-", "--overwrite"}, "", nil)
	fake.AddResponseKV("kubectl", []string{"annotate", "vm", "devpool-1", "-n", "corral-vms", annoClaimedAt + "-", annoAssignedTo + "-", "--overwrite"}, "", nil)
	fake.AddPrefixResponse("kubectl delete", "", nil)
	if err := Release("corral-vms", "devpool-1", "alice"); err != nil {
		t.Fatalf("Release of a stopped member: %v", err)
	}
}

func TestLabelSafe(t *testing.T) {
	cases := map[string]string{
		"alice":                        "alice",
		"alice@example.com":            "alice_example.com",
		"@github":                      "github",
		"--":                           "claimed",
		strings.Repeat("a", 70) + "@x": strings.Repeat("a", 63),
	}
	for in, want := range cases {
		if got := labelSafe(in); got != want {
			t.Errorf("labelSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
