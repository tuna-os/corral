package vdi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// The self-service broker (#227, ADR-0013). It turns a trusted caller
// identity into claim, start, inspect, and release operations on desktop
// pool members. The broker holds no ownership state of its own: every
// decision reads the member's Lease (#226), and every transition goes
// through the claim primitives of VM pools (Claim, Release) and CT pools
// (ClaimCTMember, ReleaseCTMember). The HTTP surface lives in pkg/web.

// PoolKind says what backs a pool's members.
type PoolKind string

const (
	PoolKindVM PoolKind = "vm" // KubeVirt VMs cloned from a golden VM
	PoolKindCT PoolKind = "ct" // Corral CTs created from an image (#230)
)

// Pool modes. A persistent member keeps its disks across claims; an
// ephemeral member is recreated from its source after each claim.
const (
	PoolModePersistent = "persistent"
	PoolModeEphemeral  = "ephemeral"
)

// PoolRef names a pool.
type PoolRef struct {
	Kind      PoolKind `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
}

// ID is the stable pool identifier: kind/namespace/name.
func (p PoolRef) ID() string { return string(p.Kind) + "/" + p.Namespace + "/" + p.Name }

// MemberRef names one pool member.
type MemberRef struct {
	Kind      PoolKind `json:"kind"`
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
}

// ID is the stable member identifier: kind/namespace/member. It survives
// claims and releases; SessionID tells claims of one member apart.
func (m MemberRef) ID() string { return string(m.Kind) + "/" + m.Namespace + "/" + m.Name }

// PoolStatus is what a user sees when choosing a pool.
type PoolStatus struct {
	ID string `json:"id"`
	PoolRef
	Mode    string `json:"mode"`
	Members int    `json:"members"`
	Claimed int    `json:"claimed"`
	Free    int    `json:"free"`
}

// MemberClaim is the cluster-visible claim on a member: its Lease.
type MemberClaim struct {
	Member     MemberRef
	Pool       string
	Holder     string
	AcquiredAt time.Time
	RenewedAt  time.Time
	Duration   time.Duration
	// Version is the Lease resourceVersion.
	Version string
}

// SessionID identifies one claim of a member. A new claim, or a takeover
// of an expired one, gets a new acquisition time and so a new ID.
func (c MemberClaim) SessionID() string {
	sum := sha256.Sum256([]byte(c.Member.ID() + "\x00" + c.Holder + "\x00" + c.AcquiredAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:8])
}

// Expired reports a claim whose Lease another identity may take over.
func (c MemberClaim) Expired(now time.Time) bool {
	return c.Holder == "" || c.Duration <= 0 || now.After(c.RenewedAt.Add(c.Duration))
}

// MemberState is the observed lifecycle state of a member.
type MemberState struct {
	Phase   string `json:"phase"`
	Running bool   `json:"running"`
}

// Cluster is everything the broker needs from Kubernetes. KubeCluster is
// the production implementation; vditest.Cluster is an in-memory fake.
type Cluster interface {
	// Pools lists the VM and CT pools in namespace.
	Pools(namespace string) ([]PoolStatus, error)
	// Claims lists the user claims on pool members in namespace.
	Claims(namespace string) ([]MemberClaim, error)
	// GetClaim reads one member's claim; found is false when it has none.
	GetClaim(ref MemberRef) (claim MemberClaim, found bool, err error)
	// ClaimMember atomically claims a member of pool for identity and
	// starts it. An identity that already holds a member gets it back.
	// It fails with ErrPoolNotFound or ErrPoolExhausted; a failed start
	// returns the member with a *StartError and keeps the claim.
	ClaimMember(pool PoolRef, identity string) (member string, err error)
	// MemberState reports the lifecycle state of a claimed member.
	MemberState(c MemberClaim) (MemberState, error)
	// StartMember starts a claimed member.
	StartMember(c MemberClaim) error
	// ReleaseMember ends c, provided c.Holder still holds it. A persistent
	// member stops and keeps its disks; an ephemeral one is recreated. On
	// a lifecycle failure the claim is kept, so the release can be retried.
	ReleaseMember(c MemberClaim) error
}

// Broker errors. pkg/web maps them to HTTP status codes.
var (
	ErrUnauthenticated = errors.New("VDI self-service needs an authenticated identity")
	ErrForbidden       = errors.New("the desktop belongs to another user")
	ErrNotFound        = errors.New("no claimed desktop by that ID")
)

// Actor is the caller of a broker operation. Identity comes from the
// trusted identity contract (ADR-0003, ADR-0006), never from the request
// body. An admin may inspect, start, and release any user's desktop.
type Actor struct {
	Identity string
	Admin    bool
}

// AuditEvent records one broker decision.
type AuditEvent struct {
	Action string // claim, start, release, force-release
	Actor  string
	Target string // a pool or member ID
	Holder string // the claim holder, when it differs from Actor
	Err    error
}

// BrokerConfig configures NewBroker.
type BrokerConfig struct {
	// Namespaces holds the pools the broker serves. Members elsewhere are
	// invisible to it, whatever their labels say.
	Namespaces []string
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Audit receives every claim, start, and release (optional).
	Audit func(AuditEvent)
}

// Desktop is a claimed member as the API reports it.
type Desktop struct {
	ID           string    `json:"id"`
	SessionID    string    `json:"sessionId"`
	PoolID       string    `json:"poolId"`
	Kind         PoolKind  `json:"kind"`
	Namespace    string    `json:"namespace"`
	Pool         string    `json:"pool"`
	Member       string    `json:"member"`
	Owner        string    `json:"owner"`
	ClaimedAt    time.Time `json:"claimedAt"`
	ClaimExpired bool      `json:"claimExpired,omitempty"`
	Phase        string    `json:"phase"`
	Running      bool      `json:"running"`
	StartError   string    `json:"startError,omitempty"`
}

// Broker is the self-service layer over a Cluster.
type Broker struct {
	cluster    Cluster
	namespaces []string
	now        func() time.Time
	audit      func(AuditEvent)
}

// NewBroker returns a broker that serves the pools in cfg.Namespaces.
func NewBroker(c Cluster, cfg BrokerConfig) *Broker {
	b := &Broker{cluster: c, now: cfg.Now, audit: cfg.Audit}
	for _, ns := range cfg.Namespaces {
		if ns = strings.TrimSpace(ns); ns != "" && !slices.Contains(b.namespaces, ns) {
			b.namespaces = append(b.namespaces, ns)
		}
	}
	if b.now == nil {
		b.now = time.Now
	}
	if b.audit == nil {
		b.audit = func(AuditEvent) {}
	}
	return b
}

// Namespaces returns the namespaces the broker serves.
func (b *Broker) Namespaces() []string { return slices.Clone(b.namespaces) }

func (b *Broker) serves(ns string) bool { return slices.Contains(b.namespaces, ns) }

func validKind(k PoolKind) bool { return k == PoolKindVM || k == PoolKindCT }

func checkActor(a Actor) error {
	switch id := strings.TrimSpace(a.Identity); {
	case id == "":
		return ErrUnauthenticated
	case id == CTReconcilerHolder:
		return fmt.Errorf("%w: identity %q is reserved", ErrForbidden, id)
	}
	return nil
}

// Pools lists every pool the broker serves.
func (b *Broker) Pools() ([]PoolStatus, error) {
	var out []PoolStatus
	for _, ns := range b.namespaces {
		pools, err := b.cluster.Pools(ns)
		if err != nil {
			return nil, fmt.Errorf("list pools in ns/%s: %w", ns, err)
		}
		out = append(out, pools...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Claim gives the actor a desktop from pool, starting it when needed. A
// retry returns the desktop the actor already holds. A failed start keeps
// the claim and reports it in Desktop.StartError.
func (b *Broker) Claim(a Actor, pool PoolRef) (Desktop, error) {
	if err := checkActor(a); err != nil {
		return Desktop{}, err
	}
	if !validKind(pool.Kind) || !b.serves(pool.Namespace) || pool.Name == "" {
		return Desktop{}, fmt.Errorf("%w: %s", ErrPoolNotFound, pool.ID())
	}
	member, err := b.cluster.ClaimMember(pool, a.Identity)
	var startErr *StartError
	if err != nil && !errors.As(err, &startErr) {
		b.audit(AuditEvent{Action: "claim", Actor: a.Identity, Target: pool.ID(), Err: err})
		return Desktop{}, err
	}
	ref := MemberRef{Kind: pool.Kind, Namespace: pool.Namespace, Name: member}
	b.audit(AuditEvent{Action: "claim", Actor: a.Identity, Target: ref.ID(), Err: err})
	c, found, getErr := b.cluster.GetClaim(ref)
	if getErr != nil || !found || c.Holder != a.Identity {
		// The claim succeeded; report it even if it cannot be read back.
		now := b.now().UTC()
		c = MemberClaim{Member: ref, Pool: pool.Name, Holder: a.Identity, AcquiredAt: now, RenewedAt: now, Duration: claimDuration}
	}
	d := b.desktop(c)
	if startErr != nil {
		d.StartError = startErr.Error()
	}
	return d, nil
}

// Desktops lists the actor's desktops; all lists every user's (admin only).
func (b *Broker) Desktops(a Actor, all bool) ([]Desktop, error) {
	if err := checkActor(a); err != nil {
		return nil, err
	}
	if all && !a.Admin {
		return nil, fmt.Errorf("%w: listing every desktop needs an admin", ErrForbidden)
	}
	out := []Desktop{}
	for _, ns := range b.namespaces {
		claims, err := b.cluster.Claims(ns)
		if err != nil {
			return nil, fmt.Errorf("list claims in ns/%s: %w", ns, err)
		}
		for _, c := range claims {
			if all || c.Holder == a.Identity {
				out = append(out, b.desktop(c))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// authorize returns ref's claim if the actor may act on it: the actor
// holds it, or is an admin.
func (b *Broker) authorize(a Actor, ref MemberRef) (MemberClaim, error) {
	if err := checkActor(a); err != nil {
		return MemberClaim{}, err
	}
	if !validKind(ref.Kind) || !b.serves(ref.Namespace) || ref.Name == "" {
		return MemberClaim{}, fmt.Errorf("%w: %s", ErrNotFound, ref.ID())
	}
	c, found, err := b.cluster.GetClaim(ref)
	if err != nil {
		return MemberClaim{}, fmt.Errorf("read claim of %s: %w", ref.ID(), err)
	}
	if !found || c.Holder == "" || c.Holder == CTReconcilerHolder {
		return MemberClaim{}, fmt.Errorf("%w: %s", ErrNotFound, ref.ID())
	}
	if c.Holder != a.Identity && !a.Admin {
		return MemberClaim{}, fmt.Errorf("%w: %s", ErrForbidden, ref.ID())
	}
	return c, nil
}

// Desktop inspects one desktop.
func (b *Broker) Desktop(a Actor, ref MemberRef) (Desktop, error) {
	c, err := b.authorize(a, ref)
	if err != nil {
		return Desktop{}, err
	}
	return b.desktop(c), nil
}

// Start starts a claimed desktop, e.g. after a failed start at claim time.
// A running desktop is left alone.
func (b *Broker) Start(a Actor, ref MemberRef) (Desktop, error) {
	c, err := b.authorize(a, ref)
	if err != nil {
		return Desktop{}, err
	}
	if state, err := b.cluster.MemberState(c); err == nil && state.Running {
		return b.desktop(c), nil
	}
	err = b.cluster.StartMember(c)
	b.audit(AuditEvent{Action: "start", Actor: a.Identity, Target: ref.ID(), Holder: c.Holder, Err: err})
	d := b.desktop(c)
	if err != nil {
		d.StartError = err.Error()
	}
	return d, nil
}

// BeginRelease authorizes the actor to release ref and returns the release
// itself, which may run for minutes (an ephemeral member is recreated).
// Callers run it in the background. The holder keeps the desktop if the
// release fails, so it can be retried.
func (b *Broker) BeginRelease(a Actor, ref MemberRef) (func() error, error) {
	c, err := b.authorize(a, ref)
	if err != nil {
		return nil, err
	}
	action, holder := "release", ""
	if c.Holder != a.Identity {
		action, holder = "force-release", c.Holder
	}
	return func() error {
		err := b.cluster.ReleaseMember(c)
		b.audit(AuditEvent{Action: action, Actor: a.Identity, Target: ref.ID(), Holder: holder, Err: err})
		return err
	}, nil
}

func (b *Broker) desktop(c MemberClaim) Desktop {
	d := Desktop{
		ID: c.Member.ID(), SessionID: c.SessionID(),
		PoolID: PoolRef{Kind: c.Member.Kind, Namespace: c.Member.Namespace, Name: c.Pool}.ID(),
		Kind:   c.Member.Kind, Namespace: c.Member.Namespace, Pool: c.Pool, Member: c.Member.Name,
		Owner: c.Holder, ClaimedAt: c.AcquiredAt, ClaimExpired: c.Expired(b.now()),
	}
	state, err := b.cluster.MemberState(c)
	if err != nil {
		d.Phase = "Unknown"
		return d
	}
	d.Phase, d.Running = state.Phase, state.Running
	return d
}
