package qemu

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Start ownership is captured at launch: the token pins what the launch
// wrote and verified, never a later re-read — and a launch whose record
// cannot be verified fails with its child reaped, never started=true
// with an unverifiable record.

// TestStartOwnedToken_VerifyFailureReapsChild stages an external record
// replacement during the launch settle window: the pid file is valid when
// written, then overwritten with garbage before verification. The launch
// must fail with its child observably reaped — not report started=true
// for a child nobody can prove or stop.
func TestStartOwnedToken_VerifyFailureReapsChild(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(dir, pidFile)

	// The overwriter simulates a non-lifecycle writer (hand edit, foreign
	// tool — anything outside the lifecycle lock): it waits for the valid
	// record, learns the real pid from it, then replaces it with garbage
	// long before the 750ms settle ends and verification runs.
	type overwrite struct {
		pid int
		err error
	}
	done := make(chan overwrite, 1)
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for {
			recorded, err := readPIDFile("gate")
			if err == nil && recorded.PID > 0 {
				if werr := os.WriteFile(pidPath, []byte("replaced-record"), 0o644); werr != nil {
					done <- overwrite{err: werr}
					return
				}
				done <- overwrite{pid: recorded.PID}
				return
			}
			if !time.Now().Before(deadline) {
				done <- overwrite{err: err}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	tok, started, err := StartOwnedToken("gate")
	ow := <-done
	if ow.err != nil {
		t.Fatalf("overwriter never saw a valid record: %v", ow.err)
	}
	// Defensive reaper for the old world (which orphans the child): the
	// overwriter learned its pid.
	t.Cleanup(func() {
		if proc, _ := os.FindProcess(ow.pid); proc != nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	})
	if started {
		t.Errorf("launched-but-unverifiable start must report started=false, got started=true with token %+v", tok)
	}
	if err == nil || !strings.Contains(err.Error(), "reaped") {
		t.Errorf("unverifiable launch must fail naming the observed reaping, got %v", err)
	}
	// Only our exact attempted bytes may go after the observed
	// settlement: the foreign replacement belongs to someone else and
	// stays. (Review-mandated update: C4 removed it.)
	if got, rerr := os.ReadFile(pidPath); rerr != nil || string(got) != "replaced-record" {
		t.Errorf("settled launch must preserve the foreign replacement (read %q, %v)", got, rerr)
	}
	if proc, _ := os.FindProcess(ow.pid); proc != nil && proc.Signal(syscall.Signal(0)) == nil {
		t.Errorf("launched child %d still alive after unverifiable launch — it was orphaned, not reaped", ow.pid)
	}
}

// TestStartOwnedToken_BlackholeRecordRefusesBeforeLaunch pins the
// fail-closed edge: a pid path that swallows writes (a symlink to
// /dev/null reports success and keeps nothing) refuses BEFORE launching,
// so no child is ever started behind an unrecordable pid. Both old and
// new code refuse here; the test locks the property across the launch
// refactor.
func TestStartOwnedToken_BlackholeRecordRefusesBeforeLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privilege on windows")
	}
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(dir, pidFile)
	if err := os.Symlink(os.DevNull, pidPath); err != nil {
		t.Fatal(err)
	}
	_, started, err := StartOwnedToken("gate")
	if started {
		t.Error("start over an unreadable pid record must report started=false")
	}
	if err == nil || !strings.Contains(err.Error(), "unreadable pid file") {
		t.Errorf("start over an unreadable pid record must refuse naming it, got %v", err)
	}
	if st, serr := os.Lstat(pidPath); serr != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("refused start must leave the pid path untouched: %v", serr)
	}
}

// TestStartOwnedToken_UnverifiableActivationReportsUncertainty: a systemd
// start whose activation identity cannot be read reports started=true
// WITH an error and an unresolved token — never a silent success that
// later phases would treat as a clean reusable admission.
func TestStartOwnedToken_UnverifiableActivationReportsUncertainty(t *testing.T) {
	_, unitDir := systemdVM(t, "gate")
	if err := os.WriteFile(filepath.Join(unitDir, "corral-gate.service"), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "show" {
			// Activation unreadable: no MainPID, no timestamp.
			return []byte("MainPID=0\nActiveEnterTimestamp=n/a\n"), nil
		}
		return []byte{}, nil
	})
	tok, started, err := StartOwnedToken("gate")
	if !started {
		t.Error("unit start reported started=false, want started=true with uncertainty")
	}
	if tok.Resolved {
		t.Error("unverifiable activation must leave the token unresolved")
	}
	if err == nil || !strings.Contains(err.Error(), "activation cannot be verified") {
		t.Errorf("unverifiable activation must fail naming it, got %v", err)
	}
}
