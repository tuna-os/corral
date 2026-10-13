package qemu

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAllocatePorts_StartsAtHash(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	SetPortProbe(func(port int) bool { return true })
	t.Cleanup(func() { SetPortProbe(nil) })

	alloc, err := AllocatePorts("bluefin")
	if err != nil {
		t.Fatalf("AllocatePorts: %v", err)
	}
	if want := hashDisplay("bluefin"); alloc.Display != want {
		t.Errorf("display = %d, want hash %d", alloc.Display, want)
	}
	if alloc.VNCPort != 5900+alloc.Display || alloc.SSHPort != 2200+alloc.Display {
		t.Errorf("ports not derived from display: %+v", alloc)
	}
}

func TestAllocatePorts_SkipsRecordedCollision(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	SetPortProbe(func(port int) bool { return true })
	t.Cleanup(func() { SetPortProbe(nil) })

	// Occupy bluefin's hash display via another VM's metadata.
	taken := hashDisplay("bluefin")
	dir := filepath.Join(home, "taken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"name": "taken", "vnc_port": 5900 + taken, "ssh_port": 2200 + taken}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	alloc, err := AllocatePorts("bluefin")
	if err != nil {
		t.Fatalf("AllocatePorts: %v", err)
	}
	if alloc.Display == taken {
		t.Errorf("collision display %d was reused", taken)
	}
	if alloc.VNCPort == 5900+taken || alloc.SSHPort == 2200+taken {
		t.Errorf("colliding ports reused: %+v", alloc)
	}
}

func TestAllocatePorts_SkipsLiveBindCollision(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	// The hash display's VNC port is live-bound elsewhere.
	taken := hashDisplay("bluefin")
	SetPortProbe(func(port int) bool { return port != 5900+taken })
	t.Cleanup(func() { SetPortProbe(nil) })

	alloc, err := AllocatePorts("bluefin")
	if err != nil {
		t.Fatalf("AllocatePorts: %v", err)
	}
	if alloc.Display == taken {
		t.Errorf("live-bound display %d was reused", taken)
	}
}

func TestAllocatePorts_Exhausted(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	SetPortProbe(func(port int) bool { return false })
	t.Cleanup(func() { SetPortProbe(nil) })

	if _, err := AllocatePorts("bluefin"); err == nil {
		t.Error("expected an error when all displays are busy")
	}
}

func TestConsoleBindAddr(t *testing.T) {
	if got := ConsoleBindAddr(true, "100.64.0.1"); got != Loopback {
		t.Errorf("loopback bind = %q, want %q", got, Loopback)
	}
	if got := ConsoleBindAddr(false, "100.64.0.1"); got != "100.64.0.1" {
		t.Errorf("tailscale bind = %q", got)
	}
	if got := ConsoleBindAddr(false, ""); got != Loopback {
		t.Errorf("empty fallback = %q, want loopback", got)
	}
}

func TestLoopbackVNCAddr(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	dir := filepath.Join(home, "vm1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"name": "vm1", "vnc_port": 5942, "tailscale_ip": "100.64.0.1"}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	addr, err := LoopbackVNCAddr("vm1")
	if err != nil {
		t.Fatalf("LoopbackVNCAddr: %v", err)
	}
	if addr != "127.0.0.1:5942" {
		t.Errorf("addr = %q, want 127.0.0.1:5942", addr)
	}
}
