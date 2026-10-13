package qemu

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Whole-group retirement: a bounded call retires the leader's entire
// process group (so pipe-holding descendants die with it) and WaitDelay
// backstops even a group escapee, so descendant-held stdout can neither
// orphan processes nor hang the wait. Old code killed the leader only with
// WaitDelay 0: descendants survived and Wait hung on their held pipes.
// These tests use real supervised helpers with pid-file publication and
// retirement observation.

// readPidFileForTest reads a helper-published pid file.
func readPidFileForTest(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("helper never published %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("bad helper pid %q: %v", raw, err)
	}
	return pid
}

// killPidFile best-effort SIGKILLs a helper-published pid (hang-guard and
// test cleanup: missing/unparsable files are ignored).
func killPidFile(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGKILL)
	}
}

// runBoundedGuarded runs runBoundedCmd on a test goroutine with a hang
// guard: old code hangs forever on descendant-held pipes, so the guard
// retires the strays (releasing the hung wait) and fails instead of
// wedging the package.
func runBoundedGuarded(t *testing.T, bound time.Duration, limit int64, strays []string, name string, args ...string) (out []byte, runErr error, elapsed time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		start := time.Now()
		out, runErr = runBoundedCmd(context.Background(), bound, limit, name, args...)
		elapsed = time.Since(start)
		close(done)
	}()
	select {
	case <-done:
		return out, runErr, elapsed
	case <-time.After(20 * time.Second):
		for _, stray := range strays {
			killPidFile(stray)
		}
		select {
		case <-done:
			t.Fatalf("bounded run hung past 20s (bound %s); out=%d bytes err=%v", bound, len(out), runErr)
		case <-time.After(10 * time.Second):
			t.Fatalf("bounded run hung and its strays resisted cleanup")
		}
		return nil, nil, 0
	}
}

func TestRunBoundedCmd_RetiresDescendantHeldStdout(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("process-group retirement needs Unix groups (no group kill on %s)", runtime.GOOS)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	dir := t.TempDir()
	leaderPid := filepath.Join(dir, "leader.pid")
	childPid := filepath.Join(dir, "child.pid")
	// The leader spawns a background grandchild inheriting stdout,
	// publishes both pids, then stalls past the bound with the pipes
	// held at both ends.
	script := "echo $$ > " + leaderPid + "; " + sleep + " 30 & echo $! > " + childPid + "; exec " + sleep + " 30"
	out, runErr, elapsed := runBoundedGuarded(t, 500*time.Millisecond, 1<<20,
		[]string{leaderPid, childPid}, sh, "-c", script)
	if runErr == nil || !strings.Contains(runErr.Error(), "timed out") {
		t.Errorf("stalled group must fail naming the timeout, got out=%q err=%v", out, runErr)
	}
	if elapsed > 15*time.Second {
		t.Errorf("bounded run took %s, want the 500ms bound", elapsed)
	}
	// Whole-group retirement observed: neither pid survives.
	signalDead(t, readPidFileForTest(t, leaderPid), 3*time.Second)
	signalDead(t, readPidFileForTest(t, childPid), 3*time.Second)
}

func TestRunBoundedCmd_CapKillsDescendants(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("process-group retirement needs Unix groups (no group kill on %s)", runtime.GOOS)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	dir := t.TempDir()
	leaderPid := filepath.Join(dir, "leader.pid")
	childPid := filepath.Join(dir, "child.pid")
	// A background sleeper holds the pipes while the leader floods past
	// the cap: the cap kill must retire the sleeper too, or Wait wedges
	// on its held descriptors.
	script := "echo $$ > " + leaderPid + "; " + sleep + " 30 & echo $! > " + childPid + "; while true; do echo xxxxxxxxxx; done"
	out, runErr, elapsed := runBoundedGuarded(t, 10*time.Second, 1024,
		[]string{leaderPid, childPid}, sh, "-c", script)
	if runErr == nil || !strings.Contains(runErr.Error(), "exceeded") {
		t.Errorf("over-limit group must fail naming the cap, got out=%d bytes err=%v", len(out), runErr)
	}
	if elapsed > 15*time.Second {
		t.Errorf("capped run took %s", elapsed)
	}
	if len(out) > 64*1024 {
		t.Errorf("capped run returned %d bytes, want near the 1KiB cap", len(out))
	}
	signalDead(t, readPidFileForTest(t, leaderPid), 3*time.Second)
	signalDead(t, readPidFileForTest(t, childPid), 3*time.Second)
}

func TestRunBoundedCmd_EscapedDescendantCannotHangWait(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("setsid-escape staging is linux-only")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skipf("setsid not available: %v", err)
	}
	dir := t.TempDir()
	leaderPid := filepath.Join(dir, "leader.pid")
	escapePid := filepath.Join(dir, "escape.pid")
	// An escapee that leaves the group (setsid) and holds stdout: the
	// group kill cannot reach it, so WaitDelay must close the pipes and
	// return instead of hanging. (The escapee publishes its own pid from
	// inside the setsid, so the file names the survivor either way.)
	script := setsid + " " + sh + " -c 'echo $$ > " + escapePid + "; exec " + sleep + " 30' & echo $$ > " + leaderPid + "; exec " + sleep + " 30"
	t.Cleanup(func() {
		killPidFile(escapePid)
		killPidFile(leaderPid)
	})
	out, runErr, elapsed := runBoundedGuarded(t, 500*time.Millisecond, 1<<20,
		[]string{leaderPid, escapePid}, sh, "-c", script)
	if runErr == nil || !strings.Contains(runErr.Error(), "timed out") {
		t.Errorf("stalled group with an escapee must fail naming the timeout, got out=%q err=%v", out, runErr)
	}
	if elapsed > 15*time.Second {
		t.Errorf("bounded run took %s, want the bound plus WaitDelay", elapsed)
	}
	signalDead(t, readPidFileForTest(t, leaderPid), 3*time.Second)
	// The escapee must REALLY have escaped: had it died with the group,
	// this test would pass vacuously (EOF, not WaitDelay, released it).
	escape := readPidFileForTest(t, escapePid)
	if proc, err := os.FindProcess(escape); err != nil || proc.Signal(syscall.Signal(0)) != nil {
		t.Fatalf("escapee %d died with the group — setsid staging failed, test is vacuous", escape)
	}
	killPidFile(escapePid)
	signalDead(t, escape, 3*time.Second)
}

// TestRunBoundedCmd_LeaderExitGrandchildHeldPipes stages the true
// leader-exit edge: the leader exits at once while its background
// grandchild holds the pipes and sleeps. The bound's group kill must
// retire the grandchild (observed dead) and return the timeout — old
// code killed only the leader (already gone), then waited on the
// grandchild-held pipes until it exited on its own (~30s, reported as
// success), failing both assertions below.
func TestRunBoundedCmd_LeaderExitGrandchildHeldPipes(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("process-group retirement needs Unix groups (no group kill on %s)", runtime.GOOS)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not available: %v", err)
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	dir := t.TempDir()
	childPid := filepath.Join(dir, "child.pid")
	script := sleep + " 30 & echo $! > " + childPid + "; exit 0"
	out, runErr, elapsed := runBoundedGuarded(t, 500*time.Millisecond, 1<<20,
		[]string{childPid}, sh, "-c", script)
	if runErr == nil || !strings.Contains(runErr.Error(), "timed out") {
		t.Errorf("leader-exit with held pipes must fail naming the timeout, got out=%q err=%v", out, runErr)
	}
	if elapsed > 15*time.Second {
		t.Errorf("bounded run took %s, want the 500ms bound", elapsed)
	}
	signalDead(t, readPidFileForTest(t, childPid), 3*time.Second)
}

// TestIsRunningCtx_MidCallCancelPreemptsStalledProbe cancels the caller
// mid-probe with the FULL default bound in force: the probe must die
// within milliseconds of the cancel, not run to the 60s bound. Old code
// honored the caller only before dispatch, so this probe ran the whole
// default bound (the test fails on elapsed there).
func TestIsRunningCtx_MidCallCancelPreemptsStalledProbe(t *testing.T) {
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
	// The real runner with the UNSHRUNK default bound: preemption must
	// come from the caller context, not from a small bound.
	SetSystemctl(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(500*time.Millisecond, cancel)
	start := time.Now()
	if IsRunningCtx(ctx, "gate") {
		t.Error("cancelled probe must report not-running")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("mid-call-cancelled probe took %s — caller cancellation was not honored mid-call (want ~500ms)", elapsed)
	}
	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("fake systemctl never ran (no pid file): %v", rerr)
	}
	pid, aerr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if aerr != nil {
		t.Fatalf("bad helper pid %q: %v", raw, aerr)
	}
	t.Cleanup(func() {
		if proc, _ := os.FindProcess(pid); proc != nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	})
	signalDead(t, pid, 3*time.Second)
}
