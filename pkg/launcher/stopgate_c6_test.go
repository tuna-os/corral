package launcher

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Unbound-stop gating: where stop signals cannot bind to the verified
// child (no pidfd — see qemu.DirectStopBound), live stops refuse by
// design instead of signalling through a possibly-reused PID. Tests that
// stop live VMs assert that refusal there and retire the child by hand;
// the stopped-state assertions after them are bound-platform-only.

// requireUnboundStopRefusal asserts the designed refusal of a live stop
// without binding. Terminal: callers return to bound-only assertions —
// or to no VM at all — after it.
func requireUnboundStopRefusal(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "cannot bind a signal target") {
		t.Fatalf("%s without stop binding must refuse naming the unbound target, got %v", what, err)
	}
}

// killStartedChild retires a child the test started where stops refuse:
// SIGKILL by the token pid, then observed death.
func killStartedChild(t *testing.T, pid int) {
	t.Helper()
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGKILL)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return
		}
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive 3s after SIGKILL", pid)
}
