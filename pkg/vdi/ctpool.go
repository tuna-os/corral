package vdi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CT-backed ephemeral desktop pools (#230). Kubernetes has no
// VirtualMachinePool equivalent for pods with a durable identity, so this
// file is a small reconciler of its own:
//
//   - The pool definition is a ConfigMap (corral-vdi-ctpool-<pool>). Its
//     data.spec is the declarative source and size; ReconcileCTPool writes
//     the observed state back to data.status.
//   - Members are ordinary Corral CTs named <pool>-<n>, n in 1..size. Their
//     data PVC carries the pool, member, and source-hash labels.
//   - Claims are per-member coordination.k8s.io Leases, the #226 contract.
//     The reconciler takes the same Lease under its own holder identity
//     before it creates, replaces, or deletes a member. A user claim and a
//     destructive repair therefore never interleave.

const (
	labelCTPool   = "corral.dev/vdi-ct-pool"
	labelCTMember = "corral.dev/vdi-ct-member"
	labelCTSource = "corral.dev/vdi-ct-source"
	// annoCTAssignedTo mirrors the claim holder on the member PVC. Like the
	// VM pool label it is presentation state; the Lease is authoritative.
	// It is an annotation because identities (e-mail addresses) are not
	// valid label values.
	annoCTAssignedTo = "corral.dev/vdi-assigned-to"

	ctPoolConfigPrefix = "corral-vdi-ctpool-"
	ctLeasePrefix      = "corral-vdi-ct-"

	// CTReconcilerHolder is the Lease holder the reconciler uses while it
	// creates, replaces, or deletes a member. Users cannot claim as it.
	CTReconcilerHolder    = "corral-vdi/reconciler"
	ctMaintenanceDuration = 10 * time.Minute

	CTPoolModeEphemeral  = "ephemeral"
	CTPoolModePersistent = "persistent"

	ctPoolNameMax = 40
	ctPoolSizeMax = 500
)

// ErrPersistentCTPool is returned for a pool whose mode is "persistent".
// A recreated member would destroy tenant state, so only ephemeral CT pools
// are supported.
var ErrPersistentCTPool = errors.New("persistent CT pools are unsupported: CT pool members are destroyed and recreated on release; use a VM pool for persistent desktops")

// ErrCTPoolExhausted is returned by ClaimCTMember when no member is free.
var ErrCTPoolExhausted = errors.New("CT pool has no free members")

var ctPoolNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ctNow is the reconciler's clock (a var so tests can pin it).
var ctNow = func() time.Time { return time.Now().UTC() }

// CTSource is what every member of a CT pool is created from.
type CTSource struct {
	Image        string `json:"image"`
	CPU          int    `json:"cpu,omitempty"`
	Mem          string `json:"mem,omitempty"`
	Disk         string `json:"disk,omitempty"`
	StorageClass string `json:"storageClass,omitempty"`
	Privileged   bool   `json:"privileged,omitempty"`
	Init         bool   `json:"init,omitempty"`
}

// Hash identifies the source. A member whose PVC carries a different hash
// has drifted from the pool definition.
func (s CTSource) Hash() string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// CTPoolSpec is the declarative definition of a CT pool.
type CTPoolSpec struct {
	Name      string   `json:"name"`
	Namespace string   `json:"namespace"`
	Mode      string   `json:"mode"`
	Size      int      `json:"size"`
	Source    CTSource `json:"source"`
}

// Validate reports whether the spec can be reconciled. Persistent mode
// fails with ErrPersistentCTPool.
func (s CTPoolSpec) Validate() error {
	switch {
	case !ctPoolNameRE.MatchString(s.Name) || len(s.Name) > ctPoolNameMax:
		return fmt.Errorf("CT pool name %q must be a DNS label of at most %d characters", s.Name, ctPoolNameMax)
	case s.Namespace == "":
		return fmt.Errorf("CT pool %q needs a namespace", s.Name)
	case s.Mode == CTPoolModePersistent:
		return ErrPersistentCTPool
	case s.Mode != CTPoolModeEphemeral:
		return fmt.Errorf("CT pool %q: unknown mode %q (want %q)", s.Name, s.Mode, CTPoolModeEphemeral)
	case s.Size < 0 || s.Size > ctPoolSizeMax:
		return fmt.Errorf("CT pool %q: size %d must be between 0 and %d", s.Name, s.Size, ctPoolSizeMax)
	case strings.TrimSpace(s.Source.Image) == "":
		return fmt.Errorf("CT pool %q needs a source image", s.Name)
	case s.Source.CPU < 0:
		return fmt.Errorf("CT pool %q: cpu must not be negative", s.Name)
	}
	return nil
}

// CTMemberName is the stable identity of the index-th member (1-based).
func CTMemberName(pool string, index int) string { return pool + "-" + strconv.Itoa(index) }

// ctMemberIndex parses a member name back to its index; 0 means the name
// is not a canonical member of pool.
func ctMemberIndex(pool, member string) int {
	rest, ok := strings.CutPrefix(member, pool+"-")
	if !ok {
		return 0
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 1 || strconv.Itoa(i) != rest {
		return 0
	}
	return i
}

func ctLeaseRef(namespace, pool, member string) leaseRef {
	return leaseRef{Namespace: namespace, Name: ctLeasePrefix + member, Labels: map[string]string{labelCTPool: pool, labelVDIMember: member}}
}

// CTMemberState is the observed lifecycle state of one member.
type CTMemberState string

const (
	CTMemberMissing       CTMemberState = "Missing"
	CTMemberStopped       CTMemberState = "Stopped"
	CTMemberStarting      CTMemberState = "Starting"
	CTMemberReady         CTMemberState = "Ready"
	CTMemberUnschedulable CTMemberState = "Unschedulable"
	CTMemberFailed        CTMemberState = "Failed"
	CTMemberTerminating   CTMemberState = "Terminating"
)

// CTMemberStatus is one member's row in the pool status.
type CTMemberStatus struct {
	Name         string        `json:"name"`
	Index        int           `json:"index,omitempty"`
	State        CTMemberState `json:"state"`
	ClaimedBy    string        `json:"claimedBy,omitempty"`
	ClaimExpired bool          `json:"claimExpired,omitempty"`
	Maintenance  bool          `json:"maintenance,omitempty"`
	Drifted      bool          `json:"drifted,omitempty"`
	Excess       bool          `json:"excess,omitempty"`
	Message      string        `json:"message,omitempty"`
}

// CTPoolStatus is the observable state of a CT pool. ReconcileCTPool
// records it in the pool ConfigMap.
type CTPoolStatus struct {
	Pool          string           `json:"pool"`
	Namespace     string           `json:"namespace"`
	Desired       int              `json:"desired"`
	Observed      int              `json:"observed"`
	Ready         int              `json:"ready"`
	Claimed       int              `json:"claimed"`
	Free          int              `json:"free"`
	Unschedulable int              `json:"unschedulable"`
	Exhausted     bool             `json:"exhausted"`
	SourceHash    string           `json:"sourceHash"`
	Members       []CTMemberStatus `json:"members,omitempty"`
	Errors        []string         `json:"errors,omitempty"`
	ObservedAt    time.Time        `json:"observedAt"`
}

// ctMemberObs is what the cluster reports about one member.
type ctMemberObs struct {
	Name          string
	SourceHash    string
	AssignedTo    string
	Terminating   bool   // the PVC or the pod is being deleted
	PodPhase      string // "" when the member has no pod (stopped)
	PodReady      bool
	Unschedulable string // scheduler message while the pod cannot be placed
}

func (m ctMemberObs) state() CTMemberState {
	switch {
	case m.Terminating:
		return CTMemberTerminating
	case m.PodPhase == "":
		return CTMemberStopped
	case m.PodPhase == "Failed" || m.PodPhase == "Succeeded":
		return CTMemberFailed
	case m.Unschedulable != "":
		return CTMemberUnschedulable
	case m.PodPhase == "Running" && m.PodReady:
		return CTMemberReady
	default:
		return CTMemberStarting
	}
}

// ctCluster is everything the CT pool code needs from Kubernetes. The
// production implementation is kubeCTCluster; tests use an in-memory fake.
type ctCluster interface {
	GetPool(namespace, name string) (CTPoolSpec, CTPoolStatus, bool, error)
	ListPools(namespace string) ([]CTPoolSpec, []CTPoolStatus, error)
	PutPoolSpec(spec CTPoolSpec) error
	PutPoolStatus(namespace, name string, status CTPoolStatus) error
	DeletePool(namespace, name string) error

	ListMembers(namespace, pool string) ([]ctMemberObs, error)
	CreateMember(spec CTPoolSpec, member string) error
	StartMember(namespace, member string) error
	DeleteMember(namespace, member string) error
	SetAssignee(namespace, member, identity string) error

	ListClaims(namespace, pool string) (map[string]lease, error)
	GetClaim(ref leaseRef) (lease, bool, error)
	CreateClaim(ref leaseRef, holder string, duration time.Duration) (bool, error)
	ReplaceClaim(ref leaseRef, holder string, duration time.Duration, resourceVersion string) bool
	DeleteClaim(ref leaseRef, resourceVersion string) error
}

var ctc ctCluster = kubeCTCluster{}

// ApplyCTPool validates spec, defaulting an empty mode to ephemeral, and
// stores it as the pool's cluster-visible definition. It does not touch
// members; run ReconcileCTPool afterwards.
func ApplyCTPool(spec CTPoolSpec) (CTPoolSpec, error) {
	if spec.Mode == "" {
		spec.Mode = CTPoolModeEphemeral
	}
	if err := spec.Validate(); err != nil {
		return spec, err
	}
	if err := ctc.PutPoolSpec(spec); err != nil {
		return spec, fmt.Errorf("store CT pool %q: %w", spec.Name, err)
	}
	return spec, nil
}

// GetCTPool returns a pool's definition and the status the last reconcile
// recorded.
func GetCTPool(namespace, name string) (CTPoolSpec, CTPoolStatus, error) {
	spec, status, found, err := ctc.GetPool(namespace, name)
	if err != nil {
		return spec, status, err
	}
	if !found {
		return spec, status, fmt.Errorf("CT pool %q not found in ns/%s", name, namespace)
	}
	return spec, status, nil
}

// ListCTPools returns every CT pool definition in namespace ("" = all)
// with its recorded status.
func ListCTPools(namespace string) ([]CTPoolSpec, []CTPoolStatus, error) {
	return ctc.ListPools(namespace)
}

// ObserveCTPool computes the live status of a pool without changing it.
func ObserveCTPool(namespace, name string) (CTPoolStatus, error) {
	spec, _, err := GetCTPool(namespace, name)
	if err != nil {
		return CTPoolStatus{}, err
	}
	members, claims, err := observeCTPool(spec)
	if err != nil {
		return CTPoolStatus{}, err
	}
	return ctPoolStatus(spec, members, claims, ctNow()), nil
}

func observeCTPool(spec CTPoolSpec) ([]ctMemberObs, map[string]lease, error) {
	members, err := ctc.ListMembers(spec.Namespace, spec.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("list members of CT pool %q: %w", spec.Name, err)
	}
	claims, err := ctc.ListClaims(spec.Namespace, spec.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("list claims of CT pool %q: %w", spec.Name, err)
	}
	return members, claims, nil
}

// ctPoolStatus derives the pool status from one observation.
func ctPoolStatus(spec CTPoolSpec, members []ctMemberObs, claims map[string]lease, now time.Time) CTPoolStatus {
	hash := spec.Source.Hash()
	status := CTPoolStatus{Pool: spec.Name, Namespace: spec.Namespace, Desired: spec.Size, SourceHash: hash, ObservedAt: now}
	markClaim := func(row *CTMemberStatus) {
		claim, ok := claims[row.Name]
		if !ok {
			return
		}
		expired := leaseExpired(claim, now)
		if claim.Spec.HolderIdentity == CTReconcilerHolder {
			row.Maintenance = !expired
			return
		}
		row.ClaimedBy, row.ClaimExpired = claim.Spec.HolderIdentity, expired
		status.Claimed++
	}
	seen := map[string]bool{}
	for _, m := range members {
		seen[m.Name] = true
		idx := ctMemberIndex(spec.Name, m.Name)
		row := CTMemberStatus{Name: m.Name, Index: idx, State: m.state(), Drifted: m.SourceHash != hash, Excess: idx == 0 || idx > spec.Size}
		if row.State == CTMemberUnschedulable {
			row.Message = m.Unschedulable
			status.Unschedulable++
		}
		markClaim(&row)
		status.Observed++
		if row.State == CTMemberReady {
			status.Ready++
		}
		if ctClaimable(row) {
			status.Free++
		}
		status.Members = append(status.Members, row)
	}
	for i := 1; i <= spec.Size; i++ {
		if name := CTMemberName(spec.Name, i); !seen[name] {
			row := CTMemberStatus{Name: name, Index: i, State: CTMemberMissing}
			markClaim(&row)
			status.Members = append(status.Members, row)
		}
	}
	sort.Slice(status.Members, func(a, b int) bool {
		ma, mb := status.Members[a], status.Members[b]
		if ma.Index != mb.Index {
			return ma.Index != 0 && (mb.Index == 0 || ma.Index < mb.Index)
		}
		return ma.Name < mb.Name
	})
	status.Exhausted = spec.Size > 0 && status.Free == 0
	return status
}

// ctClaimable reports whether a member may be handed to a new user: it is
// a current, unclaimed member built from the current source that the
// scheduler can place. An unschedulable member is capacity the cluster
// does not have, so it does not count as free.
func ctClaimable(m CTMemberStatus) bool {
	if m.Excess || m.Drifted || m.Maintenance || m.ClaimedBy != "" {
		return false
	}
	switch m.State {
	case CTMemberReady, CTMemberStarting, CTMemberStopped:
		return true
	}
	return false
}

// ReconcileCTPool drives a pool towards its definition: it creates
// missing members, starts stopped ones, replaces drifted or failed ones,
// and deletes members outside 1..size. It never destroys a member that a
// user has claimed, even if the claim has expired. Every destructive step
// runs under the member's Lease, so it is safe to run repeatedly and
// concurrently. The resulting status is recorded in the pool ConfigMap and
// returned; an error means some step failed and a later run will retry it.
func ReconcileCTPool(namespace, name string) (CTPoolStatus, error) {
	spec, _, err := GetCTPool(namespace, name)
	if err != nil {
		return CTPoolStatus{Pool: name, Namespace: namespace, ObservedAt: ctNow()}, err
	}
	return reconcileCTPool(spec)
}

func reconcileCTPool(spec CTPoolSpec) (CTPoolStatus, error) {
	fail := func(err error) (CTPoolStatus, error) {
		status := CTPoolStatus{Pool: spec.Name, Namespace: spec.Namespace, Desired: spec.Size, ObservedAt: ctNow(), Errors: []string{err.Error()}}
		if recErr := ctc.PutPoolStatus(spec.Namespace, spec.Name, status); recErr != nil {
			err = fmt.Errorf("%w (recording status also failed: %v)", err, recErr)
		}
		return status, err
	}
	if err := spec.Validate(); err != nil {
		return fail(err)
	}
	members, claims, err := observeCTPool(spec)
	if err != nil {
		return fail(err)
	}

	now := ctNow()
	hash := spec.Source.Hash()
	var errs []string
	note := func(member, step string, err error) {
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s: %v", member, step, err))
		}
	}
	exists := map[string]bool{}
	sort.Slice(members, func(a, b int) bool { return members[a].Name < members[b].Name })
	for _, m := range members {
		exists[m.Name] = true
		claim, claimed := claims[m.Name]
		if m.Terminating || ctUnderMaintenance(claim, claimed, now) {
			continue
		}
		userHeld := claimed && claim.Spec.HolderIdentity != CTReconcilerHolder
		idx := ctMemberIndex(spec.Name, m.Name)
		state := m.state()
		switch {
		case userHeld:
			// A claimed member is never destroyed, not even when it is
			// excess, drifted, failed, or its claim expired. Starting it
			// is safe.
			if state == CTMemberStopped {
				note(m.Name, "start", ctc.StartMember(spec.Namespace, m.Name))
			}
		case idx == 0 || idx > spec.Size:
			note(m.Name, "scale down", ctMaintain(spec, m.Name, claim, claimed, func() error {
				return ctc.DeleteMember(spec.Namespace, m.Name)
			}))
		case m.SourceHash != hash || state == CTMemberFailed:
			note(m.Name, "replace", ctMaintain(spec, m.Name, claim, claimed, func() error {
				if err := ctc.DeleteMember(spec.Namespace, m.Name); err != nil {
					return err
				}
				return ctc.CreateMember(spec, m.Name)
			}))
		case state == CTMemberStopped:
			note(m.Name, "start", ctc.StartMember(spec.Namespace, m.Name))
		}
	}
	for i := 1; i <= spec.Size; i++ {
		name := CTMemberName(spec.Name, i)
		claim, claimed := claims[name]
		switch {
		case exists[name] || ctUnderMaintenance(claim, claimed, now):
			continue
		case claimed && claim.Spec.HolderIdentity != CTReconcilerHolder:
			// The claimed member vanished (deleted by hand). Recreating it
			// destroys nothing, and the holder keeps the claim.
			note(name, "create", ctc.CreateMember(spec, name))
		default:
			note(name, "create", ctMaintain(spec, name, claim, claimed, func() error {
				return ctc.CreateMember(spec, name)
			}))
		}
	}

	members, claims, err = observeCTPool(spec)
	if err != nil {
		errs = append(errs, err.Error())
		return fail(errors.New(strings.Join(errs, "; ")))
	}
	status := ctPoolStatus(spec, members, claims, ctNow())
	status.Errors = errs
	if err := ctc.PutPoolStatus(spec.Namespace, spec.Name, status); err != nil {
		status.Errors = append(status.Errors, fmt.Sprintf("record status: %v", err))
	}
	if len(status.Errors) > 0 {
		return status, fmt.Errorf("CT pool %q reconciliation incomplete: %s", spec.Name, strings.Join(status.Errors, "; "))
	}
	return status, nil
}

// ctUnderMaintenance reports a live reconciler Lease: another reconcile or
// release is working on the member right now.
func ctUnderMaintenance(claim lease, claimed bool, now time.Time) bool {
	return claimed && claim.Spec.HolderIdentity == CTReconcilerHolder && !leaseExpired(claim, now)
}

// ctMaintain runs fn while holding the member's Lease as the reconciler.
// It takes a free Lease or an expired reconciler Lease, never a user's.
// When the Lease is contended it skips fn: the member is claimed or under
// maintenance elsewhere, and the next reconcile looks again.
func ctMaintain(spec CTPoolSpec, member string, claim lease, claimed bool, fn func() error) error {
	ref := ctLeaseRef(spec.Namespace, spec.Name, member)
	switch {
	case !claimed:
		created, err := ctc.CreateClaim(ref, CTReconcilerHolder, ctMaintenanceDuration)
		if err != nil || !created {
			return err
		}
	case claim.Spec.HolderIdentity == CTReconcilerHolder && leaseExpired(claim, ctNow()):
		if !ctc.ReplaceClaim(ref, CTReconcilerHolder, ctMaintenanceDuration, claim.Metadata.ResourceVersion) {
			return nil
		}
	default:
		return nil
	}
	return errors.Join(fn(), ctEndMaintenance(ref))
}

// ctEndMaintenance deletes the reconciler's Lease, preconditioned on the
// version it reads, so it never removes a Lease someone else took over.
func ctEndMaintenance(ref leaseRef) error {
	current, found, err := ctc.GetClaim(ref)
	if err != nil || !found || current.Spec.HolderIdentity != CTReconcilerHolder {
		return err
	}
	return ctc.DeleteClaim(ref, current.Metadata.ResourceVersion)
}

// ClaimCTMember atomically gives identity one free member of an ephemeral
// CT pool and starts it if it is stopped. A second claim by the same
// identity returns the member it already holds (renewing an expired claim).
func ClaimCTMember(namespace, pool, identity string) (string, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return "", fmt.Errorf("claim identity must not be empty")
	}
	if identity == CTReconcilerHolder {
		return "", fmt.Errorf("claim identity %q is reserved for the reconciler", identity)
	}
	spec, _, err := GetCTPool(namespace, pool)
	if err != nil {
		return "", err
	}
	if err := spec.Validate(); err != nil {
		return "", err
	}
	members, claims, err := observeCTPool(spec)
	if err != nil {
		return "", err
	}
	now := ctNow()
	status := ctPoolStatus(spec, members, claims, now)
	byName := map[string]ctMemberObs{}
	for _, m := range members {
		byName[m.Name] = m
	}

	for _, row := range status.Members {
		if row.ClaimedBy != identity || row.State == CTMemberMissing {
			continue
		}
		if row.ClaimExpired {
			claim := claims[row.Name]
			if !ctc.ReplaceClaim(ctLeaseRef(namespace, pool, row.Name), identity, claimDuration, claim.Metadata.ResourceVersion) {
				continue
			}
		}
		return row.Name, ctStartIfStopped(namespace, row.Name, row.State)
	}

	var candidates []CTMemberStatus
	for _, row := range status.Members {
		if ctClaimable(row) {
			candidates = append(candidates, row)
		}
	}
	rank := map[CTMemberState]int{CTMemberReady: 0, CTMemberStarting: 1, CTMemberStopped: 2}
	sort.SliceStable(candidates, func(a, b int) bool { return rank[candidates[a].State] < rank[candidates[b].State] })
	for _, row := range candidates {
		ref := ctLeaseRef(namespace, pool, row.Name)
		created, err := ctc.CreateClaim(ref, identity, claimDuration)
		if err != nil {
			return "", err
		}
		if !created {
			continue
		}
		if err := ctc.SetAssignee(namespace, row.Name, identity); err != nil {
			if undo := ctReleaseOwnClaim(ref, identity); undo != nil {
				err = errors.Join(err, undo)
			}
			return "", fmt.Errorf("claimed %s but could not record the assignment; claim withdrawn: %w", row.Name, err)
		}
		return row.Name, ctStartIfStopped(namespace, row.Name, byName[row.Name].state())
	}
	return "", fmt.Errorf("%w: pool %q in ns/%s (%d desired, %d claimed, %d unschedulable)",
		ErrCTPoolExhausted, pool, namespace, status.Desired, status.Claimed, status.Unschedulable)
}

func ctStartIfStopped(namespace, member string, state CTMemberState) error {
	if state != CTMemberStopped {
		return nil
	}
	if err := ctc.StartMember(namespace, member); err != nil {
		return fmt.Errorf("claimed %s but failed to start it: %w", member, err)
	}
	return nil
}

func ctReleaseOwnClaim(ref leaseRef, holder string) error {
	current, found, err := ctc.GetClaim(ref)
	if err != nil || !found || current.Spec.HolderIdentity != holder {
		return err
	}
	return ctc.DeleteClaim(ref, current.Metadata.ResourceVersion)
}

// ReleaseCTMember ends identity's claim on member, destroys the member, and
// recreates it from the pool's current source (unless the pool has shrunk
// below it). The claim is first swapped, by compare-and-swap, to the
// reconciler, so nobody can claim the member while it is recycled.
// Releasing a member nobody claims is a no-op. force releases any claim
// (administrative reclaim) and recycles an unclaimed member.
func ReleaseCTMember(namespace, pool, member, identity string, force bool) error {
	identity = strings.TrimSpace(identity)
	if identity == "" && !force {
		return fmt.Errorf("release identity must not be empty (use force for an administrative release)")
	}
	spec, _, found, err := ctc.GetPool(namespace, pool)
	if err != nil {
		return err
	}
	if found {
		if err := spec.Validate(); err != nil {
			return err
		}
	}
	members, err := ctc.ListMembers(namespace, pool)
	if err != nil {
		return fmt.Errorf("list members of CT pool %q: %w", pool, err)
	}
	isMember := false
	for _, m := range members {
		isMember = isMember || m.Name == member
	}
	ref := ctLeaseRef(namespace, pool, member)
	claim, claimed, err := ctc.GetClaim(ref)
	if err != nil {
		return fmt.Errorf("read claim for %s: %w", member, err)
	}
	now := ctNow()
	switch {
	case !claimed && !force:
		return nil
	case !claimed:
		created, err := ctc.CreateClaim(ref, CTReconcilerHolder, ctMaintenanceDuration)
		if err != nil {
			return err
		}
		if !created {
			return fmt.Errorf("member %s was claimed concurrently; not released", member)
		}
	case ctUnderMaintenance(claim, claimed, now):
		return nil
	case claim.Spec.HolderIdentity != identity && claim.Spec.HolderIdentity != CTReconcilerHolder && !force:
		return fmt.Errorf("member %s is claimed by %q, not %q", member, claim.Spec.HolderIdentity, identity)
	default:
		if !ctc.ReplaceClaim(ref, CTReconcilerHolder, ctMaintenanceDuration, claim.Metadata.ResourceVersion) {
			return fmt.Errorf("claim on %s changed concurrently; not released", member)
		}
	}

	recycle := func() error {
		// Only a labeled member of this pool is destroyed. A CT that merely
		// shares the name is left alone, and CreateMember refuses to
		// overwrite it.
		if isMember {
			if err := ctc.DeleteMember(namespace, member); err != nil {
				return fmt.Errorf("destroy %s: %w", member, err)
			}
		}
		if !found {
			return nil
		}
		if idx := ctMemberIndex(pool, member); idx < 1 || idx > spec.Size {
			return nil
		}
		if err := ctc.CreateMember(spec, member); err != nil {
			return fmt.Errorf("recreate %s from %s (the next reconcile retries): %w", member, spec.Source.Image, err)
		}
		return nil
	}
	return errors.Join(recycle(), ctEndMaintenance(ref))
}

// DeleteCTPool removes a pool: it shrinks the definition to size 0 so no
// reconcile recreates members, deletes every member under its Lease, and
// then deletes the definition. Any user claim, live or expired, blocks the
// delete unless force is set; force destroys claimed members too.
func DeleteCTPool(namespace, name string, force bool) error {
	spec, _, err := GetCTPool(namespace, name)
	if err != nil {
		return err
	}
	members, claims, err := observeCTPool(spec)
	if err != nil {
		return err
	}
	var held []string
	for member, claim := range claims {
		if claim.Spec.HolderIdentity != CTReconcilerHolder {
			held = append(held, member+" ("+claim.Spec.HolderIdentity+")")
		}
	}
	if len(held) > 0 && !force {
		sort.Strings(held)
		return fmt.Errorf("CT pool %q has claimed members: %s; release them or delete with force", name, strings.Join(held, ", "))
	}
	spec.Size = 0
	if err := ctc.PutPoolSpec(spec); err != nil {
		return fmt.Errorf("drain CT pool %q: %w", name, err)
	}
	var errs []error
	for _, m := range members {
		claim, claimed := claims[m.Name]
		if !force {
			if err := ctMaintain(spec, m.Name, claim, claimed, func() error { return ctc.DeleteMember(namespace, m.Name) }); err != nil {
				errs = append(errs, fmt.Errorf("delete %s: %w", m.Name, err))
			}
			continue
		}
		if err := ctc.DeleteMember(namespace, m.Name); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", m.Name, err))
			continue
		}
		if claimed {
			if err := ctc.DeleteClaim(ctLeaseRef(namespace, name, m.Name), claim.Metadata.ResourceVersion); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) == 0 {
		remaining, err := ctc.ListMembers(namespace, name)
		if err != nil {
			errs = append(errs, err)
		}
		for _, m := range remaining {
			errs = append(errs, fmt.Errorf("%s is still present (claimed or busy)", m.Name))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("CT pool %q drained to size 0 but not deleted: %w", name, errors.Join(errs...))
	}
	return ctc.DeletePool(namespace, name)
}
