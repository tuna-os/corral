package qemu

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tri-state manager knowledge: a manager that answers is available or
// unavailable, and a manager that does not answer (timeout above all) is
// unknown — never silently unavailable. Unknown refuses destruction (no
// delete, no ProvenStopped-true). Old code collapsed unknown to false, so
// Delete skipped the unit block while ProvenStopped reported true on an
// absent direct record, deleting files under a possibly-live unit.

// TestProbeSystemdState_Tristate pins the probe's answer per evidence.
// The bool column is the preserved routing (unknown still routes false);
// the state column is the fix (old code has no unknown).
func TestProbeSystemdState_Tristate(t *testing.T) {
	execMissing := &exec.Error{Name: "systemctl", Err: exec.ErrNotFound}
	timedOut := errors.New("systemctl timed out after 60s (process killed and reaped): signal: killed")
	for _, tc := range []struct {
		name  string
		out   string
		err   error
		want  systemdState
		wantB bool
	}{
		{name: "running", out: "running\n", want: systemdAvailable, wantB: true},
		{name: "degraded with exit error", out: "degraded\n", err: errors.New("exit status 1"), want: systemdAvailable, wantB: true},
		{name: "no bus", out: "Failed to connect to bus: No medium found\n", err: errors.New("exit status 1"), want: systemdUnavailable},
		{name: "missing binary", out: "", err: execMissing, want: systemdUnavailable},
		{name: "timeout is unknown", out: "", err: timedOut, want: systemdUnknown},
		{name: "garbage with error is unknown", out: "???\n", err: errors.New("boom"), want: systemdUnknown},
		{name: "offline is unknown", out: "offline\n", err: errors.New("exit status 1"), want: systemdUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
				return []byte(tc.out), tc.err
			})
			t.Cleanup(func() { SetSystemctl(nil) })
			if got := probeSystemdState(); got != tc.want {
				t.Errorf("probe = %d, want %d", got, tc.want)
			}
			if got := userSystemd(); got != tc.wantB {
				t.Errorf("userSystemd() = %v, want %v (routing preserved)", got, tc.wantB)
			}
		})
	}
}

// unknownManagerFixture stages a stopped VM (disk, stale unit file, no
// pid) under a manager that never answers, with the REAL probe in force
// (no userSystemd pin): the probe reads the stubbed timeout.
func unknownManagerFixture(t *testing.T) (vmDir, unitPath, disk string) {
	t.Helper()
	home := t.TempDir()
	unitDir := t.TempDir()
	SetStateDirs(home, unitDir)
	t.Cleanup(func() { SetStateDirs("", "") })
	vmDir = filepath.Join(home, "gate")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	disk = filepath.Join(vmDir, "disk.qcow2")
	if err := os.WriteFile(disk, []byte("disk-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	unitPath = filepath.Join(unitDir, "corral-gate.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte(""), errors.New("systemctl timed out after 60s (process killed and reaped): signal: killed")
	})
	t.Cleanup(func() { SetSystemctl(nil) })
	return vmDir, unitPath, disk
}

func TestDelete_RefusesUnknownManager(t *testing.T) {
	vmDir, unitPath, disk := unknownManagerFixture(t)
	err := Delete("gate")
	if err == nil || !strings.Contains(err.Error(), "not deleted") || !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("delete under an unanswered manager must refuse naming it, got %v", err)
	}
	if got, rerr := os.ReadFile(disk); rerr != nil || string(got) != "disk-bytes" {
		t.Errorf("refused delete must preserve the disk (read %q, %v)", got, rerr)
	}
	if _, serr := os.Stat(unitPath); serr != nil {
		t.Errorf("refused delete must preserve the unit file: %v", serr)
	}
	if _, serr := os.Stat(vmDir); serr != nil {
		t.Errorf("refused delete must preserve the VM dir: %v", serr)
	}
}

func TestProvenStopped_UnknownManagerFalse(t *testing.T) {
	unknownManagerFixture(t)
	if ProvenStopped("gate") {
		t.Error("ProvenStopped under an unanswered manager must be false, got true")
	}
}

// TestDelete_RefusesManagerTimeoutWithLiveActivation stages mixed
// evidence: the manager probe times out (unknown) while the unit file
// exists and the activation query reports known-live. Destruction must
// still refuse with everything preserved — old code read the timed-out
// probe as "no session", skipped the unit block, and deleted the files
// under the live activation.
func TestDelete_RefusesManagerTimeoutWithLiveActivation(t *testing.T) {
	home := t.TempDir()
	unitDir := t.TempDir()
	SetStateDirs(home, unitDir)
	t.Cleanup(func() { SetStateDirs("", "") })
	vmDir := filepath.Join(home, "gate")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(vmDir, "disk.qcow2")
	if err := os.WriteFile(disk, []byte("disk-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(unitDir, "corral-gate.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	timedOut := errors.New("systemctl timed out after 60s (process killed and reaped): signal: killed")
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "is-system-running" {
			return []byte(""), timedOut
		}
		if len(args) > 0 && args[0] == "is-active" {
			return []byte("active\n"), nil
		}
		return []byte{}, nil
	})
	t.Cleanup(func() { SetSystemctl(nil) })
	if err := Delete("gate"); err == nil || !strings.Contains(err.Error(), "not deleted") {
		t.Errorf("delete under a timed-out manager with a live activation must refuse, got %v", err)
	}
	if ProvenStopped("gate") {
		t.Error("ProvenStopped with a known-live activation must be false, got true")
	}
	if got, rerr := os.ReadFile(disk); rerr != nil || string(got) != "disk-bytes" {
		t.Errorf("refused delete must preserve the disk (read %q, %v)", got, rerr)
	}
	if _, serr := os.Stat(unitPath); serr != nil {
		t.Errorf("refused delete must preserve the unit file: %v", serr)
	}
}

// TestDelete_ProvenAbsentManagerProceeds locks the unavailable arm: a
// manager proven absent (not merely unanswered) still deletes. Old and
// new code agree here; the test guards the probe refactor.
func TestDelete_ProvenAbsentManagerProceeds(t *testing.T) {
	home := t.TempDir()
	unitDir := t.TempDir()
	SetStateDirs(home, unitDir)
	t.Cleanup(func() { SetStateDirs("", "") })
	vmDir := filepath.Join(home, "gate")
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vmDir, "disk.qcow2"), []byte("disk-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	unitPath := filepath.Join(unitDir, "corral-gate.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "is-system-running" {
			return []byte("Failed to connect to bus: No medium found\n"), errors.New("exit status 1")
		}
		return []byte{}, nil
	})
	t.Cleanup(func() { SetSystemctl(nil) })
	if err := Delete("gate"); err != nil {
		t.Fatalf("delete under a proven-absent manager: %v", err)
	}
	if _, serr := os.Stat(vmDir); !os.IsNotExist(serr) {
		t.Errorf("VM dir should be gone after delete: %v", serr)
	}
	if _, serr := os.Stat(unitPath); !os.IsNotExist(serr) {
		t.Errorf("stale unit file should be gone after delete: %v", serr)
	}
}

// TestManagerPin_SeamConsistency locks the single-source pin: bool pins
// prove available/absent identically through both seams, nil restores the
// live probe, and a direct unknown pin reads false without proving
// absent. (New symbols: fails on old code by construction.)
func TestManagerPin_SeamConsistency(t *testing.T) {
	SetUserSystemd(func() bool { return false })
	if !managerProvenAbsent() {
		t.Error("false pin must prove the manager absent")
	}
	if userSystemd() {
		t.Error("false pin must read false")
	}
	SetUserSystemd(func() bool { return true })
	if managerProvenAbsent() {
		t.Error("true pin must not prove the manager absent")
	}
	if !userSystemd() {
		t.Error("true pin must read true")
	}
	SetUserSystemd(nil)
	if systemdStatePin != nil {
		t.Error("nil restore must clear the state pin")
	}
	s := systemdUnknown
	systemdStatePin = &s
	t.Cleanup(func() { systemdStatePin = nil })
	if userSystemd() {
		t.Error("unknown pin must read false")
	}
	if managerProvenAbsent() {
		t.Error("unknown pin must not prove the manager absent")
	}
}
