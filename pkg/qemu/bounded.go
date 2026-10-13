package qemu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Bounded native commands: every subprocess this package owns runs under a
// deadline with its output capped, and is killed and reaped by the OWNING
// goroutine — never abandoned to a background goroutine on cancellation.
//
// The mechanism is exec.CommandContext with a group-killing Cancel plus
// same-goroutine Wait: when the effective deadline fires, the child's whole
// process group is SIGKILLed and Wait reaps the leader before this function
// returns, so a hung manager leaves no process and no goroutine behind.
// There is deliberately no fan-out/select abandonment anywhere on this
// path: a cancelled caller waits for the kill and the reap (fast — SIGKILL
// is not negotiable), it does not leave them behind.
//
// Two bounds apply. The default bound (systemctlTimeout) governs every
// real call, so lifecycle operations without a caller context (Start, Stop,
// Delete) are still finite. A caller WITH a context gets the earlier of
// its own deadline and the default, honored MID-call: the systemctlRun
// seam carries the caller's context into runBoundedCmd (see qemu.go), so a
// mid-call cancel kills the group within milliseconds instead of running
// to the default bound. Retiring the subprocess is always preferred to
// abandoning it.
var (
	// systemctlTimeout bounds one real systemctl call. Variables so tests
	// can shrink them; production values assume a responsive manager and
	// bound only the wedged case.
	systemctlTimeout = 60 * time.Second
	// systemctlOutputLimit caps combined output of one real systemctl
	// call. systemctl answers are small; anything past this is a wedged
	// or hostile manager, and the child is killed rather than drained.
	systemctlOutputLimit int64 = 1 << 20
	// boundedWaitDelay bounds Wait's patience for I/O pipes a dead
	// leader's descendants still hold: past it the pipes are closed so
	// Wait returns instead of hanging on orphan-held stdout. It fires
	// only when the group kill missed something (a descendant that
	// escaped the group, or a platform without group kill); the normal
	// path reaps in milliseconds.
	boundedWaitDelay = 2 * time.Second
)

// cappedBuffer is a combined-output sink that kills its process instead of
// growing past limit. Writes past the limit are swallowed (reported as
// written) so reaping proceeds normally; the exceeded flag produces the
// error after Wait. Stdout and stderr copy goroutines share one buffer,
// hence the mutex.
type cappedBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	limit    int64
	exceeded bool
	killed   bool
	kill     func()
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.exceeded && int64(c.buf.Len())+int64(len(p)) > c.limit {
		c.exceeded = true
	}
	if c.exceeded {
		// A limitless producer must not hold the wait hostage: kill it
		// at the first over-limit write; it dies, its pipes hit EOF, and
		// Wait returns. Kill is fire-and-forget and idempotent.
		if !c.killed {
			c.killed = true
			if c.kill != nil {
				c.kill()
			}
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf.Bytes()...)
}

// runBoundedCmd runs name with args under the earlier of ctx's deadline
// and now+timeout, with combined output capped at limit bytes. Like
// CombinedOutput it returns the output alongside any error, so callers can
// quote a failed command's stderr. The child is always reaped by this
// goroutine: timeout, over-limit output, and caller cancellation all kill
// first and return after the reap.
//
// The kill retires the whole process group, not just the leader: the child
// starts isolated (its own group on Unix — see isolateBoundedCmd) and both
// the deadline/cancel path (a group-killing Cancel override) and the
// over-limit path (the capped buffer's kill) signal the group, so
// descendants holding the stdout/stderr pipes die with the leader instead
// of wedging Wait on EOF past the bound. WaitDelay backstops even a group
// escapee: past it the pipes are closed and Wait returns. Residual, stated:
// a descendant that escapes the group (setsid) and outlives the call is
// still an orphan — nothing this side of a PID namespace can prevent that —
// but it can no longer hold the wait hostage.
func runBoundedCmd(ctx context.Context, timeout time.Duration, limit int64, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	eff, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(eff, name, args...)
	isolateBoundedCmd(cmd)
	cmd.WaitDelay = boundedWaitDelay
	// Group-killing Cancel: the default kills only the leader, whose
	// pipe-holding descendants would then wedge Wait past the bound. A
	// gone group reports done (preserving the success-racing-deadline
	// outcome); anything signalled reports through the context error.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := killBounded(cmd); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	buf := &cappedBuffer{limit: limit}
	buf.kill = func() {
		// Process is assigned by Start before the copy goroutines that
		// call this exist, so it is always set here. The whole group
		// goes: a capped leader with live descendants must not leave
		// them holding the pipes (and the wait) open.
		if cmd.Process != nil {
			_ = killBounded(cmd)
		}
	}
	cmd.Stdout, cmd.Stderr = buf, buf
	err := cmd.Run()
	out := buf.bytes()
	if buf.exceeded {
		return out, fmt.Errorf("%s output exceeded %d bytes (process killed and reaped)", name, limit)
	}
	if err != nil {
		if eff.Err() != nil {
			// Our bound fired and the child was killed and reaped;
			// attribute it to whoever set the earlier deadline.
			// (A success racing the deadline can land here when the
			// group kill wins the race — reported as the bound, which
			// is what fired.)
			if ctx.Err() != nil {
				return out, fmt.Errorf("%s interrupted: %w", name, ctx.Err())
			}
			return out, fmt.Errorf("%s timed out after %s (process killed and reaped): %w", name, timeout, err)
		}
		return out, err
	}
	return out, nil
}
