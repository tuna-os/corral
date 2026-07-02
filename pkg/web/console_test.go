package web

import (
	"net"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"golang.org/x/net/websocket"
)

// fakeConsoleDialer stands in for kubevirt.RealConsoleDialer in tests — no
// virtctl subprocess, just an in-memory net.Conn handed back directly.
type fakeConsoleDialer struct {
	conn    net.Conn
	err     error
	gotNS   string
	gotName string
	gotKind kubevirt.Console
}

func (f *fakeConsoleDialer) Dial(ns, name string, console kubevirt.Console) (net.Conn, error) {
	f.gotNS, f.gotName, f.gotKind = ns, name, console
	return f.conn, f.err
}

func TestVNCBridge_RelaysBytesBothWays(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	serverSide, testSide := net.Pipe()
	fake := &fakeConsoleDialer{conn: serverSide}
	orig := consoleDialer
	consoleDialer = fake
	defer func() { consoleDialer = orig }()

	ws, err := websocket.Dial(wsURL(fx.Server.URL)+"/api/vnc/tailvm/testvm", "", "http://localhost")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	if _, err := ws.Write([]byte("hello-from-browser")); err != nil {
		t.Fatalf("ws.Write: %v", err)
	}
	buf := make([]byte, 64)
	testSide.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := testSide.Read(buf)
	if err != nil {
		t.Fatalf("read from console conn: %v", err)
	}
	if got := string(buf[:n]); got != "hello-from-browser" {
		t.Errorf("console conn got %q, want %q", got, "hello-from-browser")
	}

	if _, err := testSide.Write([]byte("hello-from-guest")); err != nil {
		t.Fatalf("write to console conn: %v", err)
	}
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = ws.Read(buf)
	if err != nil {
		t.Fatalf("ws.Read: %v", err)
	}
	if got := string(buf[:n]); got != "hello-from-guest" {
		t.Errorf("ws got %q, want %q", got, "hello-from-guest")
	}

	if fake.gotNS != "tailvm" || fake.gotName != "testvm" || fake.gotKind != kubevirt.VNC {
		t.Errorf("dialed (%s, %s, %v), want (tailvm, testvm, VNC)", fake.gotNS, fake.gotName, fake.gotKind)
	}
}

func TestRDPBridge_RelaysBytesBothWays(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	serverSide, testSide := net.Pipe()
	fake := &fakeConsoleDialer{conn: serverSide}
	orig := consoleDialer
	consoleDialer = fake
	defer func() { consoleDialer = orig }()

	ws, err := websocket.Dial(wsURL(fx.Server.URL)+"/api/rdp/tailvm/winvm", "", "http://localhost")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	if _, err := ws.Write([]byte("rdp-client-hello")); err != nil {
		t.Fatalf("ws.Write: %v", err)
	}
	buf := make([]byte, 64)
	testSide.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := testSide.Read(buf)
	if err != nil {
		t.Fatalf("read from console conn: %v", err)
	}
	if got := string(buf[:n]); got != "rdp-client-hello" {
		t.Errorf("console conn got %q, want %q", got, "rdp-client-hello")
	}

	if fake.gotNS != "tailvm" || fake.gotName != "winvm" || fake.gotKind != kubevirt.RDP {
		t.Errorf("dialed (%s, %s, %v), want (tailvm, winvm, RDP)", fake.gotNS, fake.gotName, fake.gotKind)
	}
}

func TestConsoleBridges_DialErrorClosesCleanly(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	fake := &fakeConsoleDialer{err: net.ErrClosed}
	orig := consoleDialer
	consoleDialer = fake
	defer func() { consoleDialer = orig }()

	ws, err := websocket.Dial(wsURL(fx.Server.URL)+"/api/vnc/tailvm/testvm", "", "http://localhost")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = ws.Read(make([]byte, 16))
	if err == nil {
		t.Error("expected the bridge to close the websocket when Dial fails")
	}
}
