//go:build linux

package qemu

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

// No PID fallback on Linux: where pidfd_open is unavailable the stop
// refuses instead of signalling through a possibly-reused PID. Old code
// fell back to a retained PID handle on ENOSYS/EPERM, keeping the
// verification-to-signal reuse race.

// TestAcquirePIDHandle_LinuxRefusesWithoutPidfd stages pidfd_open
// failures: every one refuses naming the reused-PID race.
func TestAcquirePIDHandle_LinuxRefusesWithoutPidfd(t *testing.T) {
	orig := pidfdOpen
	t.Cleanup(func() { pidfdOpen = orig })
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "ENOSYS pre-5.3 kernel", err: syscall.ENOSYS},
		{name: "EPERM seccomp-blocked pidfd_open", err: syscall.EPERM},
		{name: "other pidfd failure", err: syscall.EMFILE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidfdOpen = func(pid int, flags int) (int, error) { return -1, tc.err }
			h, err := acquirePIDHandle(os.Getpid())
			if err == nil {
				h.Release()
				t.Fatalf("acquire with pidfd failing %v must refuse, got a handle", tc.err)
			}
			if !strings.Contains(err.Error(), "possibly-reused PID") {
				t.Errorf("unbound refusal must name the reused-PID race, got %v", err)
			}
		})
	}
}

// TestAcquirePIDHandle_LinuxGoneOnESRCH: a pid already empty at open
// still yields the signalling-nothing gone handle (stale-record success),
// not a refusal.
func TestAcquirePIDHandle_LinuxGoneOnESRCH(t *testing.T) {
	orig := pidfdOpen
	t.Cleanup(func() { pidfdOpen = orig })
	pidfdOpen = func(pid int, flags int) (int, error) { return -1, syscall.ESRCH }
	h, err := acquirePIDHandle(1<<30 - 7)
	if err != nil {
		t.Fatalf("acquire of an empty pid must not fail: %v", err)
	}
	defer h.Release()
	if serr := h.Signal(syscall.Signal(0)); !errors.Is(serr, syscall.ESRCH) {
		t.Errorf("gone-handle signal = %v, want ESRCH", serr)
	}
}
