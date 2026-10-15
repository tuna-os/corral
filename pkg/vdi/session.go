package vdi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Session tracking and conservative reclaim (#228). The broker counts the
// open consoles of each pool member and records presence on the member's
// Lease. Reconcile then applies the reclaim policy:
//
//   - An explicit release reclaims at once (BeginRelease).
//   - The last console closing starts DisconnectGrace. A reconnect within
//     it cancels the reclaim.
//   - MaxSession, when set, reclaims a claim that old, connected or not.
//
// Presence is not activity: an open console does not prove that anyone
// types, and the broker never treats the age of a websocket as idleness.
// A claim whose console never opened is never reclaimed for disconnect;
// its user may connect with a native client the broker cannot see.

// Reclaim reasons, as AuditEvent.Reason and Desktop.ReclaimReason report them.
const (
	ReclaimDisconnected = "disconnect-grace"
	ReclaimMaxDuration  = "max-duration"
)

// ReclaimActor is the AuditEvent.Actor of a reclaim.
const ReclaimActor = "corral-vdi/broker"

const (
	annoConnectedAt    = "corral.dev/vdi-connected-at"
	annoDisconnectedAt = "corral.dev/vdi-disconnected-at"
)

// presence is this process's view of a member's consoles.
type presence struct {
	conns          int
	connectedAt    time.Time // start of the current connected period
	disconnectedAt time.Time // when conns last fell to zero
}

func presenceKey(namespace, member string) string { return namespace + "/" + member }

// ConsoleOpened records a console that opened on member and returns the
// function to call when it closes. Calling that function more than once
// counts once. Consoles of members outside the served namespaces are
// ignored.
func (b *Broker) ConsoleOpened(namespace, member string) (closed func()) {
	if !b.serves(namespace) {
		return func() {}
	}
	key := presenceKey(namespace, member)
	b.mu.Lock()
	p := b.presence[key]
	if p == nil {
		p = &presence{}
		b.presence[key] = p
	}
	if p.conns == 0 {
		p.connectedAt, p.disconnectedAt = b.now().UTC(), time.Time{}
	}
	p.conns++
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if p.conns == 0 {
				return
			}
			p.conns--
			if p.conns == 0 {
				p.disconnectedAt = b.now().UTC()
			}
		})
	}
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// sessionMarks merges this process's presence with the marks on the Lease.
// Presence older than the claim belongs to an earlier claim of the member
// and is ignored. A claim the Lease shows as connected, with no console
// open here, was connected under an earlier broker process: its grace
// period starts when this broker started, so a restart neither reclaims
// it at once nor keeps it forever.
func (b *Broker) sessionMarks(c MemberClaim) (SessionMarks, bool) {
	b.mu.Lock()
	var p presence
	if live := b.presence[presenceKey(c.Member.Namespace, c.Member.Name)]; live != nil {
		p = *live
	}
	b.mu.Unlock()
	marks := c.Session
	switch {
	case p.conns > 0:
		connectedAt := latest(p.connectedAt, c.AcquiredAt)
		if !marks.ConnectedAt.IsZero() && marks.DisconnectedAt.IsZero() && marks.ConnectedAt.Before(connectedAt) {
			connectedAt = latest(marks.ConnectedAt, c.AcquiredAt)
		}
		return SessionMarks{ConnectedAt: connectedAt}, true
	case !p.disconnectedAt.IsZero() && !p.disconnectedAt.Before(c.AcquiredAt):
		return SessionMarks{ConnectedAt: latest(p.connectedAt, c.AcquiredAt), DisconnectedAt: p.disconnectedAt}, false
	case !marks.ConnectedAt.IsZero() && marks.DisconnectedAt.IsZero():
		marks.DisconnectedAt = latest(b.started, marks.ConnectedAt)
	}
	return marks, false
}

// reclaimAt reports when the policy reclaims c, and why; reason is empty
// when no rule applies.
func (b *Broker) reclaimAt(c MemberClaim, marks SessionMarks, connected bool) (time.Time, string) {
	var at time.Time
	reason := ""
	if b.maxSession > 0 {
		at, reason = c.AcquiredAt.Add(b.maxSession), ReclaimMaxDuration
	}
	if b.grace > 0 && !connected && !marks.DisconnectedAt.IsZero() {
		if graceAt := marks.DisconnectedAt.Add(b.grace); reason == "" || graceAt.Before(at) {
			at, reason = graceAt, ReclaimDisconnected
		}
	}
	return at, reason
}

// Reconcile applies the reclaim policy to every claim once. It reclaims
// claims whose disconnect grace or maximum duration has run out, records
// console presence on the remaining claims, and renews their Leases so that
// they do not expire while the broker runs. A reclaim that fails keeps the
// claim and is retried by the next Reconcile.
func (b *Broker) Reconcile() error {
	b.reconcileMu.Lock()
	defer b.reconcileMu.Unlock()
	now := b.now().UTC()
	var errs []error
	seen := map[string]bool{}
	for _, ns := range b.namespaces {
		claims, err := b.cluster.Claims(ns)
		if err != nil {
			errs = append(errs, fmt.Errorf("list claims in ns/%s: %w", ns, err))
			// Keep this namespace's presence: the claims are unknown.
			for key := range b.presenceSnapshot() {
				seen[key] = true
			}
			continue
		}
		for _, c := range claims {
			seen[presenceKey(ns, c.Member.Name)] = true
			if err := b.reconcileClaim(c, now); err != nil {
				errs = append(errs, err)
			}
		}
	}
	b.mu.Lock()
	for key, p := range b.presence {
		if p.conns == 0 && !seen[key] {
			delete(b.presence, key)
		}
	}
	b.mu.Unlock()
	return errors.Join(errs...)
}

func (b *Broker) presenceSnapshot() map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	keys := make(map[string]bool, len(b.presence))
	for key := range b.presence {
		keys[key] = true
	}
	return keys
}

func (b *Broker) connected(c MemberClaim) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.presence[presenceKey(c.Member.Namespace, c.Member.Name)]
	return p != nil && p.conns > 0
}

func (b *Broker) reconcileClaim(c MemberClaim, now time.Time) error {
	marks, connected := b.sessionMarks(c)
	var releaseErr error
	if at, reason := b.reclaimAt(c, marks, connected); reason != "" && !now.Before(at) {
		// A console that opened since the marks were read cancels a
		// disconnect reclaim; the maximum duration applies regardless.
		if reason == ReclaimMaxDuration || !b.connected(c) {
			releaseErr = b.cluster.ReleaseMember(c)
			b.audit(AuditEvent{Action: "reclaim", Actor: ReclaimActor, Target: c.Member.ID(), Holder: c.Holder, Reason: reason, Err: releaseErr})
			if releaseErr == nil {
				return nil
			}
			releaseErr = fmt.Errorf("reclaim %s (%s): %w", c.Member.ID(), reason, releaseErr)
		}
	}
	if !sameMarks(marks, c.Session) || c.Duration <= 0 || now.Sub(c.RenewedAt) >= c.Duration/2 {
		c.Session, c.RenewedAt = marks, now
		if c.Duration <= 0 {
			c.Duration = claimDuration
		}
		if _, err := b.cluster.UpdateClaim(c); err != nil {
			return errors.Join(releaseErr, fmt.Errorf("renew %s: %w", c.Member.ID(), err))
		}
	}
	return releaseErr
}

func sameMarks(a, b SessionMarks) bool {
	return a.ConnectedAt.Equal(b.ConnectedAt) && a.DisconnectedAt.Equal(b.DisconnectedAt)
}

// Run calls Reconcile every interval until ctx ends, and passes each error
// to onError.
func (b *Broker) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := b.Reconcile(); err != nil && onError != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
