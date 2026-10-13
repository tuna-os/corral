package qemu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Delete is gated twice: every applicable stop must succeed, and the VM
// must then be proven stopped, before any file is touched. Anything else
// preserves every file and reports the uncertainty.

func TestDelete_RefusesUnknownDirect(t *testing.T) {
	dir := directVM(t, "gate")
	disk := []byte("disk-bytes")
	if err := os.WriteFile(filepath.Join(dir, "disk.qcow2"), disk, 0o644); err != nil {
		t.Fatal(err)
	}
	record := []byte("not-a-record!!!")
	if err := os.WriteFile(filepath.Join(dir, pidFile), record, 0o644); err != nil {
		t.Fatal(err)
	}
	// An unverifiable direct child refuses: the disk and the record are
	// preserved, and the error says so.
	err := Delete("gate")
	if err == nil || !strings.Contains(err.Error(), "not deleted") {
		t.Errorf("delete over an unknown direct record must refuse, got %v", err)
	}
	if got, rerr := os.ReadFile(filepath.Join(dir, "disk.qcow2")); rerr != nil || string(got) != string(disk) {
		t.Errorf("refused delete must preserve the disk (read %q, %v)", got, rerr)
	}
	if got, rerr := os.ReadFile(filepath.Join(dir, pidFile)); rerr != nil || string(got) != string(record) {
		t.Errorf("refused delete must preserve the pid record (read %q, %v)", got, rerr)
	}
}

func TestDelete_RefusesFailedUnitStop(t *testing.T) {
	vmDir, unitDir := systemdVM(t, "gate")
	disk := []byte("disk-bytes")
	if err := os.WriteFile(filepath.Join(vmDir, "disk.qcow2"), disk, 0o644); err != nil {
		t.Fatal(err)
	}
	unit := []byte("[Service]\nExecStart=/usr/bin/qemu -name gate\n")
	unitPath := filepath.Join(unitDir, "corral-gate.service")
	if err := os.WriteFile(unitPath, unit, 0o644); err != nil {
		t.Fatal(err)
	}
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "stop" {
			return []byte("boom\n"), fmt.Errorf("exit status 1")
		}
		return []byte{}, nil
	})
	err := Delete("gate")
	if err == nil || !strings.Contains(err.Error(), "not deleted") || !strings.Contains(err.Error(), "stopping its unit") {
		t.Errorf("delete with a failed unit stop must refuse naming the stop, got %v", err)
	}
	if got, rerr := os.ReadFile(filepath.Join(vmDir, "disk.qcow2")); rerr != nil || string(got) != string(disk) {
		t.Errorf("refused delete must preserve the disk (read %q, %v)", got, rerr)
	}
	if got, rerr := os.ReadFile(unitPath); rerr != nil || string(got) != string(unit) {
		t.Errorf("refused delete must preserve the unit file (read %q, %v)", got, rerr)
	}
}

func TestDelete_RefusesUnretiredUnit(t *testing.T) {
	vmDir, unitDir := systemdVM(t, "gate")
	disk := []byte("disk-bytes")
	if err := os.WriteFile(filepath.Join(vmDir, "disk.qcow2"), disk, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "corral-gate.service"), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "is-active":
			// The stop below "succeeds" but retires nothing.
			return []byte("active\n"), nil
		case "stop", "disable":
			return []byte{}, nil
		default:
			return []byte{}, nil
		}
	})
	err := Delete("gate")
	if err == nil || !strings.Contains(err.Error(), "not deleted") || !strings.Contains(err.Error(), "still running") {
		t.Errorf("delete with an unretired unit must refuse naming the live state, got %v", err)
	}
	if got, rerr := os.ReadFile(filepath.Join(vmDir, "disk.qcow2")); rerr != nil || string(got) != string(disk) {
		t.Errorf("refused delete must preserve the disk (read %q, %v)", got, rerr)
	}
	if _, rerr := os.Stat(filepath.Join(unitDir, "corral-gate.service")); rerr != nil {
		t.Errorf("refused delete must preserve the unit file: %v", rerr)
	}
}

func TestDelete_StopsRunningDirectVM(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	if err := Start("gate"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid, alive := directPID("gate")
	if !alive {
		t.Fatal("expected a live VM before delete")
	}
	// Defensive reaper: if the test fails mid-delete, no sleep may leak.
	t.Cleanup(func() {
		if proc, _ := os.FindProcess(pid); proc != nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	})
	payload := []byte("disk-bytes")
	if err := os.WriteFile(filepath.Join(dir, "disk.qcow2"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	// Without stop binding the live stop inside Delete refuses by
	// design; the removed-state assertions below are bound-only.
	if err := Delete("gate"); !DirectStopBound() {
		requireUnboundStopRefusal(t, err, "delete of a live direct VM")
		killTestPID(t, pid)
		return
	} else if err != nil {
		t.Fatalf("Delete of a stoppable VM: %v", err)
	}
	if Exists("gate") {
		t.Error("VM directory should be gone after delete")
	}
	if proc, _ := os.FindProcess(pid); proc != nil && proc.Signal(syscall.Signal(0)) == nil {
		t.Errorf("old pid %d still alive after delete", pid)
	}
}

func TestDelete_RemovesStoppedVM(t *testing.T) {
	vmDir, unitDir := systemdVM(t, "gate")
	// A direct-only host with a stale unit file on disk: nothing can be
	// running under a manager that does not exist, but the stale file
	// must still go.
	SetUserSystemd(func() bool { return false })
	if err := os.WriteFile(filepath.Join(vmDir, "disk.qcow2"), []byte("disk-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(unitDir, "corral-gate.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Delete("gate"); err != nil {
		t.Fatalf("Delete of a stopped VM: %v", err)
	}
	if Exists("gate") {
		t.Error("VM directory should be gone after delete")
	}
	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Errorf("stale unit file should be gone after delete: %v", err)
	}
}
