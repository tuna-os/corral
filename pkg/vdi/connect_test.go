package vdi_test

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/vdi"
)

// Tests of one-click connect resolution (#229).

func rdpOpen(addr string) vdi.RDPProbe {
	return func(vdi.MemberClaim) (string, error) { return addr, nil }
}

func rdpClosed(c vdi.MemberClaim) (string, error) {
	return "", fmt.Errorf("TCP 3389 did not answer for %s", c.Member.Name)
}

func TestConnect_PrefersRDPThenVNCThenTerminal(t *testing.T) {
	b, _, _ := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	got, err := b.Connect(alice, desk1, rdpOpen("10.0.0.5:3389"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Protocol != vdi.ProtocolRDP || got.Path != "/api/rdp/vdi/desk-1?rdcleanpath=1" || got.Direct != "10.0.0.5:3389" {
		t.Fatalf("target = %+v", got.ConnectOption)
	}
	if len(got.Fallbacks) != 2 || got.Fallbacks[0].Protocol != vdi.ProtocolVNC || got.Fallbacks[0].Path != "/api/vnc/vdi/desk-1" ||
		got.Fallbacks[1].Protocol != vdi.ProtocolTerminal || got.Fallbacks[1].Path != "/api/tty/vdi/desk-1" {
		t.Fatalf("fallbacks = %+v", got.Fallbacks)
	}
	if got.Desktop.ID != desk1.ID() || got.Desktop.Owner != alice.Identity || len(got.Skipped) != 0 {
		t.Fatalf("desktop/skipped = %+v %v", got.Desktop, got.Skipped)
	}
}

func TestConnect_RDPAvailabilityChangesBetweenRequests(t *testing.T) {
	b, _, _ := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	got, err := b.Connect(alice, desk1, rdpClosed)
	if err != nil || got.Protocol != vdi.ProtocolVNC || len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "3389") {
		t.Fatalf("RDP down: %+v, %v", got, err)
	}
	if got, err = b.Connect(alice, desk1, rdpOpen("10.0.0.5:3389")); err != nil || got.Protocol != vdi.ProtocolRDP {
		t.Fatalf("RDP up: %+v, %v", got, err)
	}
	if got, err = b.Connect(alice, desk1, rdpClosed); err != nil || got.Protocol != vdi.ProtocolVNC {
		t.Fatalf("RDP down again: %+v, %v", got, err)
	}
}

func TestConnect_VNCOnlyAndTerminalOnlyVMs(t *testing.T) {
	b, c, _ := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	c.SetConsoles(desk1, true, false)
	got, err := b.Connect(alice, desk1, rdpClosed)
	if err != nil || got.Protocol != vdi.ProtocolVNC || len(got.Fallbacks) != 0 || len(got.Skipped) != 2 {
		t.Fatalf("VNC-only: %+v, %v", got, err)
	}
	c.SetConsoles(desk1, false, true)
	got, err = b.Connect(alice, desk1, rdpClosed)
	if err != nil || got.Protocol != vdi.ProtocolTerminal || got.Path != "/api/tty/vdi/desk-1" {
		t.Fatalf("terminal-only: %+v, %v", got, err)
	}
	c.SetConsoles(desk1, false, false)
	got, err = b.Connect(alice, desk1, rdpClosed)
	if !errors.Is(err, vdi.ErrUnreachable) || len(got.Skipped) != 3 {
		t.Fatalf("no console: %+v, %v", got, err)
	}
}

func TestConnect_CTMemberOffersItsTerminal(t *testing.T) {
	b, _, _ := newBroker(t)
	d := mustClaim(t, b, alice, ctPool)
	ref := vdi.MemberRef{Kind: vdi.PoolKindCT, Namespace: "vdi", Name: d.Member}
	probed := false
	got, err := b.Connect(alice, ref, func(vdi.MemberClaim) (string, error) { probed = true; return "x:3389", nil })
	if err != nil || got.Protocol != vdi.ProtocolTerminal || got.Path != "/api/tty/vdi/lab-1" || len(got.Fallbacks) != 0 {
		t.Fatalf("CT connect: %+v, %v", got, err)
	}
	if probed {
		t.Fatal("probed RDP on a CT")
	}
}

func TestConnect_Authorization(t *testing.T) {
	b, _, _ := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	if _, err := b.Connect(bob, desk1, rdpClosed); !errors.Is(err, vdi.ErrForbidden) {
		t.Errorf("bob connected to alice's desktop: %v", err)
	}
	if _, err := b.Connect(vdi.Actor{}, desk1, rdpClosed); !errors.Is(err, vdi.ErrUnauthenticated) {
		t.Errorf("anonymous connect: %v", err)
	}
	if got, err := b.Connect(admin, desk1, rdpClosed); err != nil || got.Desktop.Owner != alice.Identity {
		t.Errorf("admin connect: %+v, %v", got, err)
	}
}

func TestConnect_StaleAndStoppedDesktops(t *testing.T) {
	b, c, _ := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	c.SetRunning(desk1, false)
	if _, err := b.Connect(alice, desk1, rdpClosed); !errors.Is(err, vdi.ErrNotRunning) || !strings.Contains(err.Error(), "start it") {
		t.Errorf("stopped desktop: %v", err)
	}
	if err := release(t, b, alice, desk1); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Connect(alice, desk1, rdpClosed); !errors.Is(err, vdi.ErrNotFound) {
		t.Errorf("released desktop: %v", err)
	}

	// A claim whose Lease expired: another user may take it over.
	c.SetClaim(vdi.MemberClaim{Member: desk2, Pool: "desk", Holder: alice.Identity, AcquiredAt: t0.Add(-3 * time.Hour), RenewedAt: t0.Add(-2 * time.Hour), Duration: time.Hour})
	if _, err := b.Connect(alice, desk2, rdpClosed); !errors.Is(err, vdi.ErrClaimExpired) {
		t.Errorf("expired claim: %v", err)
	}
}

func TestAuthorizeConsole(t *testing.T) {
	b, _, _ := newBroker(t)
	mustClaim(t, b, alice, vmPool)
	d := mustClaim(t, b, bob, ctPool)
	cases := []struct {
		actor     vdi.Actor
		ns, name  string
		forbidden bool
	}{
		{alice, "vdi", "desk-1", false},
		{bob, "vdi", "desk-1", true},
		{vdi.Actor{}, "vdi", "desk-1", true},
		{admin, "vdi", "desk-1", false},
		{alice, "vdi", d.Member, true},
		{bob, "vdi", d.Member, false},
		{bob, "vdi", "desk-2", false},         // unclaimed member
		{bob, "vdi", "some-vm", false},        // not a pool member
		{bob, "kube-system", "desk-1", false}, // not served
	}
	for _, tc := range cases {
		err := b.AuthorizeConsole(tc.actor, tc.ns, tc.name)
		if got := errors.Is(err, vdi.ErrForbidden); got != tc.forbidden {
			t.Errorf("%q on %s/%s: err = %v, want forbidden=%v", tc.actor.Identity, tc.ns, tc.name, err, tc.forbidden)
		}
	}
}

func TestResolveConnect_IsPure(t *testing.T) {
	c := vdi.MemberClaim{Member: vdi.MemberRef{Kind: vdi.PoolKindVM, Namespace: "a b", Name: "x/y"}}
	got, err := vdi.ResolveConnect(c, vdi.MemberState{Running: true, VNC: true}, rdpClosed)
	if err != nil || got.Path != "/api/vnc/a%20b/x%2Fy" {
		t.Fatalf("escaped path = %+v, %v", got, err)
	}
}

func TestProbeRDP(t *testing.T) {
	fake := shell.NewFake()
	vdi.SetRunner(fake)
	t.Cleanup(func() { vdi.SetRunner(shell.Real{}) })
	orig := vdi.DialRDP
	t.Cleanup(func() { vdi.DialRDP = orig })
	c := vdi.MemberClaim{Member: desk1}

	if _, err := vdi.ProbeRDP(c); err == nil || !strings.Contains(err.Error(), "no guest address") {
		t.Fatalf("no VMI: %v", err)
	}
	fake.AddResponseKV("kubectl", []string{"get", "vmi", "desk-1", "-n", "vdi", "-o", "jsonpath={.status.interfaces[0].ipAddress}"}, "10.0.0.5\n", nil)
	vdi.DialRDP = func(string) (net.Conn, error) { return nil, errors.New("refused") }
	if _, err := vdi.ProbeRDP(c); err == nil || !strings.Contains(err.Error(), "10.0.0.5:3389") {
		t.Fatalf("closed port: %v", err)
	}
	vdi.DialRDP = func(string) (net.Conn, error) { a, b := net.Pipe(); _ = b.Close(); return a, nil }
	if addr, err := vdi.ProbeRDP(c); err != nil || addr != "10.0.0.5:3389" {
		t.Fatalf("open port: %q, %v", addr, err)
	}
}
