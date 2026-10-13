package qemu

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Finding 3: admission and start are one bound definition transaction, and a
// backend switch never double-starts.
//
// Old-fails/new-passes direction per test, stated honestly:
//   - Token/admission tests reference new symbols (AdmitDefinition,
//     StartOwnedTokenAdmitted, DefinitionToken.Verify), so they fail on old
//     code by construction. Where the same staging is expressible with old
//     symbols, the comment states what old code does at runtime.
//   - Backend-switch tests use only old-existing symbols and fail on old
//     code at runtime: old code starts the second QEMU where new code
//     refuses.

// admitGate admits the "gate" fixture with a trivially satisfied manifest
// binding: the definition-identity half under test, without the launcher's
// manifest dimension (which launch_admitted_c5_test.go covers).
func admitGate(t *testing.T) DefinitionToken {
	t.Helper()
	tok, err := AdmitDefinition("gate", func([]byte) error { return nil })
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return tok
}

// liveSleep starts a real sleep process and records it as this VM's child,
// staging pidOwned without going through startDirectLocked. Cleanup kills
// and reaps it, so no sleep leaks even on failure.
func liveSleep(t *testing.T, dir string) int {
	t.Helper()
	sleep := sleepBin(t)
	cmd := exec.Command(sleep, "120")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep stand-in: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		if proc, _ := os.FindProcess(pid); proc != nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
		_ = cmd.Wait()
	})
	_, raw, err := buildPIDRecord(pid, sleep)
	if err != nil {
		t.Fatalf("recording the sleep stand-in: %v", err)
	}
	if err := writePIDRecord(dir, raw); err != nil {
		t.Fatalf("writing the pid record: %v", err)
	}
	if _, _, st := directState("gate"); st != pidOwned {
		t.Fatalf("staged child state = %s, want owned", st)
	}
	return pid
}

func TestAdmittedToken_RoundTrip(t *testing.T) {
	f := newVFix(t)
	_ = f
	tok := admitGate(t)
	if tok.MetaSHA == "" || tok.CmdSHA == "" || tok.UnitSHA == "" || !tok.UnitPresent {
		t.Fatalf("admission left an incomplete token: %+v", tok)
	}
	if err := tok.Verify("gate"); err != nil {
		t.Errorf("unchanged definition must verify: %v", err)
	}
	// A zero token admits nothing, not even an unchanged definition.
	if err := (DefinitionToken{}).Verify("gate"); err == nil {
		t.Error("zero token must not verify")
	}
}

func TestAdmittedStart_RefusesReplacedMetadata(t *testing.T) {
	f := newVFix(t)
	tok := admitGate(t)
	// Another legitimate force-create lands between admission and start.
	f.meta["memory"] = "8G"
	f.rewrite()
	_, started, err := StartOwnedTokenAdmitted("gate", tok)
	if started {
		t.Error("start over replaced metadata must report started=false")
	}
	if err == nil || !strings.Contains(err.Error(), "changed since admission") || !strings.Contains(err.Error(), "metadata") {
		t.Errorf("replaced metadata must refuse naming it, got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(f.dir, pidFile)); !os.IsNotExist(serr) {
		t.Errorf("refused start must leave no pid record: %v", serr)
	}
	// Runtime direction note: old code has no admitted entry; its plain
	// StartOwnedToken over this replacement proceeds to launch it instead
	// of refusing.
}

func TestAdmittedStart_RefusesReplacedCommand(t *testing.T) {
	f := newVFix(t)
	tok := admitGate(t)
	// The recorded argv changes while metadata and unit stay byte-identical.
	f.setFlag("-m", "8G")
	cmdData, err := json.Marshal(map[string]any{"binary": f.binary, "args": f.args})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, commandFile), cmdData, 0o644); err != nil {
		t.Fatal(err)
	}
	_, started, err := StartOwnedTokenAdmitted("gate", tok)
	if started {
		t.Error("start over a replaced command must report started=false")
	}
	if err == nil || !strings.Contains(err.Error(), "changed since admission") || !strings.Contains(err.Error(), "recorded QEMU command") {
		t.Errorf("replaced command must refuse naming it, got %v", err)
	}
}

func TestAdmittedStart_RefusesReplacedUnit(t *testing.T) {
	f := newVFix(t)
	tok := admitGate(t)
	unitPath := filepath.Join(f.unitDir, "corral-gate.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, started, err := StartOwnedTokenAdmitted("gate", tok)
	if started {
		t.Error("start over a replaced unit must report started=false")
	}
	if err == nil || !strings.Contains(err.Error(), "changed since admission") || !strings.Contains(err.Error(), "systemd unit") {
		t.Errorf("replaced unit must refuse naming it, got %v", err)
	}
}

func TestAdmittedStart_RefusesUnitPresenceFlip(t *testing.T) {
	// Present at admission, removed before start.
	f := newVFix(t)
	tok := admitGate(t)
	if err := os.Remove(filepath.Join(f.unitDir, "corral-gate.service")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := StartOwnedTokenAdmitted("gate", tok); err == nil || !strings.Contains(err.Error(), "removed after admission") {
		t.Errorf("removed unit must refuse naming it, got %v", err)
	}
}

func TestAdmittedStart_RefusesUnitAppeared(t *testing.T) {
	// Absent at admission (direct-only VM), present before start.
	f := newVFix(t)
	f.noUnit = true
	f.rewrite()
	tok := admitGate(t)
	if tok.UnitPresent {
		t.Fatalf("admission over no unit file must record absent, got %+v", tok)
	}
	unit := "[Service]\nExecStart=" + f.binary + " " + strings.Join(f.args, " ") + "\n"
	if err := os.WriteFile(filepath.Join(f.unitDir, "corral-gate.service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := StartOwnedTokenAdmitted("gate", tok); err == nil || !strings.Contains(err.Error(), "appeared after admission") {
		t.Errorf("appeared unit must refuse naming it, got %v", err)
	}
}

func TestAdmittedStart_ReusesRunningDefinition(t *testing.T) {
	// Positive: the admitted definition, already running under another
	// actor, is reused — the token pins the definition, not liveness.
	f := newVFix(t)
	sleepPID := liveSleep(t, f.dir)
	tok := admitGate(t)
	stok, started, err := StartOwnedTokenAdmitted("gate", tok)
	if err != nil {
		t.Fatalf("admitted reuse of the running definition: %v", err)
	}
	if started {
		t.Error("reuse of a running VM must report started=false")
	}
	if stok.Resolved {
		t.Error("reuse must leave the token unresolved (nothing started here)")
	}
	if pid, alive := directPID("gate"); !alive || pid != sleepPID {
		t.Errorf("reused child disturbed: pid %d alive %v, want pid %d alive", pid, alive, sleepPID)
	}
}

func TestAdmittedStart_RefusesReplacementWhileRunning(t *testing.T) {
	// The already-running reuse path compares too: a replacement that is
	// already running refuses instead of being silently reused.
	f := newVFix(t)
	sleepPID := liveSleep(t, f.dir)
	tok := admitGate(t)
	f.meta["memory"] = "8G"
	f.rewrite()
	_, started, err := StartOwnedTokenAdmitted("gate", tok)
	if started {
		t.Error("start over a running replacement must report started=false")
	}
	if err == nil || !strings.Contains(err.Error(), "changed since admission") {
		t.Errorf("running replacement must refuse naming the change, got %v", err)
	}
	if pid, alive := directPID("gate"); !alive || pid != sleepPID {
		t.Errorf("refused start disturbed the running child: pid %d alive %v, want pid %d alive", pid, alive, sleepPID)
	}
	// Runtime direction note: old code's plain StartOwnedToken over this
	// staging returns started=false, nil — silent reuse of the
	// replacement — so the refusal assertion fails there at runtime.
}

func TestAdmitDefinition_BindFailureRefuses(t *testing.T) {
	f := newVFix(t)
	_ = f
	bindErr := errors.New("manifest wants another image")
	tok, err := AdmitDefinition("gate", func([]byte) error { return bindErr })
	if err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Errorf("failed binding must refuse admission, got %v", err)
	}
	if tok != (DefinitionToken{}) {
		t.Errorf("failed admission must return no token, got %+v", tok)
	}
}

func TestAdmitDefinition_NilBindRefuses(t *testing.T) {
	f := newVFix(t)
	_ = f
	if _, err := AdmitDefinition("gate", nil); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Errorf("nil binding must refuse admission, got %v", err)
	}
}

func TestAdmitDefinition_InvalidDefinitionRefuses(t *testing.T) {
	f := newVFix(t)
	// Argv diverges from the admitted metadata: validity, not just
	// continuity, is proven at admission.
	f.setFlag("-m", "8G")
	f.rewrite()
	if _, err := AdmitDefinition("gate", func([]byte) error { return nil }); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Errorf("invalid definition must refuse admission, got %v", err)
	}
}

// TestSystemdStart_RefusesLiveDirectOwner: a user manager becomes available
// while a direct QEMU holds the disk. The systemd branch must refuse —
// never stack a unit on the live direct child. Uses only old-existing
// symbols: old code runs systemctl start (fails the starts==0 assertion
// and reports started=true) where new code refuses.
func TestSystemdStart_RefusesLiveDirectOwner(t *testing.T) {
	f := newVFix(t)
	SetUserSystemd(func() bool { return true })
	sleepPID := liveSleep(t, f.dir)
	var starts int
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "is-active":
			return []byte("inactive\n"), errors.New("exit status 3")
		case "start":
			starts++
			return []byte{}, nil
		case "show":
			return []byte("MainPID=4242\nActiveEnterTimestamp=Tue 2026-10-13 00:00:00 UTC\n"), nil
		default:
			return []byte{}, nil
		}
	})
	t.Cleanup(func() { SetSystemctl(nil) })
	_, started, err := StartOwnedToken("gate")
	if started {
		t.Error("systemd start beside a live direct child must report started=false")
	}
	if err == nil || !strings.Contains(err.Error(), "direct QEMU child") || !strings.Contains(err.Error(), "second QEMU") {
		t.Errorf("live direct owner must refuse the unit start, got %v", err)
	}
	if starts != 0 {
		t.Errorf("refused start ran systemctl start %d times, want 0", starts)
	}
	if pid, alive := directPID("gate"); !alive || pid != sleepPID {
		t.Errorf("refused start disturbed the direct child: pid %d alive %v, want pid %d alive", pid, alive, sleepPID)
	}
}

// TestSystemdStart_RefusesUnknownDirect: an unverifiable direct record
// refuses the unit start with the same force as a proven child. Uses only
// old-existing symbols: old code starts the unit (fails starts==0).
func TestSystemdStart_RefusesUnknownDirect(t *testing.T) {
	f := newVFix(t)
	SetUserSystemd(func() bool { return true })
	if err := os.WriteFile(filepath.Join(f.dir, pidFile), []byte("not-a-record!!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	var starts int
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "is-active":
			return []byte("inactive\n"), errors.New("exit status 3")
		case "start":
			starts++
			return []byte{}, nil
		case "show":
			return []byte("MainPID=4242\nActiveEnterTimestamp=Tue 2026-10-13 00:00:00 UTC\n"), nil
		default:
			return []byte{}, nil
		}
	})
	t.Cleanup(func() { SetSystemctl(nil) })
	_, started, err := StartOwnedToken("gate")
	if started {
		t.Error("systemd start beside an unknown direct record must report started=false")
	}
	if err == nil || !strings.Contains(err.Error(), "state unknown") {
		t.Errorf("unknown direct record must refuse naming it, got %v", err)
	}
	if starts != 0 {
		t.Errorf("refused start ran systemctl start %d times, want 0", starts)
	}
}

// TestDirectStart_RefusesActiveUnit: probe disagreement — no user session
// detected, but the manager positively reports the unit live. The direct
// branch must refuse, never launch beside it. Uses only old-existing
// symbols: old code launches a real child and writes its pid record (fails
// the refusal and no-pid-file assertions), so the test carries a defensive
// reaper for the old world.
func TestDirectStart_RefusesActiveUnit(t *testing.T) {
	dir, unitDir := systemdVM(t, "gate")
	SetUserSystemd(func() bool { return false })
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "corral-gate.service"), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "is-active" {
			return []byte("active\n"), nil
		}
		return []byte{}, nil
	})
	// Defensive reaper for the old world (which launches a real child).
	t.Cleanup(func() {
		if recorded, err := readPIDFile("gate"); err == nil && recorded.PID > 0 {
			if proc, _ := os.FindProcess(recorded.PID); proc != nil {
				_ = proc.Signal(syscall.SIGKILL)
			}
		}
	})
	if err := Start("gate"); err == nil || !strings.Contains(err.Error(), "second QEMU") || !strings.Contains(err.Error(), "systemd unit") {
		t.Errorf("direct start beside an active unit must refuse naming it, got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, pidFile)); !os.IsNotExist(serr) {
		t.Errorf("refused start must leave no pid record: %v", serr)
	}
}

// TestDirectStart_ProceedsWhenUnitInactive locks the negative control: a
// unit file on disk with the unit provably stopped still starts directly.
// Passes on old code too (old code never asked); it locks no-false-refusal.
func TestDirectStart_ProceedsWhenUnitInactive(t *testing.T) {
	dir, unitDir := systemdVM(t, "gate")
	SetUserSystemd(func() bool { return false })
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "corral-gate.service"), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "is-active" {
			return []byte("inactive\n"), errors.New("exit status 3")
		}
		return []byte{}, nil
	})
	t.Cleanup(func() { _ = Stop("gate") })
	if err := Start("gate"); err != nil {
		t.Fatalf("direct start beside a stopped unit: %v", err)
	}
	if _, alive := directPID("gate"); !alive {
		t.Error("direct start beside a stopped unit left no live child")
	}
}

// TestDirectStart_SkipsUnitQueryWithoutUnitFile locks the gating: with no
// unit file there is nothing to ask, so no is-active query runs even under
// a hostile stub. Passes on old code too; it locks the no-query property.
func TestDirectStart_SkipsUnitQueryWithoutUnitFile(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	var queried []string
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		queried = append(queried, args[0])
		if args[0] == "is-active" {
			return []byte("active\n"), nil
		}
		return []byte{}, nil
	})
	t.Cleanup(func() {
		SetSystemctl(nil)
		_ = Stop("gate")
	})
	if err := Start("gate"); err != nil {
		t.Fatalf("direct start without a unit file: %v", err)
	}
	if _, alive := directPID("gate"); !alive {
		t.Error("direct start without a unit file left no live child")
	}
	for _, q := range queried {
		if q == "is-active" {
			t.Errorf("start without a unit file queried is-active %d time(s), want 0", len(queried))
		}
	}
}
