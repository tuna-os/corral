//go:build !linux

package qemu

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

// No PID fallback off Linux: without a pidfd equivalent a live pid can
// never be bound, so live stops always refuse instead of signalling
// through a possibly-reused PID. Old code always returned a retained PID
// handle here, keeping the verification-to-signal reuse race. Only a
// provably-empty pid yields a (signalling-nothing) handle.

// TestAcquirePIDHandle_OtherRefusesLive: alive and uninspectable pids
// refuse naming the reused-PID race.
func TestAcquirePIDHandle_OtherRefusesLive(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "alive pid", err: nil},
		{name: "alive but uninspectable pid", err: syscall.EPERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinSignal(t, tc.err)
			h, err := acquirePIDHandle(4242)
			if err == nil {
				h.Release()
				t.Fatal("acquire of a live unbound pid must refuse, got a handle")
			}
			if !strings.Contains(err.Error(), "possibly-reused PID") {
				t.Errorf("unbound refusal must name the reused-PID race, got %v", err)
			}
		})
	}
}

// TestAcquirePIDHandle_OtherGoneOnEmpty: a provably-empty pid yields the
// signalling-nothing gone handle (stale-record success), not a refusal.
func TestAcquirePIDHandle_OtherGoneOnEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "ESRCH", err: syscall.ESRCH},
		{name: "already-reaped handle", err: os.ErrProcessDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinSignal(t, tc.err)
			h, err := acquirePIDHandle(1<<30 - 7)
			if err != nil {
				t.Fatalf("acquire of an empty pid must not fail: %v", err)
			}
			defer h.Release()
			if serr := h.Signal(syscall.Signal(0)); !errors.Is(serr, syscall.ESRCH) {
				t.Errorf("gone-handle signal = %v, want ESRCH", serr)
			}
		})
	}
}
