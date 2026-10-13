package qemu

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// Lifetime handles are acquired BEFORE verification and retained across
// the escalation; a handle that cannot be acquired refuses the stop.

type orderRecorder struct {
	calls []string
}

func (o *orderRecorder) note(s string) {
	o.calls = append(o.calls, s)
}

func indexOfCall(calls []string, want string) int {
	for i, c := range calls {
		if c == want {
			return i
		}
	}
	return -1
}

type fakeHandle struct {
	signals []os.Signal
}

func (h *fakeHandle) Signal(s os.Signal) error {
	h.signals = append(h.signals, s)
	return nil
}

func (h *fakeHandle) Release() {}

func TestStopDirect_AcquiresHandleBeforeVerifying(t *testing.T) {
	dir := directVM(t, "gate")
	// A valid record for a dead pid, with probes pinned to claim owned:
	// classification must still happen strictly after acquisition.
	const deadPID = 1<<30 - 7
	record := []byte(fmt.Sprintf(`{"version":2,"pid":%d,"binary":"/usr/bin/qemu","start":"s1"}`, deadPID))
	if err := os.WriteFile(pidPathForTest(dir), record, 0o644); err != nil {
		t.Fatal(err)
	}
	var rec orderRecorder
	fake := &fakeHandle{}
	origAcquire := procAcquire
	procAcquire = func(pid int) (lifetimeHandle, error) {
		rec.note("acquire")
		if pid != deadPID {
			t.Errorf("acquire pid = %d, want the recorded %d", pid, deadPID)
		}
		return fake, nil
	}
	t.Cleanup(func() { procAcquire = origAcquire })
	origSignal, origExe, origStart := procSignal0, procExe, procStartID
	procSignal0 = func(pid int) error { rec.note("probe"); return nil }
	procExe = func(pid int) (string, error) { rec.note("probe"); return "exe:/usr/bin/qemu", nil }
	procStartID = func(pid int) (string, error) { rec.note("probe"); return "s1", nil }
	t.Cleanup(func() { procSignal0, procExe, procStartID = origSignal, origExe, origStart })
	shrinkEscalationWaits(t)

	err := stopDirectLocked("gate")
	// The flow must run to the end (pinned owned throughout, signals
	// swallowed by the fake): "still alive" proves the full escalation
	// ran through the acquired handle.
	if err == nil || !strings.Contains(err.Error(), "still alive") {
		t.Fatalf("expected the escalation to run through the handle, got %v", err)
	}
	ai := indexOfCall(rec.calls, "acquire")
	if ai < 0 {
		t.Fatal("stop never acquired a lifetime handle")
	}
	for i, c := range rec.calls {
		if c == "probe" && i < ai {
			t.Fatalf("verification probe ran before handle acquisition: %v", rec.calls)
		}
	}
	acquires := 0
	for _, c := range rec.calls {
		if c == "acquire" {
			acquires++
		}
	}
	if acquires != 1 {
		t.Errorf("acquired %d handles, want exactly 1 retained across the escalation", acquires)
	}
	if len(fake.signals) < 2 {
		t.Errorf("escalation sent %d signals through the handle, want TERM then KILL", len(fake.signals))
	}
}

func TestStopDirect_RefusesWhenHandleCannotBeBound(t *testing.T) {
	dir := directVM(t, "gate")
	record := []byte(fmt.Sprintf(`{"version":2,"pid":%d,"binary":"/usr/bin/qemu","start":"s1"}`, 1<<30-7))
	if err := os.WriteFile(pidPathForTest(dir), record, 0o644); err != nil {
		t.Fatal(err)
	}
	pinSignal(t, nil)
	pinProbes(t, "exe:/usr/bin/qemu", "s1", nil, nil)
	origAcquire := procAcquire
	procAcquire = func(pid int) (lifetimeHandle, error) {
		return nil, fmt.Errorf("pidfd blocked by policy")
	}
	t.Cleanup(func() { procAcquire = origAcquire })

	err := stopDirectLocked("gate")
	if err == nil || !strings.Contains(err.Error(), "cannot bind a signal target") {
		t.Errorf("unbindable stop must refuse naming the missing handle, got %v", err)
	}
	// Nothing signalled, nothing removed: the record is kept for the
	// operator to inspect.
	if got, rerr := os.ReadFile(pidPathForTest(dir)); rerr != nil || string(got) != string(record) {
		t.Errorf("refused stop must keep the pid record (read %q, %v)", got, rerr)
	}
}

func TestAcquirePIDHandle_RealHandles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal-0 semantics differ on windows")
	}
	if !DirectStopBound() {
		// No lifetime binding on this platform: a live pid refuses
		// instead of yielding a signalling handle.
		if _, err := acquirePIDHandle(os.Getpid()); err == nil {
			t.Error("acquire of a live pid without lifetime binding must refuse, got a handle")
		} else if !strings.Contains(err.Error(), "possibly-reused PID") {
			t.Errorf("live-pid refusal must name the reused-PID race, got %v", err)
		}
	} else {
		h, err := acquirePIDHandle(os.Getpid())
		if err != nil {
			t.Fatalf("acquire self: %v", err)
		}
		defer h.Release()
		if err := h.Signal(syscall.Signal(0)); err != nil {
			t.Errorf("self signal-0: %v", err)
		}
	}
	// A pid that cannot exist: the acquire succeeds with a handle that
	// signals nothing (gone on Linux, signal-0-proven gone elsewhere).
	d, err := acquirePIDHandle(1<<30 - 7)
	if err != nil {
		t.Fatalf("acquire dead pid must not fail: %v", err)
	}
	defer d.Release()
	if err := d.Signal(syscall.Signal(0)); !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("dead handle signal-0 = %v, want ESRCH/ErrProcessDone", err)
	}
}

func pidPathForTest(dir string) string {
	return filepath.Join(dir, pidFile)
}
