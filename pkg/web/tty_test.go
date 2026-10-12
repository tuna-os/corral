package web

// Tests for ttyBridge (GET /api/tty/{ns}/{name}) — the tty cell the parity
// matrix ships for kubevirt (`virtctl console`) and incus (`incus exec`)
// without a covering test. The doubles are stub binaries on PATH that record
// their argv and then behave like a terminal (cat): the tests assert the
// bridge reaches the right command and relays bytes both ways.
//
// The argv assertion is race-free by construction: the stub records its argv
// at startup, and the echoed bytes prove it started, so reading the log after
// the echo cannot observe a half-started process.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// stubConsole puts a recording cat-like binary on PATH and returns the file
// its argv lands in.
func stubConsole(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\necho \"$@\" >> \"$ARGV_LOG\"\nexec cat\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGV_LOG", log)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func readArgv(t *testing.T, log string) string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("reading argv log: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

func TestTTYBridge_ProxiesVirtctlConsole(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	log := stubConsole(t, "virtctl")
	fx.Runner.AddResponseKV("kubectl",
		[]string{"get", "vm", "testvm", "-n", "tailvm", "-o", "name"},
		"vm.kubevirt.io/testvm", nil)

	ws, err := websocket.Dial(wsURL(fx.Server.URL)+"/api/tty/tailvm/testvm", "", "http://localhost")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// The bridge must reach `virtctl console`, not kubectl exec: only the
	// virtio-serial console works over plain pipes.
	if _, err := ws.Write([]byte("hello-tty")); err != nil {
		t.Fatalf("ws.Write: %v", err)
	}
	buf := make([]byte, 64)
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := ws.Read(buf)
	if err != nil {
		t.Fatalf("ws.Read: %v", err)
	}
	if got := string(buf[:n]); got != "hello-tty" {
		t.Errorf("ws got %q, want the console echo", got)
	}
	if got, want := readArgv(t, log), "console testvm -n tailvm"; got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestTTYBridge_ProxiesIncusExec(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	log := stubConsole(t, "incus")

	ws, err := websocket.Dial(wsURL(fx.Server.URL)+"/api/tty/incus/db1", "", "http://localhost")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	// The pty echoes our own bytes before any process spawns, so the argv
	// log is polled: its appearance proves `incus exec` started, and only
	// then does the echo round-trip mean anything.
	argv := pollArgv(t, log)
	for _, want := range []string{"exec", "db1", "--", "bash"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}

	// The pty layer transforms bytes (echo, CR/LF), so this asserts
	// containment, not an exact echo: the point is the bridge reaches the
	// instance's shell and relays both ways.
	if _, err := ws.Write([]byte("hello\n")); err != nil {
		t.Fatalf("ws.Write: %v", err)
	}
	buf := make([]byte, 256)
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := ws.Read(buf)
	if err != nil {
		t.Fatalf("ws.Read: %v", err)
	}
	if got := string(buf[:n]); !strings.Contains(got, "hello") {
		t.Errorf("ws got %q, want it to contain the shell output", got)
	}
}

// pollArgv waits for the stub to record its argv, proving the server spawned
// the console command.
func pollArgv(t *testing.T, log string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, err := os.ReadFile(log); err == nil {
			return strings.TrimSpace(string(raw))
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the console command to start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
