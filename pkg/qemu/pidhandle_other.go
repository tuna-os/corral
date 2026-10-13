//go:build !linux

package qemu

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// Non-Linux acquisition: no pidfd equivalent exists here (notably macOS),
// so a live pid can never be bound and live stops always refuse —
// signalling through a possibly-reused PID keeps the
// verification-to-signal race, and no verify-after-acquire ordering removes
// it. Only a provably-empty pid yields a handle, and it signals nothing
// (goneHandle), so stale records still clean themselves. The operator stops
// a live direct child by hand after inspecting its pid.
func acquirePIDHandle(pid int) (lifetimeHandle, error) {
	if err := procSignal0(pid); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return goneHandle{}, nil
		}
		return nil, fmt.Errorf("cannot bind a lifetime handle for pid %d on %s (no pidfd equivalent; signal target unverifiable: %v) — refusing to signal a possibly-reused PID", pid, runtime.GOOS, err)
	}
	return nil, fmt.Errorf("cannot bind a lifetime handle for pid %d on %s (no pidfd equivalent) — refusing to signal a possibly-reused PID", pid, runtime.GOOS)
}
