package qemu

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Unbound-stop gating: where stop signals cannot bind to the verified
// child (no pidfd — see DirectStopBound), live stops refuse by design
// instead of signalling through a possibly-reused PID. Tests that stop
// live VMs assert that refusal there and retire the child by hand; the
// stopped-state assertions after them are bound-platform-only.

// requireUnboundStopRefusal asserts the designed refusal of a live stop
// without binding. Terminal: callers return to bound-only assertions —
// or to no VM at all — after it.
func requireUnboundStopRefusal(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "cannot bind a signal target") {
		t.Fatalf("%s without stop binding must refuse naming the unbound target, got %v", what, err)
	}
}

// killTestPID retires a test child by pid where stops refuse: SIGKILL,
// then observed death (signalDead fails the test if it sticks).
func killTestPID(t *testing.T, pid int) {
	t.Helper()
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGKILL)
	}
	signalDead(t, pid, 3*time.Second)
}

// killRecordedChild retires the recorded child of name by its pid file.
// The file must exist: refused stops keep the record.
func killRecordedChild(t *testing.T, name string) {
	t.Helper()
	rec, err := readPIDFile(name)
	if err != nil || rec.PID <= 0 {
		t.Fatalf("no recorded pid to retire for %q: %v", name, err)
	}
	killTestPID(t, rec.PID)
}
