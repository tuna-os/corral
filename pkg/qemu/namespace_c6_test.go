package qemu

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/types"
)

// The locks namespace is reserved: <VMHome>/locks holds one permanent
// flock per VM name, so a VM named `locks` would make Delete unlink every
// live lock and Create scribble VM state into the lock namespace. Old code
// accepted the name (it passes the character rules), so Delete(locks)
// removed all flocks and waiters overlapped on fresh inodes.

// TestValidateVMName_LocksReserved pins the reservation and its bounds:
// exactly `locks` refuses; near-miss names stay valid.
func TestValidateVMName_LocksReserved(t *testing.T) {
	if err := ValidateVMName("locks"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("name locks must refuse naming the reservation, got %v", err)
	}
	for _, ok := range []string{"gate", "locks-old", "mylocks", "LOCKS"} {
		if err := ValidateVMName(ok); err != nil {
			t.Errorf("name %q must stay valid, got %v", ok, err)
		}
	}
}

// TestLocksNamespace_InvisibleToListAndPorts plants hostile metadata
// inside the lock namespace itself: List must not report a `locks` VM
// and its ports must not be squatted. Old code had no explicit skip —
// the plant listed (harmless for the flocks, which Delete's refusal
// protects, but a namespace violation all the same).
func TestLocksNamespace_InvisibleToListAndPorts(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	locksDir := filepath.Join(home, "locks")
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plant := `{"name":"locks","cpu":1,"memory":"1G","disk_size":"1G","vnc_port":5999,"ssh_port":2299}`
	if err := os.WriteFile(filepath.Join(locksDir, "metadata.json"), []byte(plant), 0o644); err != nil {
		t.Fatal(err)
	}
	vms, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, vm := range vms {
		if vm.Name == "locks" {
			t.Errorf("List reported the locks namespace as a VM: %+v", vm)
		}
	}
	if usedPorts()[5999] || usedPorts()[2299] {
		t.Errorf("planted locks metadata squatted ports: %v", usedPorts())
	}
}

func TestCreate_LocksNameRefused(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	if err := Create(types.CreateOpts{Name: "locks"}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("create of locks must refuse naming the reservation, got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(home, "locks")); !os.IsNotExist(serr) {
		t.Errorf("refused create must not touch the locks namespace: %v", serr)
	}
}

// TestDeleteLocksName_RefusedAndFlocksSurvive is the cross-process
// negative: with a waiter blocked on a victim's flock, Delete(locks) must
// refuse without touching the lock files, and a later entrant must still
// serialize behind the waiter (same inode). On old code the delete
// succeeds, the locks vanish, and the entrant overlaps the waiter.
func TestDeleteLocksName_RefusedAndFlocksSurvive(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin":
	default:
		t.Skipf("cross-process flock is Unix-only (no exclusion on %s)", runtime.GOOS)
	}
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	SetUserSystemd(func() bool { return false })
	t.Cleanup(func() { SetUserSystemd(nil) })
	name := "gate"
	vmDir := filepath.Join(home, name)
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	// The parent holds the victim's lifecycle lock.
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

	// A different-process waiter blocks on the lock (the lifecycle
	// helper re-execs the test binary for a genuine separate process).
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
		waitForWaiterFD(t, cmd.Process.Pid,
			[]string{lifecycleLockPath(name), filepath.Join(vmDir, ".lifecycle.lock")},
			10*time.Second)
	} else {
		time.Sleep(500 * time.Millisecond)
	}

	// THE ATTEMPT: deleting the locks namespace must refuse, and the
	// live lock file must still be in place afterwards.
	if err := Delete("locks"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("delete of locks must refuse naming the reservation, got %v", err)
	}
	if _, serr := os.Stat(lifecycleLockPath(name)); serr != nil {
		t.Errorf("refused delete must leave live lock files in place: %v", serr)
	}

	// Release: the waiter acquires, holds 3s, and a NEW entrant must wait
	// for it — proving the flock inode survived the attempt.
	t0 := time.Now()
	unlock1()
	released1 = true
	waitForFile(t, filepath.Join(dir, "B_ACQUIRED"), 10*time.Second)
	unlock2, err := lockLifecycle(name)
	if err != nil {
		t.Fatalf("entrant lock: %v", err)
	}
	defer unlock2()
	if waited := time.Since(t0); waited < 1500*time.Millisecond {
		t.Errorf("new entrant acquired after %s across the delete attempt — it overlapped the waiter (want >= 1.5s of the 3s hold)", waited)
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("waiter exit: %v (stderr: %s)", err, helperErr.String())
	}
}
