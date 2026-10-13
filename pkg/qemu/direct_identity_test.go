package qemu

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadPIDFile_LegacyAndV2(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })

	legacy := filepath.Join(home, "legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, pidFile), []byte("1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := readPIDFile("legacy")
	if err != nil || data.PID != 1234 {
		t.Fatalf("legacy read = %+v, %v", data, err)
	}

	v2dir := filepath.Join(home, "v2")
	if err := os.MkdirAll(v2dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v2dir, pidFile),
		[]byte(`{"version":2,"pid":5678,"binary":"/usr/bin/qemu-system-x86_64","start":"999"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err = readPIDFile("v2")
	if err != nil || data.PID != 5678 || data.Binary == "" || data.Start != "999" {
		t.Fatalf("v2 read = %+v, %v", data, err)
	}
}

// pinProbes stubs the identity readers with fixed evidence and restores the
// real ones on cleanup.
func pinProbes(t *testing.T, exe, start string, exeErr, startErr error) {
	t.Helper()
	origExe, origStart := procExe, procStartID
	procExe = func(pid int) (string, error) { return exe, exeErr }
	procStartID = func(pid int) (string, error) { return start, startErr }
	t.Cleanup(func() { procExe, procStartID = origExe, origStart })
}

// pinSignal stubs the signal-0 liveness probe and restores the real one on
// cleanup, so ESRCH-vs-EPERM behavior pins without real foreign processes.
func pinSignal(t *testing.T, err error) {
	t.Helper()
	orig := procSignal0
	procSignal0 = func(pid int) error { return err }
	t.Cleanup(func() { procSignal0 = orig })
}

func TestClassifyPID(t *testing.T) {
	self := os.Getpid()
	rec := pidFileData{Version: 2, PID: self, Binary: "/usr/bin/qemu-system-x86_64", Start: "999"}

	// Matching binary + start is owned: both dimensions must agree.
	pinProbes(t, "exe:/usr/bin/qemu-system-x86_64", "999", nil, nil)
	if st := classifyPID(self, rec, ""); st != pidOwned {
		t.Errorf("matching identity = %s, want owned", st)
	}

	// Reused PID with an unrelated binary is alien, never unknown.
	pinProbes(t, "exe:/usr/bin/python3", "999", nil, nil)
	if st := classifyPID(self, rec, ""); st != pidAlien {
		t.Errorf("unrelated binary = %s, want alien", st)
	}

	// Same binary but a different process lifetime is alien.
	pinProbes(t, "exe:/usr/bin/qemu-system-x86_64", "1000", nil, nil)
	if st := classifyPID(self, rec, ""); st != pidAlien {
		t.Errorf("different start = %s, want alien", st)
	}

	// An argv string that merely mentions the binary is not evidence: the
	// probe reports resolved executables, and anything else-shaped fails
	// closed. (A forged "exe:" string is the probe's contract, not the
	// classifier's — here the mismatch case proves disagreement wins.)
	pinProbes(t, "exe:/usr/bin/qemu-system-x86_64-wrapped", "999", nil, nil)
	if st := classifyPID(self, rec, ""); st != pidAlien {
		t.Errorf("near-miss binary = %s, want alien", st)
	}

	// One matching dimension with the other unavailable is unknown in BOTH
	// directions — never owned. An exe match with an unreadable start
	// token is a reused PID running the same binary until proven
	// otherwise (another VM's QEMU matches the path too).
	pinProbes(t, "exe:/usr/bin/qemu-system-x86_64", "", nil, os.ErrNotExist)
	if st := classifyPID(self, rec, ""); st != pidUnknown {
		t.Errorf("exe match + unavailable start = %s, want unknown", st)
	}
	pinProbes(t, "", "999", os.ErrNotExist, nil)
	if st := classifyPID(self, rec, ""); st != pidUnknown {
		t.Errorf("start match + unavailable exe = %s, want unknown", st)
	}

	// A legacy file (no lifetime token) whose exe matches is unknown, not
	// owned: the exe match alone cannot distinguish our child from a
	// reused PID running the same binary.
	pinProbes(t, "exe:/usr/bin/sleep", "", nil, nil)
	if st := classifyPID(self, pidFileData{PID: self}, "/usr/bin/sleep"); st != pidUnknown {
		t.Errorf("legacy file matching launch binary = %s, want unknown", st)
	}
	// ...but a legacy mismatch still proves alien.
	if st := classifyPID(self, pidFileData{PID: self}, "/usr/bin/qemu-system-x86_64"); st != pidAlien {
		t.Errorf("legacy file mismatching launch binary = %s, want alien", st)
	}

	// A live pid with no verifiable dimension is unknown — never owned
	// (nothing may be signalled) and never absent (no second QEMU may
	// start on the disk).
	pinProbes(t, "", "", os.ErrNotExist, os.ErrNotExist)
	if st := classifyPID(self, pidFileData{PID: self, Binary: "/qemu"}, "/qemu"); st != pidUnknown {
		t.Errorf("unverifiable live pid = %s, want unknown", st)
	}

	// macOS basename collision: a matching truncated comm with a different
	// lifetime is alien, and with an unreadable lifetime is unknown — the
	// comm alone never owns, since unrelated copies share the prefix.
	pinProbes(t, "comm:qemu-system-x86", "1000", nil, nil)
	macRec := pidFileData{Version: 2, PID: self, Binary: "/opt/homebrew/bin/qemu-system-x86_64", Start: "999"}
	if st := classifyPID(self, macRec, ""); st != pidAlien {
		t.Errorf("comm match + different start = %s, want alien", st)
	}
	pinProbes(t, "comm:qemu-system-x86", "", nil, os.ErrNotExist)
	if st := classifyPID(self, macRec, ""); st != pidUnknown {
		t.Errorf("comm match + unavailable start = %s, want unknown", st)
	}

	// Signal errors distinguish gone from uninspectable: ESRCH (or an
	// already-reaped handle) is absent; EPERM means the process exists
	// but cannot be inspected, which is unknown.
	pinSignal(t, syscall.ESRCH)
	pinProbes(t, "exe:/usr/bin/qemu-system-x86_64", "999", nil, nil)
	if st := classifyPID(1<<30-7, rec, ""); st != pidAbsent {
		t.Errorf("ESRCH = %s, want absent", st)
	}
	pinSignal(t, os.ErrProcessDone)
	if st := classifyPID(1<<30-7, rec, ""); st != pidAbsent {
		t.Errorf("already-reaped = %s, want absent", st)
	}
	pinSignal(t, syscall.EPERM)
	if st := classifyPID(self, rec, ""); st != pidUnknown {
		t.Errorf("EPERM = %s, want unknown", st)
	}
	pinSignal(t, syscall.EIO)
	if st := classifyPID(self, rec, ""); st != pidUnknown {
		t.Errorf("unexpected signal error = %s, want unknown", st)
	}
}

func TestClassifyPID_DeadPIDIsAbsent(t *testing.T) {
	// No stubs: a real signal 0 against a dead pid is ESRCH — absent
	// however the (real) probes would answer.
	rec := pidFileData{Version: 2, PID: 1<<30 - 7, Binary: "/usr/bin/qemu-system-x86_64", Start: "999"}
	if st := classifyPID(1<<30-7, rec, ""); st != pidAbsent {
		t.Errorf("dead pid = %s, want absent", st)
	}
}

func TestExeIDMatches(t *testing.T) {
	if !exeIDMatches("exe:/usr/bin/qemu", "/usr/bin/qemu") {
		t.Error("identical exe paths must match")
	}
	if exeIDMatches("exe:/usr/bin/qemu", "/usr/bin/qemu-wrapped") {
		t.Error("different exe paths must not match")
	}
	if exeIDMatches("exe:/usr/bin/qemu (deleted)", "/usr/bin/qemu") {
		t.Error("deleted-then-replaced binary must not match")
	}
	if exeIDMatches("tostring", "/usr/bin/qemu") {
		t.Error("unshaped evidence must not match")
	}
	// macOS comm: basename under MAXCOMLEN truncation (15 visible chars).
	if !exeIDMatches("comm:qemu-system-x86", "/usr/bin/qemu-system-x86_64") {
		t.Error("truncated comm must match its binary")
	}
	if exeIDMatches("comm:python3", "/usr/bin/qemu-system-x86_64") {
		t.Error("foreign comm must not match")
	}
	if exeIDMatches("comm:", "/usr/bin/qemu") {
		t.Error("empty comm must not match")
	}
	// A 15-char basename matches in full.
	if !exeIDMatches("comm:123456789012345", "/bin/123456789012345") {
		t.Error("15-char comm must match its basename")
	}
	// A 16-char comm is not kernel evidence (the kernel truncates to 15)
	// and must not match.
	if exeIDMatches("comm:qemu-system-x86_", "/usr/bin/qemu-system-x86_64") {
		t.Error("overlong comm must not match")
	}
}

func TestDirectPID_RejectsReusedPID(t *testing.T) {
	dir := directVM(t, "gate")
	// Record a launch command for qemu, but point the pid file at this very
	// test process (alive, wrong binary). The real probes prove alien.
	if err := writeLaunchCommand(dir, "/usr/bin/qemu-system-x86_64", []string{"-name", "gate"}); err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	if err := os.WriteFile(filepath.Join(dir, pidFile), []byte(strconv.Itoa(self)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if pid, alive := directPID("gate"); alive {
		t.Errorf("reused pid %d reported alive for the wrong binary", pid)
	}
	if _, _, st := directState("gate"); st != pidAlien {
		t.Errorf("reused pid state = %s, want alien", st)
	}
}

func TestDirectState_MalformedPIDFileIsUnknown(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, "/usr/bin/qemu-system-x86_64", []string{"-name", "gate"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pidFile), []byte("not-a-pid-record!!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Malformed is unknown, never absent: uncertainty about the record is
	// not proof that no child exists.
	if _, _, st := directState("gate"); st != pidUnknown {
		t.Errorf("malformed pid file state = %s, want unknown", st)
	}
	// Both start and stop refuse with the unreadable-record remedy, and
	// the file is kept for inspection.
	if err := Start("gate"); err == nil || !strings.Contains(err.Error(), "unreadable pid file") {
		t.Errorf("start over malformed record must refuse, got %v", err)
	}
	if err := Stop("gate"); err == nil || !strings.Contains(err.Error(), "unreadable pid file") {
		t.Errorf("stop over malformed record must refuse, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, pidFile)); err != nil {
		t.Errorf("refused stop must keep the pid file: %v", err)
	}
}

func TestStopDirect_RefusesUnknown(t *testing.T) {
	dir := directVM(t, "gate")
	if err := writeLaunchCommand(dir, "/usr/bin/qemu-system-x86_64", []string{"-name", "gate"}); err != nil {
		t.Fatal(err)
	}
	// A live pid whose identity cannot be verified: Stop must refuse and
	// keep the pid file, never signal it.
	self := os.Getpid()
	if err := os.WriteFile(filepath.Join(dir, pidFile), []byte(strconv.Itoa(self)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pinProbes(t, "", "", os.ErrNotExist, os.ErrNotExist)

	if err := Stop("gate"); err == nil || !strings.Contains(err.Error(), "cannot be proven") {
		t.Errorf("unknown identity must refuse signalling, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, pidFile)); err != nil {
		t.Errorf("refused stop must keep the pid file: %v", err)
	}
}

func TestWritePIDFile_PinsBinary(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	dir := filepath.Join(home, "gate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	origStart := procStartID
	procStartID = func(pid int) (string, error) { return "4242", nil }
	t.Cleanup(func() { procStartID = origStart })

	if err := writePIDFile(dir, 1234, "/opt/homebrew/bin/qemu-system-aarch64"); err != nil {
		t.Fatal(err)
	}
	data, err := readPIDFile("gate")
	if err != nil {
		t.Fatal(err)
	}
	if data.PID != 1234 || data.Binary != "/opt/homebrew/bin/qemu-system-aarch64" || data.Start != "4242" {
		t.Errorf("pid file does not pin identity: %+v", data)
	}
}

func TestWritePIDFile_RequiresStartIdentity(t *testing.T) {
	dir := t.TempDir()
	origStart := procStartID
	t.Cleanup(func() { procStartID = origStart })
	// A failed probe fails the write: the record must never silently omit
	// its lifetime token, or the VM it names could never be stopped by name.
	procStartID = func(pid int) (string, error) { return "", os.ErrNotExist }
	if err := writePIDFile(dir, 1234, "/usr/bin/qemu"); err == nil ||
		!strings.Contains(err.Error(), "start identity") {
		t.Errorf("probe failure must fail the write, got %v", err)
	}
	procStartID = func(pid int) (string, error) { return "", nil }
	if err := writePIDFile(dir, 1234, "/usr/bin/qemu"); err == nil {
		t.Error("empty start token must fail the write")
	}
	if _, err := os.Stat(filepath.Join(dir, pidFile)); !os.IsNotExist(err) {
		t.Error("failed write must not leave a pid file")
	}
}

func TestRemovePIDRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, pidFile)
	want := []byte("record-bytes-v1\n")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	// A replaced record belongs to another invocation: kept.
	if err := os.WriteFile(path, []byte("record-bytes-v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	removePIDRecord(path, want)
	if _, err := os.Stat(path); err != nil {
		t.Error("replaced record must be kept")
	}
	// Matching bytes are ours: removed.
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	removePIDRecord(path, cur)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("matching record must be removed")
	}
	// A missing file is already gone: success, no error.
	removePIDRecord(path, want)
}

// shrinkEscalationWaits shortens the TERM/KILL waits and the poll interval
// so escalation tests run in milliseconds, restoring the production values
// on cleanup.
func shrinkEscalationWaits(t *testing.T) {
	t.Helper()
	origTerm, origKill, origPoll := stopTermWait, stopKillWait, stopPollInterval
	stopTermWait, stopKillWait, stopPollInterval = 60*time.Millisecond, 60*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { stopTermWait, stopKillWait, stopPollInterval = origTerm, origKill, origPoll })
}

func writePinnedRecord(t *testing.T) (path string, want []byte) {
	t.Helper()
	path = filepath.Join(t.TempDir(), pidFile)
	want = []byte("pinned-record\n")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, want
}

func TestStopPinned_RefusesKillOnUnknown(t *testing.T) {
	shrinkEscalationWaits(t)
	path, want := writePinnedRecord(t)
	var signals []os.Signal
	signal := func(s os.Signal) error {
		signals = append(signals, s)
		return nil
	}
	// Owned through the TERM wait, then unverifiable at escalation time:
	// the SIGKILL must never be sent.
	calls := 0
	pinned := func() pidState {
		calls++
		if calls <= 2 {
			return pidOwned
		}
		return pidUnknown
	}
	err := stopPinnedLocked("gate", path, 424242, pinned, signal, want)
	if err == nil || !strings.Contains(err.Error(), "refusing to SIGKILL") {
		t.Fatalf("escalation on unknown identity must refuse, got %v", err)
	}
	for _, s := range signals {
		if s == syscall.SIGKILL {
			t.Fatal("SIGKILL was sent to an unverified process")
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("refused escalation must keep the pid file")
	}
}

func TestStopPinned_RefusesKillOnReusedPID(t *testing.T) {
	shrinkEscalationWaits(t)
	path, want := writePinnedRecord(t)
	var signals []os.Signal
	signal := func(s os.Signal) error {
		signals = append(signals, s)
		return nil
	}
	// Owned at TERM time, then the pid demonstrably reused (alien) by
	// escalation time: the SIGKILL must never be sent to the stranger.
	calls := 0
	pinned := func() pidState {
		calls++
		if calls <= 2 {
			return pidOwned
		}
		return pidAlien
	}
	err := stopPinnedLocked("gate", path, 424242, pinned, signal, want)
	if err == nil || !strings.Contains(err.Error(), "refusing to SIGKILL") {
		t.Fatalf("escalation on a reused pid must refuse, got %v", err)
	}
	for _, s := range signals {
		if s == syscall.SIGKILL {
			t.Fatal("SIGKILL was sent to a reused pid")
		}
	}
	if len(signals) != 1 || signals[0] != syscall.SIGTERM {
		t.Errorf("reused-pid escalation sent %v, want exactly [SIGTERM]", signals)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("refused escalation must keep the pid file")
	}
}

func TestStopPinned_TermESRCHRemoves(t *testing.T) {
	shrinkEscalationWaits(t)
	path, want := writePinnedRecord(t)
	// ESRCH proves the pid empty at signal time: the child is dead, its
	// record stale — removed without consulting identity.
	signal := func(s os.Signal) error { return syscall.ESRCH }
	if err := stopPinnedLocked("gate", path, 424242, func() pidState { return pidOwned }, signal, want); err != nil {
		t.Errorf("ESRCH on SIGTERM must succeed, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("stale record must be removed after ESRCH")
	}
}

func TestStopPinned_TermErrorConsultsPinned(t *testing.T) {
	shrinkEscalationWaits(t)
	// A non-ESRCH signal failure with unverifiable identity refuses (it is
	// NOT treated as gone); with proven ownership the signal error itself
	// is reported; with a dead pid the stale record is removed.
	signal := func(s os.Signal) error { return syscall.EPERM }

	path, want := writePinnedRecord(t)
	err := stopPinnedLocked("gate", path, 424242, func() pidState { return pidUnknown }, signal, want)
	if err == nil || !strings.Contains(err.Error(), "refusing to escalate") {
		t.Errorf("TERM failure + unknown must refuse escalation, got %v", err)
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Error("refused escalation must keep the pid file")
	}

	path, want = writePinnedRecord(t)
	err = stopPinnedLocked("gate", path, 424242, func() pidState { return pidOwned }, signal, want)
	if err == nil || !strings.Contains(err.Error(), "stopping QEMU") {
		t.Errorf("TERM failure + owned must report the signal error, got %v", err)
	}

	path, want = writePinnedRecord(t)
	if err := stopPinnedLocked("gate", path, 424242, func() pidState { return pidAbsent }, signal, want); err != nil {
		t.Errorf("TERM failure + absent must succeed, got %v", err)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Error("stale record must be removed")
	}
}

func TestStopPinned_ExitDuringWaitRemoves(t *testing.T) {
	shrinkEscalationWaits(t)
	path, want := writePinnedRecord(t)
	signal := func(s os.Signal) error { return nil }
	calls := 0
	pinned := func() pidState {
		calls++
		if calls <= 1 {
			return pidOwned
		}
		return pidAbsent
	}
	if err := stopPinnedLocked("gate", path, 424242, pinned, signal, want); err != nil {
		t.Errorf("exit during TERM wait must succeed, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("exited child's record must be removed")
	}
}

func TestKillAndReap(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	// Observed retirement: a real child that dies on Kill reports nil.
	cmd := exec.Command(sleep, "120")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if err := killAndReap(cmd.Process, exited, 5*time.Second); err != nil {
		t.Errorf("reaped child must report nil, got %v", err)
	}
	// Unobserved exit with a Kill failure is uncertainty, never cleanup:
	// a dead process errors on Kill while the exit channel stays silent.
	dead := exec.Command(sleep, "0")
	if err := dead.Start(); err != nil {
		t.Fatal(err)
	}
	_ = dead.Wait() // reaped: already finished
	never := make(chan error)
	if err := killAndReap(dead.Process, never, 20*time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "unobserved") {
		t.Errorf("kill failure + silent exit must report uncertainty, got %v", err)
	}
	// Unobserved exit with a successful Kill is still uncertainty: the
	// kill landed but nobody saw the exit.
	cmd2 := exec.Command(sleep, "120")
	if err := cmd2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd2.Process.Kill(); _ = cmd2.Wait() }()
	if err := killAndReap(cmd2.Process, never, 20*time.Millisecond); err == nil ||
		!strings.Contains(err.Error(), "unobserved") {
		t.Errorf("silent exit must report uncertainty even when kill succeeds, got %v", err)
	}
}
