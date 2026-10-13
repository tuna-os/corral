package qemu

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// Cross-process lifecycle-lock boundary: the flock must survive
// delete/recreate on ONE inode, and a blocked home must fail loudly.
//
// TestMain gates a helper-process entry: the waiter below re-execs the
// test binary with CORRAL_TEST_LIFECYCLE_HELPER=1, so the waiter is a
// genuinely different process (a goroutine would share the in-process
// slot and prove nothing about the flock).
func TestMain(m *testing.M) {
	if os.Getenv("CORRAL_TEST_LIFECYCLE_HELPER") == "1" {
		os.Exit(lifecycleHelperMain())
	}
	os.Exit(m.Run())
}

func lifecycleHelperMain() int {
	home := os.Getenv("CORRAL_TEST_LIFECYCLE_HOME")
	dir := os.Getenv("CORRAL_TEST_LIFECYCLE_DIR")
	holdSecs, err := strconv.Atoi(os.Getenv("CORRAL_TEST_LIFECYCLE_HOLD"))
	if err != nil || holdSecs <= 0 || home == "" || dir == "" {
		fmt.Fprintln(os.Stderr, "helper: bad environment")
		return 3
	}
	SetStateDirs(home, dir)
	started := filepath.Join(dir, "B_STARTED")
	if err := os.WriteFile(started, []byte("started\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "helper: %v\n", err)
		return 3
	}
	unlock, err := lockLifecycle("gate")
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper: lock: %v\n", err)
		return 2
	}
	if err := os.WriteFile(filepath.Join(dir, "B_ACQUIRED"), []byte("acquired\n"), 0o644); err != nil {
		unlock()
		fmt.Fprintf(os.Stderr, "helper: %v\n", err)
		return 3
	}
	time.Sleep(time.Duration(holdSecs) * time.Second)
	unlock()
	return 0
}

// waitForFile polls for path up to timeout.
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForWaiterFD waits until pid holds an open fd on one of paths — i.e.
// the helper is provably inside its flock wait (it cannot have acquired:
// the parent holds the lock). Linux-only; other Unix runs use a settle
// sleep instead (see below).
func waitForWaiterFD(t *testing.T, pid int, paths []string, timeout time.Duration) {
	t.Helper()
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err == nil {
			for _, fd := range fds {
				if target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name())); err == nil {
					if want[target] {
						return
					}
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("helper %d never entered its flock wait", pid)
}

func TestLifecycleLock_CrossProcessSurvivesDeleteRecreate(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin":
	default:
		t.Skipf("cross-process flock is Unix-only (no exclusion on %s)", runtime.GOOS)
	}
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	name := "gate"
	vmDir := filepath.Join(home, name)
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	// The parent holds the lifecycle lock, exactly as Delete does across
	// its stop → remove critical section.
	unlock1, err := lockLifecycle(name)
	if err != nil {
		t.Fatalf("parent lock: %v", err)
	}
	released1 := false
	defer func() {
		if !released1 {
			unlock1()
		}
	}()

	// A different-process waiter blocks on the lock.
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"CORRAL_TEST_LIFECYCLE_HELPER=1",
		"CORRAL_TEST_LIFECYCLE_HOME="+home,
		"CORRAL_TEST_LIFECYCLE_DIR="+dir,
		"CORRAL_TEST_LIFECYCLE_HOLD=3",
	)
	var helperErr bytes.Buffer
	cmd.Stderr = &helperErr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting waiter: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	waitForFile(t, filepath.Join(dir, "B_STARTED"), 10*time.Second)
	if runtime.GOOS == "linux" {
		// The waiter must be inside its flock wait before the
		// delete/recreate below — observed, not slept-for. Both the
		// stable path and the legacy in-VM-dir path count: on old code
		// the waiter legitimately waits on the old path, and the
		// overlap assertion below is what fails.
		waitForWaiterFD(t, cmd.Process.Pid,
			[]string{lifecycleLockPath(name), filepath.Join(vmDir, ".lifecycle.lock")},
			10*time.Second)
	} else {
		time.Sleep(500 * time.Millisecond)
	}

	// Delete + recreate under the held lock: the whole VM directory goes
	// away and comes back while the waiter is blocked on the lock.
	if err := os.RemoveAll(vmDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Release: the waiter acquires, holds 3s, and a NEW entrant must wait
	// for it. On a deleted/recreated lock inode the entrant would lock a
	// fresh inode and overlap the waiter instead of waiting.
	t0 := time.Now()
	unlock1()
	released1 = true
	waitForFile(t, filepath.Join(dir, "B_ACQUIRED"), 10*time.Second)
	unlock2, err := lockLifecycle(name)
	if err != nil {
		t.Fatalf("entrant lock: %v", err)
	}
	defer unlock2()
	waited := time.Since(t0)
	if waited < 1500*time.Millisecond {
		t.Errorf("new entrant acquired after %s across delete/recreate — it overlapped the cross-process waiter (want >= 1.5s of the waiter's 3s hold)", waited)
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("waiter exit: %v (stderr: %s)", err, helperErr.String())
	}
}

func TestLifecycleLock_HomeBlockedFailsLoud(t *testing.T) {
	// A VM home that cannot host the lock (here a file, standing in for
	// permission/I/O failure) must fail the lock loudly. Silently
	// degrading to a process-local lock would discard cross-process
	// exclusion exactly when the filesystem is least trustworthy.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetStateDirs(blocker, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	unlock, err := lockLifecycle("filehome")
	if unlock != nil {
		unlock()
	}
	if err == nil {
		t.Fatal("lock over an unusable VM home must fail loudly, not degrade to a process-local lock")
	}
}
