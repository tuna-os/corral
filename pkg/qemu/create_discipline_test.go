package qemu

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

func TestCreate_ConcurrentSameName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	fakeQemuBin(t)

	// Two concurrent creates for one name: exactly one wins, the other gets
	// an honest already-exists error — never interleaved state.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = Create(types.CreateOpts{Name: "racevm", CPU: 1, Mem: "1G", Disk: "5G"})
		}(i)
	}
	wg.Wait()
	var ok, refused int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case strings.Contains(err.Error(), "already exists"):
			refused++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Errorf("concurrent same-name create: %d won, %d refused — want exactly one of each", ok, refused)
	}
	if _, err := os.Stat(filepath.Join(VMHome(), "racevm", "metadata.json")); err != nil {
		t.Errorf("winner left no metadata: %v", err)
	}
}

func TestCreate_ForceStopsRunningVM(t *testing.T) {
	dir := directVM(t, "gate")
	fakeQemuBin(t)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	if err := writeLaunchCommand(dir, sleep, []string{"120"}); err != nil {
		t.Fatal(err)
	}
	if err := Start("gate"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	oldPID, alive := directPID("gate")
	if !alive {
		t.Fatal("expected a live VM before --force")
	}
	// ExistingDisk avoids conversion; the force discipline is what is
	// under test, not qemu-img.
	payload := []byte("bootc-built-disk-content")
	if err := os.WriteFile(filepath.Join(dir, "disk.qcow2"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Create(types.CreateOpts{Name: "gate", Force: true, ExistingDisk: true}); err != nil {
		t.Fatalf("Force create over a running VM: %v", err)
	}
	// The old QEMU is dead and the prepared disk survived.
	if proc, _ := os.FindProcess(oldPID); proc != nil && proc.Signal(syscall.Signal(0)) == nil {
		_ = proc.Signal(syscall.SIGKILL)
		t.Errorf("old pid %d still alive after --force recreation", oldPID)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "disk.qcow2"))
	if string(got) != string(payload) {
		t.Errorf("prepared disk was modified: got %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "metadata.json")); err != nil {
		t.Errorf("recreation left no metadata: %v", err)
	}
}

func TestCreate_ForceRefusesUnstoppableVM(t *testing.T) {
	dir := directVM(t, "gate")
	fakeQemuBin(t)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	if err := writeLaunchCommand(dir, sleep, []string{"120"}); err != nil {
		t.Fatal(err)
	}
	if err := Start("gate"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Identity becomes unverifiable: the VM cannot be proven stopped, so
	// --force must refuse rather than recreate underneath a live QEMU.
	pinProbes(t, "", "", os.ErrNotExist, os.ErrNotExist)
	defer func() {
		origExe, origStart := nativeExeID, nativeStartID
		procExe, procStartID = origExe, origStart
		_ = Stop("gate")
	}()

	err = Create(types.CreateOpts{Name: "gate", Force: true, ExistingDisk: true})
	if err == nil || !strings.Contains(err.Error(), "could not be stopped") {
		t.Errorf("Force over an unverifiable VM must refuse, got %v", err)
	}
	// The refusal must not disturb the VM: its process is still alive
	// (signal 0, independent of the stubbed identity probes) and its pid
	// file is kept.
	recorded, rerr := readPIDFile("gate")
	if rerr != nil {
		t.Fatalf("refused force must keep the pid file: %v", rerr)
	}
	if proc, _ := os.FindProcess(recorded.PID); proc == nil || proc.Signal(syscall.Signal(0)) != nil {
		t.Error("refused force must leave the running process alive")
	}
}
