//go:build linux

package qemu

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// pidfdHandle is a Linux lifetime handle: a pidfd pinning the exact process
// acquired for, opened before verification. Signals go through
// pidfd_send_signal, which targets the pinned process even if its pid was
// reused after verification — the reuse race that a PID-backed signal has
// cannot happen here.
type pidfdHandle struct {
	fd int
}

func (h *pidfdHandle) Signal(s os.Signal) error {
	sig, ok := s.(syscall.Signal)
	if !ok {
		return errors.New("os: unsupported signal type")
	}
	return unix.PidfdSendSignal(h.fd, unix.Signal(sig), nil, 0)
}

func (h *pidfdHandle) Release() {
	_ = unix.Close(h.fd)
}

// pidfdOpen opens the pidfd for pid. A variable so tests can stage
// pidfd-unavailable kernels (ENOSYS/EPERM) without one.
var pidfdOpen = unix.PidfdOpen

// acquirePIDHandle opens the lifetime handle for pid. ESRCH at open means
// the pid was already empty — a gone handle, which the escalation treats
// as a stale record. Anything else that is not a pinned pidfd refuses:
// kernels without pidfd_open (pre-5.3, ENOSYS), seccomp profiles blocking
// it (EPERM), and every other failure have no bound target to signal, and
// falling back to a PID handle would keep the verification-to-signal reuse
// race. The stop refuses with the record kept; the operator resolves the
// pid by hand (or allows pidfd_open in the seccomp profile).
func acquirePIDHandle(pid int) (lifetimeHandle, error) {
	fd, err := pidfdOpen(pid, 0)
	if err == nil {
		return &pidfdHandle{fd: fd}, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return goneHandle{}, nil
	}
	return nil, fmt.Errorf("cannot pin a lifetime handle for pid %d: pidfd unavailable (%v) — refusing to signal a possibly-reused PID", pid, err)
}
