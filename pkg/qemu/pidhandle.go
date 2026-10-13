package qemu

import (
	"os"
	"syscall"
)

// A lifetimeHandle is an acquired signal target for one recorded pid. It is
// acquired BEFORE the record is verified (see stopDirectLocked) and pins
// the exact process (Linux pidfd — see pidhandle_linux.go), so signals sent
// through it cannot land on a pid reused after verification. The handle is
// retained across the whole TERM → KILL escalation and released at the end
// — signals never go through a freshly looked-up pid.
//
// A handle that cannot be acquired refuses the stop: without a bound target
// there is nothing proven-safe to signal. There is deliberately NO
// PID-backed fallback anywhere: signalling through a possibly-reused PID
// keeps the verification-to-signal race no matter how recently the pid was
// verified, so an unbound stop refuses instead of signalling.
//
// Platform consequences, stated plainly: on Linux a pidfd pins every stop;
// where pidfd_open itself is unavailable (pre-5.3 kernels, seccomp
// profiles blocking it) live stops refuse and the operator resolves the
// pid by hand. Off Linux (notably macOS, which has no pidfd equivalent)
// live direct stops always refuse for the same reason; stale records still
// clean themselves (a provably-empty pid needs no signal). See
// acquirePIDHandle in pidhandle_linux.go and pidhandle_other.go.
type lifetimeHandle interface {
	Signal(os.Signal) error
	Release()
}

// goneHandle is the handle for a pid already dead at acquire time: it
// signals nothing and reports ESRCH, so the escalation treats it exactly
// like a signal-time ESRCH (stale record, success).
type goneHandle struct{}

func (goneHandle) Signal(os.Signal) error { return syscall.ESRCH }
func (goneHandle) Release()               {}

// procAcquire acquires the lifetime handle for pid. It is a variable so
// tests can pin acquisition order and failure without real processes.
var procAcquire = acquirePIDHandle

// DirectStopBound reports whether this process can bind stop signals to
// the verified child (Linux with a working pidfd_open). Where it reports
// false, live direct stops refuse rather than signal through a
// possibly-reused PID; the operator stops the recorded pid by hand after
// inspecting it. Tests gate live-stop expectations on it.
func DirectStopBound() bool {
	h, err := acquirePIDHandle(os.Getpid())
	if err != nil {
		return false
	}
	h.Release()
	return true
}
