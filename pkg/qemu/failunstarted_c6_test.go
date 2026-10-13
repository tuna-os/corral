package qemu

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Unobserved-retirement honesty: when a launched child cannot be verified
// AND its exit cannot be observed, the start keeps the record and reports
// started=true with the uncertainty — never started=false over a possibly
// live child with the evidence removed. Old code removed the pid file and
// reported started=false in exactly that case. killAndReap is stubbed to
// stage the unobservable exit (a real SIGKILL always lands on test
// children); the staged children stay alive and are retired by hand.

// stageUnobservedRetirement replaces killAndReap with a stub reporting an
// unobserved exit, restoring the real one on cleanup.
func stageUnobservedRetirement(t *testing.T) {
	t.Helper()
	orig := killAndReap
	killAndReap = func(_ *os.Process, _ <-chan error, _ time.Duration) error {
		return errors.New("staged retirement: exit unobserved")
	}
	t.Cleanup(func() { killAndReap = orig })
}

func TestStartOwnedToken_BuildFailureUncertaintyRetains(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	// The start identity cannot be read: the record cannot be built.
	origStartID := procStartID
	procStartID = func(pid int) (string, error) { return "", errors.New("no start token") }
	stageUnobservedRetirement(t)
	tok, started, err := StartOwnedToken("gate")
	procStartID = origStartID
	if !started {
		t.Errorf("launched-but-unverified start with unobserved exit must report started=true, got started=false (err=%v)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "exit unobserved") || !strings.Contains(err.Error(), "may still be running") {
		t.Errorf("unobserved retirement must fail naming the uncertainty, got %v", err)
	}
	if !tok.Resolved || tok.PID <= 0 {
		t.Fatalf("uncertain start must return a resolved token for the retained record, got %+v", tok)
	}
	// The child is really alive (the stub retired nothing): it must be
	// retired by hand, and everything below must treat it as unknown.
	t.Cleanup(func() { killTestPID(t, tok.PID) })
	raw, rerr := os.ReadFile(filepath.Join(dir, pidFile))
	if rerr != nil {
		t.Fatalf("uncertain start must retain a pid record, got %v", rerr)
	}
	if !strings.Contains(string(raw), `"pid"`) {
		t.Errorf("retained record must name the possibly-live pid, got %q", raw)
	}
	if _, _, st := directState("gate"); st != pidUnknown {
		t.Errorf("retained uncertainty must read unknown, got %s", st)
	}
	if serr := Stop("gate"); serr == nil {
		t.Error("stop over an uncertain record must refuse, reported success")
	} else if !strings.Contains(serr.Error(), "cannot be proven") && !strings.Contains(serr.Error(), "cannot bind") {
		t.Errorf("uncertain stop must refuse naming the unverified process, got %v", serr)
	}
}

func TestStartOwnedToken_VerifyFailureUncertaintyPreservesForeign(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	pidPath := filepath.Join(dir, pidFile)
	foreign := []byte("foreign-record")
	// A non-lifecycle writer replaces the valid record during the settle
	// window, before verification runs (same staging as the reaped-child
	// test, but the retirement here is unobservable).
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
				if werr := os.WriteFile(pidPath, foreign, 0o644); werr != nil {
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
	stageUnobservedRetirement(t)
	tok, started, err := StartOwnedToken("gate")
	ow := <-done
	if ow.err != nil {
		t.Fatalf("overwriter never saw a valid record: %v", ow.err)
	}
	t.Cleanup(func() { killTestPID(t, ow.pid) })
	if !started {
		t.Errorf("launched-but-unverified start with unobserved exit must report started=true, got started=false (err=%v)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "verifying the QEMU pid record") || !strings.Contains(err.Error(), "exit unobserved") {
		t.Errorf("unobserved retirement must fail naming verification and the uncertainty, got %v", err)
	}
	if !tok.Resolved || tok.PID != ow.pid {
		t.Errorf("uncertain start must return the launched pid in its token, got %+v (want pid %d)", tok, ow.pid)
	}
	// The foreign record belongs to someone else: preserved, never
	// removed, never overwritten with our attempt.
	if got, rerr := os.ReadFile(pidPath); rerr != nil || string(got) != string(foreign) {
		t.Errorf("uncertain start must preserve the foreign record (read %q, %v)", got, rerr)
	}
	// And the returned token disciplines cleanup: the disk no longer
	// matches our attempt, so an owned stop refuses loudly.
	if serr := StopOwned("gate", tok); serr == nil || !strings.Contains(serr.Error(), "ownership changed") {
		t.Errorf("owned stop over a replaced record must refuse naming the ownership change, got %v", serr)
	}
}

func TestStartOwnedToken_WriteFailureUncertaintyRetains(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	// The pid path is unwritable (a directory): the built record cannot
	// be written, and there is nothing on disk to verify.
	pidPath := filepath.Join(dir, pidFile)
	if err := os.Mkdir(pidPath, 0o755); err != nil {
		t.Fatal(err)
	}
	stageUnobservedRetirement(t)
	tok, started, err := StartOwnedToken("gate")
	if !started {
		t.Errorf("launched-but-unrecorded start with unobserved exit must report started=true, got started=false (err=%v)", err)
	}
	if err == nil || !strings.Contains(err.Error(), "recording the QEMU pid") || !strings.Contains(err.Error(), "exit unobserved") {
		t.Errorf("unobserved retirement must fail naming the recording stage and the uncertainty, got %v", err)
	}
	if !tok.Resolved || tok.PID <= 0 || len(tok.Raw) == 0 {
		t.Fatalf("uncertain start must return a resolved token carrying the attempt, got %+v", tok)
	}
	t.Cleanup(func() { killTestPID(t, tok.PID) })
	// The unwritable path is left exactly as found: no clobber, no
	// removal of something that was never ours to remove.
	if st, serr := os.Stat(pidPath); serr != nil || !st.IsDir() {
		t.Errorf("uncertain start must leave the unwritable pid path untouched: %v", serr)
	}
	if _, _, st := directState("gate"); st != pidUnknown {
		t.Errorf("unrecorded uncertainty must read unknown, got %s", st)
	}
}
