package qemu

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Bounded native commands: a stalled subprocess is killed and reaped by
// the owning goroutine within the bound — never abandoned. These tests
// use real supervised helpers (a stalling script, an over-limit
// producer) with release/retirement observation, not abandoned selects.

func shrinkSystemctlBound(t *testing.T, timeout time.Duration) {
	t.Helper()
	orig := systemctlTimeout
	systemctlTimeout = timeout
	t.Cleanup(func() { systemctlTimeout = orig })
}

// signalDead polls until pid answers signal 0 with an error (dead and
// reaped) or the timeout expires.
func signalDead(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive %s after the bound fired — it was abandoned, not retired", pid, timeout)
}

func TestRunBoundedCmd_KillsAndReapsStalledHelper(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "helper.pid")
	// The helper publishes its pid, then stalls past any test budget.
	script := "echo $$ > " + pidFile + "; exec " + sleep + " 30"
	start := time.Now()
	out, err := runBoundedCmd(context.Background(), 300*time.Millisecond, 1<<20, sh, "-c", script)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("stalled helper must fail naming the timeout, got out=%q err=%v", out, err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("bounded run took %s, want the 300ms bound", elapsed)
	}
	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("helper never ran (no pid file): %v", rerr)
	}
	pid, aerr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if aerr != nil {
		t.Fatalf("bad helper pid %q: %v", raw, aerr)
	}
	// Retirement observed: the helper is dead and reaped, not abandoned.
	signalDead(t, pid, 3*time.Second)
}

func TestRunBoundedCmd_CapsOutput(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	// A limitless producer: killed at the cap, not drained forever.
	start := time.Now()
	out, err := runBoundedCmd(context.Background(), 10*time.Second, 1024, sh, "-c", "while true; do echo xxxxxxxxxx; done")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("over-limit output must fail naming the cap, got out=%d bytes err=%v", len(out), err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("capped run took %s", elapsed)
	}
	if len(out) > 64*1024 {
		t.Errorf("capped run returned %d bytes, want near the 1KiB cap", len(out))
	}
}

// TestIsRunningCtx_SupervisedStallRetiresHelper runs the liveness probe
// against a real (fake-binary) systemctl that stalls past the bound: the
// probe must report not-running AND the stalled helper must be observably
// dead afterwards. An abandonment design returns just as fast but leaves
// the helper (and its worker) alive — which is what fails there.
func TestIsRunningCtx_SupervisedStallRetiresHelper(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "systemctl.pid")
	script := "#!" + sh + "\n" +
		"echo $$ > \"$CORRAL_TEST_SYSTEMCTL_PIDFILE\"\n" +
		"sleep 30\n"
	fake := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CORRAL_TEST_SYSTEMCTL_PIDFILE", pidFile)
	// The real runner, with a shrunk default bound: the 5s caller
	// context below is longer than the 500ms bound, so the bound governs
	// the dispatched call; the mid-call-cancel case has its own test.
	SetSystemctl(nil)
	shrinkSystemctlBound(t, 500*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if IsRunningCtx(ctx, "gate") {
		t.Error("stalled probe must report not-running")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("stalled probe took %s", elapsed)
	}
	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("fake systemctl never ran (no pid file): %v", rerr)
	}
	pid, aerr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if aerr != nil {
		t.Fatalf("bad helper pid %q: %v", raw, aerr)
	}
	// Defensive reaper for the old world (which abandons the helper):
	// the pid is known, so nothing leaks even where the test fails.
	t.Cleanup(func() {
		if proc, _ := os.FindProcess(pid); proc != nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	})
	signalDead(t, pid, 3*time.Second)
}
