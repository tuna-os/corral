package qemu

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/types"
)

// systemdVM points the package at throwaway state dirs, says a systemd user
// session exists, and returns the VM's directory and the unit directory.
// The systemctl stub is restored to real on cleanup.
func systemdVM(t *testing.T, name string) (vmDir, unitDir string) {
	t.Helper()
	home := t.TempDir()
	unitDir = t.TempDir()
	SetStateDirs(home, unitDir)
	SetUserSystemd(func() bool { return true })
	t.Cleanup(func() {
		SetStateDirs("", "")
		SetUserSystemd(nil)
		SetSystemctl(nil)
	})
	vmDir = filepath.Join(home, name)
	if err := os.MkdirAll(vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return vmDir, unitDir
}

func sleepBin(t *testing.T) string {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	return sleep
}

func TestStartOwned_ConcurrentSingleWinner(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	// Eight concurrent starts: exactly one wins, the rest observe the
	// winner's VM — never two QEMUs on one disk.
	const n = 8
	var wg sync.WaitGroup
	started := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			started[i], errs[i] = StartOwned("gate")
		}(i)
	}
	wg.Wait()
	var winners int
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("start %d: %v", i, errs[i])
		}
		if started[i] {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d concurrent starts won, want exactly 1", winners)
	}
	pid, alive := directPID("gate")
	if !alive {
		t.Fatal("winner left no live VM")
	}
	t.Cleanup(func() { _ = Stop("gate") })
	// One pid file, one owner.
	raw, err := os.ReadFile(filepath.Join(dir, pidFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), fmt.Sprintf("%d", pid)) {
		t.Errorf("pid file does not name the live pid %d: %s", pid, raw)
	}
}

func TestStartOwnedToken_ReplacementRefused(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	tok1, started, err := StartOwnedToken("gate")
	if err != nil || !started {
		t.Fatalf("first start: started=%v err=%v", started, err)
	}
	if !tok1.Resolved || !tok1.Direct || len(tok1.Raw) == 0 {
		t.Fatalf("first token unresolved: %+v", tok1)
	}
	// Another actor stops and restarts the VM before our cleanup runs.
	// Without stop binding the live stop refuses by design and the
	// replacement flow below (which needs bound stops) cannot run.
	if err := Stop("gate"); !DirectStopBound() {
		requireUnboundStopRefusal(t, err, "stop of a live direct VM")
		killRecordedChild(t, "gate")
		return
	} else if err != nil {
		t.Fatalf("replacement stop: %v", err)
	}
	tok2, started, err := StartOwnedToken("gate")
	if err != nil || !started {
		t.Fatalf("second start: started=%v err=%v", started, err)
	}
	t.Cleanup(func() { _ = StopOwned("gate", tok2) })
	if string(tok1.Raw) == string(tok2.Raw) {
		t.Fatal("replacement did not change the ownership record")
	}
	// The stale token refuses: the stranger keeps running.
	if err := StopOwned("gate", tok1); err == nil || !strings.Contains(err.Error(), "ownership changed") {
		t.Errorf("stale token stop must refuse, got %v", err)
	}
	if pid, alive := directPID("gate"); !alive || pid != tok2.PID {
		t.Errorf("stranger VM disturbed: pid %d alive %v, want pid %d alive", pid, alive, tok2.PID)
	}
	// The current token stops exactly what it owns.
	if err := StopOwned("gate", tok2); err != nil {
		t.Errorf("current token stop: %v", err)
	}
	if IsRunning("gate") {
		t.Error("VM still running after owned stop")
	}
}

func TestStopOwned_UnresolvedRefuses(t *testing.T) {
	if err := StopOwned("gate", StartToken{}); err == nil ||
		!strings.Contains(err.Error(), "never resolved") {
		t.Errorf("unresolved token must refuse, got %v", err)
	}
}

func TestStopOwned_MissingRecord(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	// Live child, deleted record: still ours to stop, pinned to the token.
	tok, started, err := StartOwnedToken("gate")
	if err != nil || !started {
		t.Fatalf("start: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, pidFile)); err != nil {
		t.Fatal(err)
	}
	// Without stop binding the live stop refuses by design (the dead
	// case below still runs: it needs no signal).
	if err := StopOwned("gate", tok); !DirectStopBound() {
		requireUnboundStopRefusal(t, err, "owned stop of a live record-less child")
		killTestPID(t, tok.PID)
	} else {
		if err != nil {
			t.Errorf("owned stop with deleted record: %v", err)
		}
		if IsRunning("gate") {
			t.Error("child still running after owned stop")
			_ = Stop("gate")
		}
	}
	// Dead child, deleted record: nothing to do, silent success.
	tok, started, err = StartOwnedToken("gate")
	if err != nil || !started {
		t.Fatalf("start: %v", err)
	}
	proc, _ := os.FindProcess(tok.PID)
	_ = proc.Signal(syscall.SIGKILL)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, st := directState("gate"); st == pidAbsent {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("killed child never classified absent")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := os.Remove(filepath.Join(dir, pidFile)); err != nil {
		t.Fatal(err)
	}
	if err := StopOwned("gate", tok); err != nil {
		t.Errorf("owned stop of a dead record-less child must succeed, got %v", err)
	}
}

func TestCreate_StartRaceForce(t *testing.T) {
	dir := directVM(t, "gate")
	fakeQemuBin(t)
	if err := writeLaunchCommand(dir, sleepBin(t), []string{"120"}); err != nil {
		t.Fatal(err)
	}
	if err := Start("gate"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	payload := []byte("bootc-built-disk-content")
	if err := os.WriteFile(filepath.Join(dir, "disk.qcow2"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	// Defensive reaper: if the test fails mid-race, no sleep may leak.
	defer func() {
		if recorded, err := readPIDFile("gate"); err == nil && recorded.PID > 0 {
			if proc, _ := os.FindProcess(recorded.PID); proc != nil {
				_ = proc.Signal(syscall.SIGKILL)
			}
		}
	}()
	// --force recreation racing a start: the lifecycle lock serializes
	// them, so no start slips between ProvenStopped and the conversion,
	// and the end state is consistent either way.
	var wg sync.WaitGroup
	var forceErr, startErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		forceErr = Create(types.CreateOpts{Name: "gate", Force: true, ExistingDisk: true})
	}()
	go func() {
		defer wg.Done()
		_, startErr = StartOwned("gate")
	}()
	wg.Wait()
	// Without stop binding the force path cannot stop the live VM and
	// refuses by design; the post-recreation assertions below are
	// bound-platform-only.
	if !DirectStopBound() {
		if forceErr == nil || !strings.Contains(forceErr.Error(), "could not be stopped") {
			t.Errorf("force create over a live VM without stop binding must refuse naming the stop, got %v", forceErr)
		}
		killRecordedChild(t, "gate")
		return
	}
	if forceErr != nil {
		t.Fatalf("force create: %v", forceErr)
	}
	// The racer either observes the running VM (nil) or starts the
	// recreated one — whose shim emulator exits immediately, an honest
	// error that removes its own pid file.
	if startErr != nil && !strings.Contains(startErr.Error(), "exited immediately") {
		t.Fatalf("racing start: %v", startErr)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "disk.qcow2"))
	if string(got) != string(payload) {
		t.Errorf("prepared disk was modified: got %q", got)
	}
	// Either order ends stopped: the force path stops the old VM, and a
	// shim-started replacement dies on its own.
	if IsRunning("gate") {
		t.Error("VM running after force race — a start slipped through")
		_ = Stop("gate")
	}
	if _, _, st := directState("gate"); st != pidAbsent && st != pidAlien {
		t.Errorf("final direct state = %s, want absent (or alien)", st)
	}
}

func TestStartOwned_SystemdConcurrent(t *testing.T) {
	_, unitDir := systemdVM(t, "gate")
	if err := os.WriteFile(filepath.Join(unitDir, "corral-gate.service"), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	active := false
	starts := 0
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch args[0] {
		case "is-active":
			if active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), fmt.Errorf("exit status 3")
		case "start":
			starts++
			active = true
			return []byte{}, nil
		case "show":
			return []byte("MainPID=4242\nActiveEnterTimestamp=Tue 2026-10-13 00:00:00 UTC\n"), nil
		default:
			return []byte{}, nil
		}
	})
	// Concurrent systemd starts serialize on the lifecycle lock: exactly
	// one systemctl start runs, the rest observe active.
	const n = 8
	var wg sync.WaitGroup
	started := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			started[i], errs[i] = StartOwned("gate")
		}(i)
	}
	wg.Wait()
	var winners int
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("start %d: %v", i, errs[i])
		}
		if started[i] {
			winners++
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 || winners != 1 {
		t.Errorf("systemctl starts = %d, winners = %d — want exactly 1 of each", starts, winners)
	}
}

func TestStartOwnedToken_SystemdCapturesActivation(t *testing.T) {
	_, unitDir := systemdVM(t, "gate")
	if err := os.WriteFile(filepath.Join(unitDir, "corral-gate.service"), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "is-active":
			return []byte("inactive\n"), fmt.Errorf("exit status 3")
		case "start":
			return []byte{}, nil
		case "show":
			return []byte("MainPID=4242\nActiveEnterTimestamp=Tue 2026-10-13 00:00:00 UTC\n"), nil
		default:
			return []byte{}, nil
		}
	})
	tok, started, err := StartOwnedToken("gate")
	if err != nil || !started {
		t.Fatalf("start: started=%v err=%v", started, err)
	}
	if tok.Direct || !tok.Resolved || tok.Unit != "corral-gate" ||
		tok.MainPID != "4242" || tok.ActiveSince != "Tue 2026-10-13 00:00:00 UTC" {
		t.Errorf("systemd token wrong: %+v", tok)
	}
}

func TestStopOwned_SystemdReplacementRefused(t *testing.T) {
	_, unitDir := systemdVM(t, "gate")
	if err := os.WriteFile(filepath.Join(unitDir, "corral-gate.service"), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var mainPID, since string
	var stops int
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch args[0] {
		case "is-active":
			return []byte("inactive\n"), fmt.Errorf("exit status 3")
		case "start":
			mainPID, since = "4242", "Tue 2026-10-13 00:00:00 UTC"
			return []byte{}, nil
		case "show":
			return []byte("MainPID=" + mainPID + "\nActiveEnterTimestamp=" + since + "\n"), nil
		case "stop":
			stops++
			mainPID, since = "0", "n/a"
			return []byte{}, nil
		default:
			return []byte{}, nil
		}
	})
	tok, started, err := StartOwnedToken("gate")
	if err != nil || !started || !tok.Resolved {
		t.Fatalf("start: %+v %v %v", tok, started, err)
	}
	// The unit re-activates (another actor's restart) before our cleanup.
	mu.Lock()
	mainPID, since = "9999", "Tue 2026-10-13 00:00:01 UTC"
	mu.Unlock()
	if err := StopOwned("gate", tok); err == nil || !strings.Contains(err.Error(), "ownership changed") {
		t.Errorf("replaced activation must refuse, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if stops != 0 {
		t.Error("refused stop still stopped the replacement unit")
	}
}

func TestUnitProvenStopped(t *testing.T) {
	unitDir := t.TempDir()
	present := filepath.Join(unitDir, "corral-gate.service")
	if err := os.WriteFile(present, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(unitDir, "corral-missing.service")
	defer SetSystemctl(nil)
	for _, tc := range []struct {
		name     string
		out      string
		err      error
		unitPath string
		want     bool
	}{
		{"active is running", "active\n", nil, present, false},
		{"activating is not stopped", "activating\n", fmt.Errorf("exit status 3"), present, false},
		{"deactivating is not stopped", "deactivating\n", fmt.Errorf("exit status 3"), present, false},
		{"inactive is stopped", "inactive\n", fmt.Errorf("exit status 3"), present, true},
		{"failed is stopped", "failed\n", fmt.Errorf("exit status 3"), present, true},
		{"unknown without unit file is stopped", "unknown\n", fmt.Errorf("exit status 3"), missing, true},
		{"unknown with unit file is unproven", "unknown\n", fmt.Errorf("exit status 3"), present, false},
		{"query error with garbage is unproven", "???\n", fmt.Errorf("boom"), present, false},
		{"query error with empty output is unproven", "", fmt.Errorf("boom"), missing, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) { return []byte(tc.out), tc.err })
			if got := unitProvenStopped("corral-gate", tc.unitPath); got != tc.want {
				t.Errorf("unitProvenStopped = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsRunningCtx(t *testing.T) {
	// A cancelled context reports not-running promptly even when the
	// manager would hang forever — WITHOUT dispatching: no subprocess is
	// spawned and no goroutine is left behind. (The old abandonment
	// design spawned a worker per call and leaked it on cancel; the
	// mid-probe stall case is covered by the supervised-helper test in
	// bounded_test.go instead.)
	var calls atomic.Int64
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		calls.Add(1)
		select {}
	})
	defer SetSystemctl(nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	if IsRunningCtx(ctx, "gate") {
		t.Error("cancelled probe must report not-running")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancelled probe took %s", elapsed)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("cancelled probe dispatched %d systemctl call(s) — must dispatch none", n)
	}
	// Repeated cancelled probes leak nothing: an abandonment design
	// strands one goroutine per call in the blocking stub.
	base := runtime.NumGoroutine()
	for i := 0; i < 25; i++ {
		if IsRunningCtx(ctx, "gate") {
			t.Fatal("cancelled probe must report not-running")
		}
	}
	time.Sleep(200 * time.Millisecond)
	if growth := runtime.NumGoroutine() - base; growth > 2 {
		t.Errorf("25 cancelled probes grew the goroutine count by %d — workers are being abandoned", growth)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("cancelled probes dispatched %d systemctl call(s) — must dispatch none", n)
	}
	// Prompt stubbed paths: active reads running, anything else reads
	// not-running, without consulting the session probe.
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) { return []byte("active\n"), nil })
	if !IsRunningCtx(context.Background(), "gate") {
		t.Error("active unit must read running")
	}
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte("inactive\n"), fmt.Errorf("exit status 3")
	})
	if IsRunningCtx(context.Background(), "gate") {
		t.Error("inactive unit must read not-running")
	}
}

func TestStopDispatch_UnknownDirectRefusesSystemd(t *testing.T) {
	dir, _ := systemdVM(t, "gate")
	if err := writeLaunchCommand(dir, "/usr/bin/qemu-system-x86_64", []string{"-name", "gate"}); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	if err := os.WriteFile(filepath.Join(dir, pidFile), []byte(fmt.Sprintf("%d\n", self)), 0o644); err != nil {
		t.Fatal(err)
	}
	pinProbes(t, "", "", os.ErrNotExist, os.ErrNotExist)
	var stops int
	SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "stop" {
			stops++
		}
		return []byte{}, nil
	})
	// The unknown direct child refuses the stop — and the unrelated unit
	// is NOT stopped behind a false "VM stopped".
	if err := Stop("gate"); err == nil || !strings.Contains(err.Error(), "cannot be proven") {
		t.Errorf("unknown direct child must refuse, got %v", err)
	}
	if stops != 0 {
		t.Error("refused stop still stopped the systemd unit")
	}
}

func TestWriteMetadataFile(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"name":"gate"}`)
	if err := writeMetadataFile(dir, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("metadata bytes = %q, want %q", got, data)
	}
	// A failing write is an error, never a silent success: a file path as
	// the directory fails on every user (no chmod games that root ignores).
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeMetadataFile(blocker, data); err == nil {
		t.Error("metadata write into a file path must fail")
	}
}
