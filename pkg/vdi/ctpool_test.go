package vdi

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCT is an in-memory ctCluster. Leases follow the API server's
// optimistic-concurrency rules: create fails on an existing name, replace
// and delete are preconditioned on resourceVersion.
type fakeCT struct {
	mu       sync.Mutex
	pools    map[string]CTPoolSpec
	statuses map[string]CTPoolStatus
	members  map[string]*ctMemberObs // ns/name
	leases   map[string]lease        // ns/leaseName
	rv       int

	createFail map[string]int // member -> remaining failures
	deleteFail map[string]int
	creates    map[string]int
	deletes    map[string]int
	starts     map[string]int
	newPhase   string // pod phase of a freshly created member
	// beforeCreateClaim runs (unlocked) before each CreateClaim.
	beforeCreateClaim func(ref leaseRef, holder string)
}

func newFakeCT(t *testing.T) *fakeCT {
	t.Helper()
	f := &fakeCT{
		pools: map[string]CTPoolSpec{}, statuses: map[string]CTPoolStatus{},
		members: map[string]*ctMemberObs{}, leases: map[string]lease{},
		createFail: map[string]int{}, deleteFail: map[string]int{},
		creates: map[string]int{}, deletes: map[string]int{}, starts: map[string]int{},
		newPhase: "Running",
	}
	origC, origNow := ctc, ctNow
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	ctNow = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	advance = func(d time.Duration) { clockMu.Lock(); clock = clock.Add(d); clockMu.Unlock() }
	ctc = f
	t.Cleanup(func() { ctc, ctNow = origC, origNow })
	return f
}

// advance moves the pinned clock of the current fakeCT test.
var advance func(time.Duration)

func ctKey(ns, name string) string { return ns + "/" + name }

func (f *fakeCT) GetPool(ns, name string) (CTPoolSpec, CTPoolStatus, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	spec, ok := f.pools[ctKey(ns, name)]
	return spec, f.statuses[ctKey(ns, name)], ok, nil
}

func (f *fakeCT) ListPools(ns string) ([]CTPoolSpec, []CTPoolStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var specs []CTPoolSpec
	var statuses []CTPoolStatus
	for k, s := range f.pools {
		if ns == "" || s.Namespace == ns {
			specs, statuses = append(specs, s), append(statuses, f.statuses[k])
		}
	}
	return specs, statuses, nil
}

func (f *fakeCT) PutPoolSpec(spec CTPoolSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pools[ctKey(spec.Namespace, spec.Name)] = spec
	return nil
}

func (f *fakeCT) PutPoolStatus(ns, name string, status CTPoolStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[ctKey(ns, name)] = status
	return nil
}

func (f *fakeCT) DeletePool(ns, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pools, ctKey(ns, name))
	delete(f.statuses, ctKey(ns, name))
	return nil
}

func (f *fakeCT) ListMembers(ns, pool string) ([]ctMemberObs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ctMemberObs
	for k, m := range f.members {
		if strings.HasPrefix(k, ns+"/"+pool+"-") {
			out = append(out, *m)
		}
	}
	return out, nil
}

func (f *fakeCT) CreateMember(spec CTPoolSpec, member string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createFail[member] > 0 {
		f.createFail[member]--
		return fmt.Errorf("exceeded quota")
	}
	f.creates[member]++
	f.members[ctKey(spec.Namespace, member)] = &ctMemberObs{Name: member, SourceHash: spec.Source.Hash(), PodPhase: f.newPhase, PodReady: f.newPhase == "Running"}
	return nil
}

func (f *fakeCT) StartMember(ns, member string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.members[ctKey(ns, member)]
	if !ok {
		return fmt.Errorf("no CT %s", member)
	}
	f.starts[member]++
	m.PodPhase, m.PodReady = "Running", true
	return nil
}

func (f *fakeCT) DeleteMember(ns, member string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteFail[member] > 0 {
		f.deleteFail[member]--
		return fmt.Errorf("delete timed out")
	}
	f.deletes[member]++
	delete(f.members, ctKey(ns, member))
	return nil
}

func (f *fakeCT) SetAssignee(ns, member, identity string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.members[ctKey(ns, member)]; ok {
		m.AssignedTo = identity
	}
	return nil
}

func (f *fakeCT) ListClaims(ns, pool string) (map[string]lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]lease{}
	for k, l := range f.leases {
		if strings.HasPrefix(k, ns+"/") && l.Metadata.Labels[labelCTPool] == pool {
			out[l.Metadata.Labels[labelVDIMember]] = l
		}
	}
	return out, nil
}

func (f *fakeCT) GetClaim(ref leaseRef) (lease, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.leases[ctKey(ref.Namespace, ref.Name)]
	return l, ok, nil
}

func (f *fakeCT) newLease(ref leaseRef, holder string, d time.Duration) lease {
	f.rv++
	var l lease
	l.Metadata.Name, l.Metadata.ResourceVersion, l.Metadata.Labels = ref.Name, strconv.Itoa(f.rv), ref.Labels
	now := ctNow().Format(time.RFC3339Nano)
	l.Spec.HolderIdentity, l.Spec.AcquireTime, l.Spec.RenewTime = holder, now, now
	l.Spec.LeaseDurationSeconds = int32(d.Seconds())
	return l
}

func (f *fakeCT) CreateClaim(ref leaseRef, holder string, d time.Duration) (bool, error) {
	if hook := f.beforeCreateClaim; hook != nil {
		hook(ref, holder)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.leases[ctKey(ref.Namespace, ref.Name)]; ok {
		return false, nil
	}
	f.leases[ctKey(ref.Namespace, ref.Name)] = f.newLease(ref, holder, d)
	return true, nil
}

func (f *fakeCT) ReplaceClaim(ref leaseRef, holder string, d time.Duration, rv string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.leases[ctKey(ref.Namespace, ref.Name)]
	if !ok || cur.Metadata.ResourceVersion != rv {
		return false
	}
	f.leases[ctKey(ref.Namespace, ref.Name)] = f.newLease(ref, holder, d)
	return true
}

func (f *fakeCT) DeleteClaim(ref leaseRef, rv string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.leases[ctKey(ref.Namespace, ref.Name)]
	if !ok {
		return nil
	}
	if cur.Metadata.ResourceVersion != rv {
		return fmt.Errorf("conflict: lease %s is at %s, not %s", ref.Name, cur.Metadata.ResourceVersion, rv)
	}
	delete(f.leases, ctKey(ref.Namespace, ref.Name))
	return nil
}

// test helpers

func (f *fakeCT) member(name string) *ctMemberObs {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.members[ctKey("vdi", name)]
}

func (f *fakeCT) leaseHolder(member string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.leases[ctKey("vdi", ctLeasePrefix+member)].Spec.HolderIdentity
}

func (f *fakeCT) leaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.leases)
}

func (f *fakeCT) memberCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.members)
}

func (f *fakeCT) putLease(member, holder string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref := ctLeaseRef("vdi", "desk", member)
	f.leases[ctKey("vdi", ref.Name)] = f.newLease(ref, holder, d)
}

func deskSpec(size int) CTPoolSpec {
	return CTPoolSpec{Name: "desk", Namespace: "vdi", Size: size, Source: CTSource{Image: "ghcr.io/example/desktop:1", Mem: "2Gi"}}
}

func applyAndReconcile(t *testing.T, spec CTPoolSpec) CTPoolStatus {
	t.Helper()
	if _, err := ApplyCTPool(spec); err != nil {
		t.Fatalf("ApplyCTPool: %v", err)
	}
	status, err := ReconcileCTPool(spec.Namespace, spec.Name)
	if err != nil {
		t.Fatalf("ReconcileCTPool: %v", err)
	}
	return status
}

func mustReconcile(t *testing.T) CTPoolStatus {
	t.Helper()
	status, err := ReconcileCTPool("vdi", "desk")
	if err != nil {
		t.Fatalf("ReconcileCTPool: %v", err)
	}
	return status
}

func rowOf(t *testing.T, status CTPoolStatus, name string) CTMemberStatus {
	t.Helper()
	for _, m := range status.Members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("status has no row for %s: %+v", name, status.Members)
	return CTMemberStatus{}
}

func TestCTPoolSpec_Validate(t *testing.T) {
	ok := deskSpec(2)
	ok.Mode = PoolModeEphemeral
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	cases := map[string]func(*CTPoolSpec){
		"bad name":      func(s *CTPoolSpec) { s.Name = "Desk_1" },
		"long name":     func(s *CTPoolSpec) { s.Name = strings.Repeat("a", ctPoolNameMax+1) },
		"no namespace":  func(s *CTPoolSpec) { s.Namespace = "" },
		"unknown mode":  func(s *CTPoolSpec) { s.Mode = "sticky" },
		"negative size": func(s *CTPoolSpec) { s.Size = -1 },
		"huge size":     func(s *CTPoolSpec) { s.Size = ctPoolSizeMax + 1 },
		"no image":      func(s *CTPoolSpec) { s.Source.Image = " " },
		"negative cpu":  func(s *CTPoolSpec) { s.Source.CPU = -1 },
	}
	for name, mutate := range cases {
		s := ok
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	persistent := ok
	persistent.Mode = PoolModePersistent
	if err := persistent.Validate(); !errors.Is(err, ErrPersistentCTPool) {
		t.Errorf("persistent mode: got %v, want ErrPersistentCTPool", err)
	}
}

func TestCTMemberIndex(t *testing.T) {
	cases := map[string]int{"desk-1": 1, "desk-12": 12, "desk-0": 0, "desk-01": 0, "desk-x": 0, "desk-1-1": 0, "other-1": 0, "desk": 0}
	for name, want := range cases {
		if got := ctMemberIndex("desk", name); got != want {
			t.Errorf("ctMemberIndex(desk, %q) = %d, want %d", name, got, want)
		}
	}
}

func TestCTSourceHash_ChangesWithSource(t *testing.T) {
	a := CTSource{Image: "img:1"}
	b := CTSource{Image: "img:2"}
	if a.Hash() == b.Hash() || a.Hash() != (CTSource{Image: "img:1"}).Hash() {
		t.Fatalf("hash must be stable per source and differ across sources")
	}
	if len(a.Hash()) > 63 {
		t.Fatalf("hash %q is not a valid label value", a.Hash())
	}
}

func TestApplyCTPool_DefaultsToEphemeralAndRejectsPersistent(t *testing.T) {
	f := newFakeCT(t)
	spec, err := ApplyCTPool(deskSpec(1))
	if err != nil || spec.Mode != PoolModeEphemeral {
		t.Fatalf("ApplyCTPool = %+v, %v; want ephemeral mode", spec, err)
	}
	p := deskSpec(1)
	p.Name, p.Mode = "keep", PoolModePersistent
	if _, err := ApplyCTPool(p); !errors.Is(err, ErrPersistentCTPool) {
		t.Fatalf("persistent pool: got %v, want ErrPersistentCTPool", err)
	}
	if _, _, found, _ := f.GetPool("vdi", "keep"); found {
		t.Fatal("a rejected persistent pool must not be stored")
	}
}

func TestReconcileCTPool_CreatesMembersAndRecordsStatus(t *testing.T) {
	f := newFakeCT(t)
	status := applyAndReconcile(t, deskSpec(3))

	if status.Desired != 3 || status.Observed != 3 || status.Ready != 3 || status.Free != 3 || status.Exhausted {
		t.Fatalf("status = %+v", status)
	}
	for i := 1; i <= 3; i++ {
		name := CTMemberName("desk", i)
		if f.member(name) == nil || f.creates[name] != 1 {
			t.Errorf("%s: created %d times, want 1", name, f.creates[name])
		}
		if row := rowOf(t, status, name); row.State != CTMemberReady || row.Index != i {
			t.Errorf("%s row = %+v", name, row)
		}
	}
	if f.leaseCount() != 0 {
		t.Errorf("reconcile left %d maintenance leases behind", f.leaseCount())
	}
	_, recorded, _ := GetCTPool("vdi", "desk")
	if recorded.Ready != 3 || recorded.SourceHash != deskSpec(3).Source.Hash() {
		t.Errorf("recorded status = %+v", recorded)
	}
}

func TestReconcileCTPool_DuplicateEventsAreNoOps(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(2))
	for range 3 {
		mustReconcile(t)
	}
	for i := 1; i <= 2; i++ {
		name := CTMemberName("desk", i)
		if f.creates[name] != 1 || f.deletes[name] != 0 || f.starts[name] != 0 {
			t.Errorf("%s: creates=%d deletes=%d starts=%d after duplicate reconciles; want 1/0/0",
				name, f.creates[name], f.deletes[name], f.starts[name])
		}
	}
}

func TestReconcileCTPool_ConcurrentReconcilesConverge(t *testing.T) {
	f := newFakeCT(t)
	if _, err := ApplyCTPool(deskSpec(4)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _, _ = ReconcileCTPool("vdi", "desk") })
	}
	wg.Wait()
	status := mustReconcile(t)
	if status.Observed != 4 || status.Free != 4 || f.leaseCount() != 0 {
		t.Fatalf("after concurrent reconciles: status=%+v leases=%d", status, f.leaseCount())
	}
	for name, n := range f.deletes {
		t.Errorf("concurrent reconciles destroyed %s %d times", name, n)
	}
}

func TestReconcileCTPool_FailedCreationIsObservableAndRetried(t *testing.T) {
	f := newFakeCT(t)
	f.createFail["desk-2"] = 1
	if _, err := ApplyCTPool(deskSpec(3)); err != nil {
		t.Fatal(err)
	}
	status, err := ReconcileCTPool("vdi", "desk")
	if err == nil || !strings.Contains(err.Error(), "desk-2") {
		t.Fatalf("expected an incomplete-reconcile error naming desk-2, got %v", err)
	}
	if row := rowOf(t, status, "desk-2"); row.State != CTMemberMissing {
		t.Errorf("desk-2 row = %+v, want Missing", row)
	}
	if len(status.Errors) != 1 || status.Observed != 2 {
		t.Errorf("status = %+v, want one error and two members", status)
	}
	if _, recorded, _ := GetCTPool("vdi", "desk"); len(recorded.Errors) != 1 {
		t.Errorf("the failure must be recorded in the pool status, got %+v", recorded)
	}
	if f.leaseCount() != 0 {
		t.Fatalf("a failed create must not leave a maintenance lease that blocks the retry")
	}

	status = mustReconcile(t)
	if status.Observed != 3 || len(status.Errors) != 0 || f.creates["desk-2"] != 1 {
		t.Fatalf("retry did not repair desk-2: %+v", status)
	}
}

func TestReconcileCTPool_ScaleUpAndDown(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(2))

	status := applyAndReconcile(t, deskSpec(4))
	if status.Observed != 4 || f.member("desk-4") == nil {
		t.Fatalf("scale up: %+v", status)
	}
	if f.creates["desk-1"] != 1 {
		t.Errorf("scale up recreated an existing member")
	}

	status = applyAndReconcile(t, deskSpec(1))
	if status.Observed != 1 || f.member("desk-1") == nil || f.memberCount() != 1 {
		t.Fatalf("scale down: %+v (members %d)", status, f.memberCount())
	}
	for _, name := range []string{"desk-2", "desk-3", "desk-4"} {
		if f.deletes[name] != 1 {
			t.Errorf("%s deleted %d times, want 1", name, f.deletes[name])
		}
	}

	status = applyAndReconcile(t, deskSpec(0))
	if status.Observed != 0 || status.Exhausted {
		t.Fatalf("scale to zero: %+v", status)
	}
}

func TestReconcileCTPool_ReplacesDriftedAndFailedMembers(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(3))
	f.member("desk-3").PodPhase = "Failed"

	updated := deskSpec(3)
	updated.Source.Image = "ghcr.io/example/desktop:2"
	status := applyAndReconcile(t, updated)

	for i := 1; i <= 3; i++ {
		name := CTMemberName("desk", i)
		if f.deletes[name] != 1 || f.creates[name] != 2 {
			t.Errorf("%s: deletes=%d creates=%d, want a single replacement", name, f.deletes[name], f.creates[name])
		}
		if m := f.member(name); m == nil || m.SourceHash != updated.Source.Hash() {
			t.Errorf("%s was not rebuilt from the new source", name)
		}
		if rowOf(t, status, name).Drifted {
			t.Errorf("%s still reported as drifted", name)
		}
	}

	f.member("desk-1").PodPhase = "Failed"
	mustReconcile(t)
	if f.creates["desk-1"] != 3 || f.member("desk-1").state() != CTMemberReady {
		t.Errorf("a failed member with an up-to-date source must be replaced too")
	}
}

func TestReconcileCTPool_StartsStoppedMembers(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(2))
	f.member("desk-1").PodPhase = ""
	f.member("desk-2").PodPhase = ""
	f.putLease("desk-2", "alice", claimDuration)

	mustReconcile(t)
	if f.starts["desk-1"] != 1 || f.starts["desk-2"] != 1 {
		t.Fatalf("starts = %v; stopped members (claimed or not) must be started", f.starts)
	}
	if f.deletes["desk-1"]+f.deletes["desk-2"] != 0 {
		t.Fatalf("starting must not destroy anything: %v", f.deletes)
	}
}

func TestReconcileCTPool_NeverDestroysAClaimedMember(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(3))
	member, err := ClaimCTMember("vdi", "desk", "alice@example.com")
	if err != nil {
		t.Fatalf("ClaimCTMember: %v", err)
	}
	f.member(member).PodPhase = "Failed"
	advance(2 * claimDuration) // the claim expires; it still protects the desktop

	updated := deskSpec(0)
	updated.Source.Image = "ghcr.io/example/desktop:2"
	status := applyAndReconcile(t, updated)

	if f.member(member) == nil || f.deletes[member] != 0 {
		t.Fatalf("claimed member %s was destroyed", member)
	}
	row := rowOf(t, status, member)
	if row.ClaimedBy != "alice@example.com" || !row.ClaimExpired || !row.Drifted || !row.Excess || row.State != CTMemberFailed {
		t.Errorf("protected member row = %+v", row)
	}
	if status.Observed != 1 || status.Claimed != 1 {
		t.Errorf("only the claimed member should remain: %+v", status)
	}
}

func TestReconcileCTPool_ClaimRacingAReplacementWins(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(1))
	updated := deskSpec(1)
	updated.Source.Image = "ghcr.io/example/desktop:2"
	if _, err := ApplyCTPool(updated); err != nil {
		t.Fatal(err)
	}
	// Between the reconciler's observation and its Lease, a user claims
	// the member.
	f.beforeCreateClaim = func(ref leaseRef, holder string) {
		if holder == CTReconcilerHolder && ref.Name == ctLeasePrefix+"desk-1" {
			f.beforeCreateClaim = nil
			f.putLease("desk-1", "bob", claimDuration)
		}
	}
	mustReconcile(t)
	if f.deletes["desk-1"] != 0 || f.leaseHolder("desk-1") != "bob" {
		t.Fatalf("a member claimed mid-reconcile was destroyed (deletes=%d holder=%q)", f.deletes["desk-1"], f.leaseHolder("desk-1"))
	}
}

func TestReconcileCTPool_MaintenanceLeases(t *testing.T) {
	f := newFakeCT(t)
	if _, err := ApplyCTPool(deskSpec(1)); err != nil {
		t.Fatal(err)
	}
	f.putLease("desk-1", CTReconcilerHolder, ctMaintenanceDuration)

	status := mustReconcile(t)
	if f.member("desk-1") != nil || !rowOf(t, status, "desk-1").Maintenance {
		t.Fatalf("a member under live maintenance must be left to its owner: %+v", status)
	}

	advance(ctMaintenanceDuration + time.Second) // the other reconciler crashed
	status = mustReconcile(t)
	if f.member("desk-1") == nil || f.leaseCount() != 0 || status.Free != 1 {
		t.Fatalf("an expired maintenance lease must be taken over and cleared: %+v leases=%d", status, f.leaseCount())
	}
}

func TestReconcileCTPool_RecreatesAVanishedClaimedMember(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(1))
	if _, err := ClaimCTMember("vdi", "desk", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := f.DeleteMember("vdi", "desk-1"); err != nil {
		t.Fatal(err)
	}
	mustReconcile(t)
	if f.member("desk-1") == nil || f.leaseHolder("desk-1") != "alice" {
		t.Fatalf("vanished member: recreated=%v holder=%q", f.member("desk-1") != nil, f.leaseHolder("desk-1"))
	}
}

func TestReconcileCTPool_UnschedulableMembersReportCapacity(t *testing.T) {
	f := newFakeCT(t)
	f.newPhase = "Pending"
	applyAndReconcile(t, deskSpec(2))
	for _, name := range []string{"desk-1", "desk-2"} {
		f.member(name).Unschedulable = "0/3 nodes are available: 3 Insufficient memory."
	}
	status := mustReconcile(t)
	if status.Unschedulable != 2 || status.Free != 0 || !status.Exhausted {
		t.Fatalf("capacity not observable: %+v", status)
	}
	if row := rowOf(t, status, "desk-1"); row.State != CTMemberUnschedulable || !strings.Contains(row.Message, "Insufficient memory") {
		t.Errorf("row = %+v", row)
	}
	_, err := ClaimCTMember("vdi", "desk", "alice")
	if !errors.Is(err, ErrPoolExhausted) || !strings.Contains(err.Error(), "2 unschedulable") {
		t.Fatalf("claim on an unschedulable pool: %v", err)
	}

	// Capacity frees up: the scheduler places the pods without help.
	for _, name := range []string{"desk-1", "desk-2"} {
		m := f.member(name)
		m.Unschedulable, m.PodPhase, m.PodReady = "", "Running", true
	}
	if status := mustReconcile(t); status.Free != 2 || status.Exhausted {
		t.Fatalf("pool did not recover: %+v", status)
	}
}

func TestReconcileCTPool_PersistentModeFailsExplicitly(t *testing.T) {
	f := newFakeCT(t)
	spec := deskSpec(1)
	spec.Mode = PoolModePersistent // e.g. an edited ConfigMap
	_ = f.PutPoolSpec(spec)

	status, err := ReconcileCTPool("vdi", "desk")
	if !errors.Is(err, ErrPersistentCTPool) || len(status.Errors) != 1 {
		t.Fatalf("reconcile = %+v, %v; want ErrPersistentCTPool", status, err)
	}
	if f.memberCount() != 0 {
		t.Fatal("a persistent pool must not get members")
	}
	if _, err := ClaimCTMember("vdi", "desk", "alice"); !errors.Is(err, ErrPersistentCTPool) {
		t.Errorf("claim: %v", err)
	}
	if err := ReleaseCTMember("vdi", "desk", "desk-1", "alice", false); !errors.Is(err, ErrPersistentCTPool) {
		t.Errorf("release: %v", err)
	}
}

func TestReconcileCTPool_UnknownPool(t *testing.T) {
	newFakeCT(t)
	if _, err := ReconcileCTPool("vdi", "ghost"); err == nil {
		t.Fatal("expected an error for an undefined pool")
	}
}

func TestClaimCTMember_ConcurrentClaimsGetDistinctMembers(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(3))

	var mu sync.Mutex
	got := map[string]string{}
	exhausted := 0
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			user := fmt.Sprintf("user%d", i)
			member, err := ClaimCTMember("vdi", "desk", user)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, ErrPoolExhausted):
				exhausted++
			case err != nil:
				t.Errorf("%s: %v", user, err)
			default:
				if prev, dup := got[member]; dup {
					t.Errorf("%s handed to both %s and %s", member, prev, user)
				}
				got[member] = user
			}
		})
	}
	wg.Wait()
	if len(got) != 3 || exhausted != 7 {
		t.Fatalf("claims = %v, exhausted = %d; want 3 distinct claims and 7 exhausted", got, exhausted)
	}
	for member, user := range got {
		if f.leaseHolder(member) != user || f.member(member).AssignedTo != user {
			t.Errorf("%s: lease holder %q, assignee %q, want %q", member, f.leaseHolder(member), f.member(member).AssignedTo, user)
		}
	}
}

func TestClaimCTMember_IdempotentAndRenewsExpiredClaim(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(2))
	first, err := ClaimCTMember("vdi", "desk", "alice")
	if err != nil {
		t.Fatal(err)
	}
	again, err := ClaimCTMember("vdi", "desk", "alice")
	if err != nil || again != first {
		t.Fatalf("second claim = %q, %v; want %q", again, err, first)
	}
	advance(2 * claimDuration)
	renewed, err := ClaimCTMember("vdi", "desk", "alice")
	if err != nil || renewed != first {
		t.Fatalf("claim after expiry = %q, %v; want the same member %q", renewed, err, first)
	}
	l, _, _ := f.GetClaim(ctLeaseRef("vdi", "desk", first))
	if leaseExpired(l, ctNow()) {
		t.Fatal("re-claiming must renew an expired claim")
	}
}

func TestClaimCTMember_PrefersReadySkipsDriftedAndStartsStopped(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(3))
	f.member("desk-1").SourceHash = "stale"
	f.member("desk-2").PodPhase = ""

	member, err := ClaimCTMember("vdi", "desk", "alice")
	if err != nil || member != "desk-3" {
		t.Fatalf("first claim = %q, %v; want the ready, current desk-3", member, err)
	}
	member, err = ClaimCTMember("vdi", "desk", "bob")
	if err != nil || member != "desk-2" || f.starts["desk-2"] != 1 {
		t.Fatalf("second claim = %q, %v (starts %d); want stopped desk-2 started", member, err, f.starts["desk-2"])
	}
	if _, err := ClaimCTMember("vdi", "desk", "carol"); !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("a drifted member must not be handed out: %v", err)
	}
}

func TestClaimCTMember_RejectsBadIdentities(t *testing.T) {
	newFakeCT(t)
	applyAndReconcile(t, deskSpec(1))
	for _, id := range []string{"", "  ", CTReconcilerHolder} {
		if _, err := ClaimCTMember("vdi", "desk", id); err == nil {
			t.Errorf("identity %q must be rejected", id)
		}
	}
}

func TestReleaseCTMember_DestroysAndRecreatesFromSource(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(2))
	member, err := ClaimCTMember("vdi", "desk", "alice")
	if err != nil {
		t.Fatal(err)
	}
	f.member(member).PodPhase = "Failed" // whatever state the user left it in

	if err := ReleaseCTMember("vdi", "desk", member, "alice", false); err != nil {
		t.Fatalf("ReleaseCTMember: %v", err)
	}
	m := f.member(member)
	if f.deletes[member] != 1 || f.creates[member] != 2 || m == nil || m.AssignedTo != "" || m.state() != CTMemberReady {
		t.Fatalf("release did not recycle %s: deletes=%d creates=%d member=%+v", member, f.deletes[member], f.creates[member], m)
	}
	if f.leaseCount() != 0 {
		t.Fatalf("release left %d leases", f.leaseCount())
	}
	if again, err := ClaimCTMember("vdi", "desk", "bob"); err != nil || again != member {
		// Both members are free; the lowest index wins on a tie.
		t.Fatalf("recycled member is not claimable: %q, %v", again, err)
	}

	if err := ReleaseCTMember("vdi", "desk", "desk-2", "carol", false); err != nil {
		t.Errorf("releasing an unclaimed member must be a no-op, got %v", err)
	}
	if f.deletes["desk-2"] != 0 {
		t.Error("a no-op release destroyed a member")
	}
}

func TestReleaseCTMember_OwnershipAndForce(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(1))
	member, err := ClaimCTMember("vdi", "desk", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := ReleaseCTMember("vdi", "desk", member, "mallory", false); err == nil || !strings.Contains(err.Error(), "claimed by") {
		t.Fatalf("non-owner release: %v", err)
	}
	if f.deletes[member] != 0 || f.leaseHolder(member) != "alice" {
		t.Fatal("a rejected release mutated the member")
	}
	if err := ReleaseCTMember("vdi", "desk", member, "", false); err == nil {
		t.Fatal("an anonymous non-forced release must be rejected")
	}
	if err := ReleaseCTMember("vdi", "desk", member, "", true); err != nil {
		t.Fatalf("forced release: %v", err)
	}
	if f.deletes[member] != 1 || f.leaseCount() != 0 || f.member(member) == nil {
		t.Fatal("a forced release must recycle the member and clear the claim")
	}
}

func TestReleaseCTMember_ExcessMemberIsNotRecreated(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(2))
	if _, err := ClaimCTMember("vdi", "desk", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimCTMember("vdi", "desk", "bob"); err != nil {
		t.Fatal(err)
	}
	applyAndReconcile(t, deskSpec(1)) // desk-2 is claimed, so it drains
	if f.member("desk-2") == nil {
		t.Fatal("scale down destroyed a claimed member")
	}
	if err := ReleaseCTMember("vdi", "desk", "desk-2", f.leaseHolder("desk-2"), false); err != nil {
		t.Fatal(err)
	}
	if f.member("desk-2") != nil || f.creates["desk-2"] != 1 {
		t.Fatal("releasing a member above the pool size must not recreate it")
	}
}

func TestReleaseCTMember_RecreateFailureIsRepairedByReconcile(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(1))
	if _, err := ClaimCTMember("vdi", "desk", "alice"); err != nil {
		t.Fatal(err)
	}
	f.createFail["desk-1"] = 1
	err := ReleaseCTMember("vdi", "desk", "desk-1", "alice", false)
	if err == nil || !strings.Contains(err.Error(), "recreate") {
		t.Fatalf("expected a recreate error, got %v", err)
	}
	if f.leaseCount() != 0 {
		t.Fatal("a failed recreate must not leave the member claimed or under maintenance")
	}
	if err := ReleaseCTMember("vdi", "desk", "desk-1", "alice", false); err != nil {
		t.Fatalf("a repeated release must be a no-op, got %v", err)
	}
	if status := mustReconcile(t); status.Free != 1 {
		t.Fatalf("reconcile did not repair the released member: %+v", status)
	}
}

func TestDeleteCTPool(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(2))
	if _, err := ClaimCTMember("vdi", "desk", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteCTPool("vdi", "desk", false); err == nil || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("delete with a claimed member: %v", err)
	}
	if f.memberCount() != 2 {
		t.Fatal("a refused delete must not remove members")
	}
	if err := DeleteCTPool("vdi", "desk", true); err != nil {
		t.Fatalf("forced delete: %v", err)
	}
	if f.memberCount() != 0 || f.leaseCount() != 0 {
		t.Fatalf("forced delete left members=%d leases=%d", f.memberCount(), f.leaseCount())
	}
	if _, _, found, _ := f.GetPool("vdi", "desk"); found {
		t.Fatal("the pool definition survived its delete")
	}
}

func TestDeleteCTPool_UnclaimedDeletesUnderLease(t *testing.T) {
	f := newFakeCT(t)
	applyAndReconcile(t, deskSpec(2))
	f.deleteFail["desk-2"] = 1
	if err := DeleteCTPool("vdi", "desk", false); err == nil {
		t.Fatal("a failed member delete must keep the pool definition")
	}
	spec, _, err := GetCTPool("vdi", "desk")
	if err != nil || spec.Size != 0 {
		t.Fatalf("pool should remain, drained to size 0: %+v, %v", spec, err)
	}
	if err := DeleteCTPool("vdi", "desk", false); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if f.memberCount() != 0 || f.leaseCount() != 0 {
		t.Fatalf("delete left members=%d leases=%d", f.memberCount(), f.leaseCount())
	}
}

func TestObserveCTPool_IsReadOnly(t *testing.T) {
	f := newFakeCT(t)
	if _, err := ApplyCTPool(deskSpec(2)); err != nil {
		t.Fatal(err)
	}
	status, err := ObserveCTPool("vdi", "desk")
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Members) != 2 || status.Members[0].State != CTMemberMissing || f.memberCount() != 0 {
		t.Fatalf("observe = %+v, members %d", status, f.memberCount())
	}
	if _, recorded, _ := GetCTPool("vdi", "desk"); !recorded.ObservedAt.IsZero() {
		t.Fatal("observe must not record status")
	}
}
