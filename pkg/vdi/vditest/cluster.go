// Package vditest provides an in-memory vdi.Cluster for tests of the VDI
// broker and its HTTP surface. Claims follow the Lease rules the real
// cluster enforces: one holder per member, a re-claim by the holder returns
// the same member, and a release must name the current holder and version.
package vditest

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/vdi"
)

type member struct {
	ref        vdi.MemberRef
	pool       string
	running    bool
	noVNC      bool
	noTerminal bool
}

type pool struct {
	ref     vdi.PoolRef
	mode    string
	members []string // member IDs, in claim order
}

// Cluster is an in-memory vdi.Cluster. The zero value is not usable; call
// New.
type Cluster struct {
	mu      sync.Mutex
	now     func() time.Time
	pools   map[string]*pool
	members map[string]*member
	claims  map[string]vdi.MemberClaim
	rv      int

	startFailures   map[string]int
	releaseFailures map[string]int
	updateFailures  int
	updates         int
	starts          map[string]int
	stops           map[string]int
	recreates       map[string]int
}

// New returns an empty cluster that stamps claims with now.
func New(now func() time.Time) *Cluster {
	return &Cluster{
		now: now, pools: map[string]*pool{}, members: map[string]*member{}, claims: map[string]vdi.MemberClaim{},
		startFailures: map[string]int{}, releaseFailures: map[string]int{},
		starts: map[string]int{}, stops: map[string]int{}, recreates: map[string]int{},
	}
}

// AddPool defines a pool with stopped members named <pool>-1..<pool>-size.
func (c *Cluster) AddPool(ref vdi.PoolRef, mode string, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := &pool{ref: ref, mode: mode}
	for i := 1; i <= size; i++ {
		m := &member{ref: vdi.MemberRef{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name + "-" + strconv.Itoa(i)}, pool: ref.Name}
		c.members[m.ref.ID()] = m
		p.members = append(p.members, m.ref.ID())
	}
	c.pools[ref.ID()] = p
}

// FailStarts makes the next n starts of member fail.
func (c *Cluster) FailStarts(ref vdi.MemberRef, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startFailures[ref.ID()] = n
}

// FailReleases makes the next n releases of member fail.
func (c *Cluster) FailReleases(ref vdi.MemberRef, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releaseFailures[ref.ID()] = n
}

// Holder returns the identity that holds member ("" when free).
func (c *Cluster) Holder(ref vdi.MemberRef) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.claims[ref.ID()].Holder
}

// Running reports whether member runs.
func (c *Cluster) Running(ref vdi.MemberRef) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.members[ref.ID()]
	return ok && m.running
}

// Counts returns how often member was started, stopped, and recreated.
func (c *Cluster) Counts(ref vdi.MemberRef) (starts, stops, recreates int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.starts[ref.ID()], c.stops[ref.ID()], c.recreates[ref.ID()]
}

// SetClaim installs a claim directly, e.g. to model state left by an
// earlier broker process.
func (c *Cluster) SetClaim(claim vdi.MemberClaim) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rv++
	claim.Version = strconv.Itoa(c.rv)
	c.claims[claim.Member.ID()] = claim
}

// Pools implements vdi.Cluster.
func (c *Cluster) Pools(namespace string) ([]vdi.PoolStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []vdi.PoolStatus
	for _, p := range c.pools {
		if p.ref.Namespace != namespace {
			continue
		}
		s := vdi.PoolStatus{ID: p.ref.ID(), PoolRef: p.ref, Mode: p.mode, Members: len(p.members)}
		for _, id := range p.members {
			if _, held := c.claims[id]; held {
				s.Claimed++
			} else {
				s.Free++
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Claims implements vdi.Cluster.
func (c *Cluster) Claims(namespace string) ([]vdi.MemberClaim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []vdi.MemberClaim
	for _, cl := range c.claims {
		if cl.Member.Namespace == namespace {
			out = append(out, cl)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Member.ID() < out[j].Member.ID() })
	return out, nil
}

// GetClaim implements vdi.Cluster.
func (c *Cluster) GetClaim(ref vdi.MemberRef) (vdi.MemberClaim, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl, ok := c.claims[ref.ID()]
	return cl, ok, nil
}

// ClaimMember implements vdi.Cluster.
func (c *Cluster) ClaimMember(ref vdi.PoolRef, identity string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pools[ref.ID()]
	if !ok {
		return "", fmt.Errorf("%w: %s", vdi.ErrPoolNotFound, ref.ID())
	}
	chosen := ""
	for _, id := range p.members {
		if c.claims[id].Holder == identity {
			chosen = id
			break
		}
	}
	if chosen == "" {
		for _, id := range p.members {
			if _, held := c.claims[id]; !held {
				chosen = id
				break
			}
		}
	}
	if chosen == "" {
		return "", fmt.Errorf("%w: %s", vdi.ErrPoolExhausted, ref.ID())
	}
	m := c.members[chosen]
	if _, held := c.claims[chosen]; !held {
		now := c.now().UTC()
		c.rv++
		c.claims[chosen] = vdi.MemberClaim{Member: m.ref, Pool: m.pool, Holder: identity, AcquiredAt: now, RenewedAt: now, Duration: time.Hour, Version: strconv.Itoa(c.rv)}
	}
	if err := c.startLocked(m); err != nil {
		return m.ref.Name, &vdi.StartError{Member: m.ref.Name, Err: err}
	}
	return m.ref.Name, nil
}

func (c *Cluster) startLocked(m *member) error {
	if m.running {
		return nil
	}
	if c.startFailures[m.ref.ID()] > 0 {
		c.startFailures[m.ref.ID()]--
		return fmt.Errorf("insufficient memory on every node")
	}
	c.starts[m.ref.ID()]++
	m.running = true
	return nil
}

// MemberState implements vdi.Cluster.
func (c *Cluster) MemberState(cl vdi.MemberClaim) (vdi.MemberState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.members[cl.Member.ID()]
	if !ok {
		return vdi.MemberState{Phase: "Missing"}, nil
	}
	s := vdi.MemberState{Phase: "Stopped", VNC: !m.noVNC && m.ref.Kind == vdi.PoolKindVM, Terminal: !m.noTerminal}
	if m.running {
		s.Phase, s.Running = "Running", true
	}
	return s, nil
}

// SetConsoles says which consoles member offers (all, by default; a CT
// member never offers VNC).
func (c *Cluster) SetConsoles(ref vdi.MemberRef, vnc, terminal bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.members[ref.ID()]; ok {
		m.noVNC, m.noTerminal = !vnc, !terminal
	}
}

// SetRunning starts or stops member out of band, e.g. a guest shutdown.
func (c *Cluster) SetRunning(ref vdi.MemberRef, running bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.members[ref.ID()]; ok {
		m.running = running
	}
}

// StartMember implements vdi.Cluster.
func (c *Cluster) StartMember(cl vdi.MemberClaim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.members[cl.Member.ID()]
	if !ok {
		return fmt.Errorf("no member %s", cl.Member.ID())
	}
	return c.startLocked(m)
}

// UpdateClaim implements vdi.Cluster.
func (c *Cluster) UpdateClaim(cl vdi.MemberClaim) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.updateFailures > 0 {
		c.updateFailures--
		return false, fmt.Errorf("the API server timed out")
	}
	cur, ok := c.claims[cl.Member.ID()]
	if !ok || cur.Version != cl.Version || cur.Holder != cl.Holder {
		return false, nil
	}
	c.rv++
	cl.Version = strconv.Itoa(c.rv)
	c.claims[cl.Member.ID()] = cl
	c.updates++
	return true, nil
}

// FailUpdates makes the next n UpdateClaim calls fail.
func (c *Cluster) FailUpdates(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updateFailures = n
}

// Updates counts successful UpdateClaim calls.
func (c *Cluster) Updates() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.updates
}

// Claim returns member's claim.
func (c *Cluster) Claim(ref vdi.MemberRef) (vdi.MemberClaim, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl, ok := c.claims[ref.ID()]
	return cl, ok
}

// ReleaseMember implements vdi.Cluster.
func (c *Cluster) ReleaseMember(cl vdi.MemberClaim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := cl.Member.ID()
	cur, ok := c.claims[id]
	if !ok || cur.Holder != cl.Holder || cur.Version != cl.Version {
		return fmt.Errorf("claim on %s changed concurrently; not released", id)
	}
	if c.releaseFailures[id] > 0 {
		c.releaseFailures[id]--
		return fmt.Errorf("stop %s: the API server timed out", id)
	}
	if m, ok := c.members[id]; ok {
		m.running = false
		c.stops[id]++
		if p := c.pools[vdi.PoolRef{Kind: m.ref.Kind, Namespace: m.ref.Namespace, Name: m.pool}.ID()]; p != nil && p.mode == vdi.PoolModeEphemeral {
			c.recreates[id]++
		}
	}
	delete(c.claims, id)
	return nil
}
