package vdi

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// One-click connect (#229). Connect resolves a claimed desktop to one of
// Corral's existing console transports: browser RDP (IronRDP through the
// RDCleanPath proxy, ADR-0002), noVNC, or the xterm.js terminal. It adds no
// display protocol. The rules are deterministic and run on every request,
// so a guest whose RDP service starts or stops gets the right answer the
// next time:
//
//  1. A CT member offers only its terminal.
//  2. A VM member offers RDP when TCP 3389 answers on the guest, then VNC
//     when it has a graphics device, then its serial console.
//  3. The first protocol on that list is the target; the rest are the
//     fallbacks, in order. Skipped says why a protocol is missing.

// Protocol is a console transport.
type Protocol string

const (
	ProtocolRDP      Protocol = "rdp"
	ProtocolVNC      Protocol = "vnc"
	ProtocolTerminal Protocol = "terminal"
)

// Connect errors. pkg/web maps them to 409, with the message as the
// action to take.
var (
	ErrNotRunning   = errors.New("the desktop is not running; start it, then connect")
	ErrClaimExpired = errors.New("the claim on this desktop expired; claim the pool again to renew it, then connect")
	ErrUnreachable  = errors.New("the desktop offers no console Corral can open")
)

// RDPProbe reports whether member answers RDP. It returns the address that
// answered, or an error that says why nothing did.
type RDPProbe func(c MemberClaim) (addr string, err error)

// ConnectOption is one way to reach a desktop.
type ConnectOption struct {
	Protocol Protocol `json:"protocol"`
	// Path is the console websocket route on this Corral (relative URL).
	// It reuses the direct-first and peer-relay transports of the route.
	Path string `json:"path"`
	// Direct is the guest address a native client can dial, when the
	// probe reached one (RDP only).
	Direct string `json:"direct,omitempty"`
}

// ConnectTarget is the result of Connect: the chosen option, its
// fallbacks, and the desktop it belongs to.
type ConnectTarget struct {
	ConnectOption
	Desktop   Desktop         `json:"desktop"`
	Fallbacks []ConnectOption `json:"fallbacks,omitempty"`
	Skipped   []string        `json:"skipped,omitempty"`
}

func consolePath(kind string, ref MemberRef, query string) string {
	p := "/api/" + kind + "/" + url.PathEscape(ref.Namespace) + "/" + url.PathEscape(ref.Name)
	if query != "" {
		p += "?" + query
	}
	return p
}

// ResolveConnect applies the protocol rules to a running member.
func ResolveConnect(c MemberClaim, state MemberState, probe RDPProbe) (ConnectTarget, error) {
	var options []ConnectOption
	var skipped []string
	if c.Member.Kind == PoolKindVM {
		switch addr, err := probe(c); {
		case err != nil:
			skipped = append(skipped, "rdp: "+err.Error())
		default:
			options = append(options, ConnectOption{Protocol: ProtocolRDP, Path: consolePath("rdp", c.Member, "rdcleanpath=1"), Direct: addr})
		}
		if state.VNC {
			options = append(options, ConnectOption{Protocol: ProtocolVNC, Path: consolePath("vnc", c.Member, "")})
		} else {
			skipped = append(skipped, "vnc: the VM has no graphics device")
		}
	}
	if state.Terminal {
		options = append(options, ConnectOption{Protocol: ProtocolTerminal, Path: consolePath("tty", c.Member, "")})
	} else {
		skipped = append(skipped, "terminal: the VM has no serial console")
	}
	if len(options) == 0 {
		return ConnectTarget{Skipped: skipped}, fmt.Errorf("%w (%s)", ErrUnreachable, strings.Join(skipped, "; "))
	}
	return ConnectTarget{ConnectOption: options[0], Fallbacks: options[1:], Skipped: skipped}, nil
}

// Connect resolves the actor's desktop to a console target. Only the
// holder or an admin may connect. A claim that ended is ErrNotFound, an
// expired one ErrClaimExpired, and a stopped member ErrNotRunning.
func (b *Broker) Connect(a Actor, ref MemberRef, probe RDPProbe) (ConnectTarget, error) {
	c, err := b.authorize(a, ref)
	if err != nil {
		return ConnectTarget{}, err
	}
	if c.Expired(b.now()) {
		return ConnectTarget{}, fmt.Errorf("%w: %s", ErrClaimExpired, ref.ID())
	}
	state, err := b.cluster.MemberState(c)
	if err != nil {
		return ConnectTarget{}, fmt.Errorf("read the state of %s: %w", ref.ID(), err)
	}
	if !state.Running {
		return ConnectTarget{}, fmt.Errorf("%w (%s is %s)", ErrNotRunning, ref.ID(), state.Phase)
	}
	t, err := ResolveConnect(c, state, probe)
	t.Desktop = b.desktopWithState(c, state)
	return t, err
}

// AuthorizeConsole decides whether the actor may open a console on
// namespace/member. A member that a user claimed is open only to that user
// and to admins. Any other VM or CT is left to the existing gates.
func (b *Broker) AuthorizeConsole(a Actor, namespace, member string) error {
	if !b.serves(namespace) {
		return nil
	}
	for _, kind := range []PoolKind{PoolKindVM, PoolKindCT} {
		ref := MemberRef{Kind: kind, Namespace: namespace, Name: member}
		c, found, err := b.cluster.GetClaim(ref)
		if err != nil {
			return fmt.Errorf("read claim of %s: %w", ref.ID(), err)
		}
		if !found || c.Holder == "" || c.Holder == CTReconcilerHolder {
			continue
		}
		if a.Admin || (a.Identity != "" && a.Identity == c.Holder) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrForbidden, ref.ID())
	}
	return nil
}

// DialRDP is the TCP dial of ProbeRDP; tests replace it.
var DialRDP = func(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 1500*time.Millisecond)
}

// ProbeRDP is the RDPProbe of a process outside the cluster network, such
// as the CLI: it reads the guest address from the VMI and dials TCP 3389
// from this machine. An address that answers here is one a native client
// here can use.
func ProbeRDP(c MemberClaim) (string, error) {
	out, err := run("kubectl", "get", "vmi", c.Member.Name, "-n", c.Member.Namespace, "-o", "jsonpath={.status.interfaces[0].ipAddress}")
	ip := strings.TrimSpace(string(out))
	if err != nil || ip == "" {
		return "", fmt.Errorf("the VM has no guest address yet")
	}
	addr := net.JoinHostPort(ip, "3389")
	conn, err := DialRDP(addr)
	if err != nil {
		return "", fmt.Errorf("TCP 3389 did not answer at %s from this machine", addr)
	}
	_ = conn.Close()
	return addr, nil
}
