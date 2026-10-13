package qemu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Running a VM where there is no systemd user session.
//
// Every local VM is a `systemd --user` unit, which is right on a workstation:
// the VM survives a closed terminal, the journal has its output, and the user's
// own session owns it. On a CI runner none of that holds. A hosted GitHub
// runner has no user bus at all, and `sudo` — which `bootc install` requires —
// has no session either, so `systemctl --user start` fails with "Failed to
// connect to bus" however healthy the machine is.
//
// So the unit is still written, and when no user session can run it, corral
// starts QEMU itself: one detached process, its argv recorded next to the VM's
// other state, its pid in a file. Same VM, same flags, same sockets — the only
// thing missing is the supervisor nobody is there to talk to.

// launchCommand is the exact QEMU invocation for a VM, saved at create time.
// Recorded rather than rebuilt, so a VM always starts the way it was made even
// if the generator changes under it.
type launchCommand struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args"`
}

const (
	commandFile = "command.json"
	pidFile     = "qemu.pid"
	directLog   = "qemu.log"
)

func writeLaunchCommand(vmDir, binary string, args []string) error {
	data, err := json.MarshalIndent(launchCommand{Binary: binary, Args: args}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(vmDir, commandFile), append(data, '\n'), 0o644)
}

func readLaunchCommand(name string) (launchCommand, error) {
	var cmd launchCommand
	data, err := os.ReadFile(filepath.Join(VMHome(), name, commandFile))
	if err != nil {
		return cmd, fmt.Errorf("VM %q has no recorded QEMU command — recreate it (corral create --force ...) to start it without a systemd user session: %w", name, err)
	}
	if err := json.Unmarshal(data, &cmd); err != nil {
		return cmd, fmt.Errorf("VM %q has an unreadable command.json: %w", name, err)
	}
	if cmd.Binary == "" {
		return cmd, fmt.Errorf("VM %q records no QEMU binary", name)
	}
	return cmd, nil
}

// systemdState is the tri-state answer of the user-manager probe: a manager
// that answers is available or unavailable, and a manager that does not
// answer (timeout, truncated output, novel failure) is unknown — never
// silently unavailable. Destruction (Delete, ProvenStopped) refuses on
// unknown; start/stop routing keeps the historical bool (unknown routes
// like unavailable — see the note on managerProvenAbsent).
type systemdState int

const (
	systemdAvailable systemdState = iota
	systemdUnavailable
	systemdUnknown
)

// probeSystemdState asks the manager whether it can run units and reports
// the tri-state answer. Only confident evidence decides: known
// no-manager spellings and a missing binary prove absence, a clean or
// running-state answer proves presence, and everything else — a timed-out
// probe above all — is unknown.
func probeSystemdState() systemdState {
	out, err := systemctlRun(context.Background(), "is-system-running")
	text := string(out)
	// Every one of these is "there is no user manager to talk to", spelled
	// differently by systemd version and by the runner's environment.
	for _, missing := range []string{
		"Failed to connect to bus",
		"No medium found",
		"Failed to connect to user scope bus",
		"executable file not found",
		"no such file or directory",
	} {
		if strings.Contains(text, missing) {
			return systemdUnavailable
		}
	}
	if err == nil {
		return systemdAvailable
	}
	// A degraded or still-starting manager exits nonzero and is perfectly able
	// to run a unit.
	for _, state := range []string{"running", "degraded", "starting", "maintenance", "stopping"} {
		if strings.Contains(text, state) {
			return systemdAvailable
		}
	}
	// No systemctl binary means no manager to talk to — confident
	// absence, not an unanswered manager.
	if isExecMissing(err) {
		return systemdUnavailable
	}
	// A timeout, a truncation, or anything unrecognized: the manager did
	// not answer, so nothing about it is proven.
	return systemdUnknown
}

// isExecMissing reports whether err is a missing-binary failure rather
// than a manager answer.
func isExecMissing(err error) bool {
	var lookErr *exec.Error
	if errors.As(err, &lookErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file or directory")
}

// systemdStatePin pins the manager state for tests; nil probes really. It
// is the single source of truth behind both the bool routing seam and the
// destruction gate, so a pinned test can never observe them disagreeing.
// SetUserSystemd keeps it in sync for bool pins (a pinned bool is certain:
// true pins available, false pins unavailable); tests pinning unknown
// assign this variable directly (in-package, precedented by the
// systemctlRun/procAcquire direct assignments).
var systemdStatePin *systemdState

// currentSystemdState reports the manager state: the pin when tests pin
// one, the live probe otherwise.
func currentSystemdState() systemdState {
	if systemdStatePin != nil {
		return *systemdStatePin
	}
	return probeSystemdState()
}

// managerProvenAbsent reports confident manager absence: the destruction
// gate. Unknown is not absent — callers refuse instead of deleting.
//
// Routing note: userSystemd (the bool) still maps unknown to false, so
// start/stop path selection is byte-identical to before; only destruction
// consults the unknown arm. Changing start routing on unknown (direct
// start while a unit may exist) belongs to the admission/start lane,
// not to this gate.
func managerProvenAbsent() bool {
	return currentSystemdState() == systemdUnavailable
}

// userSystemd reports whether a systemd user session can run units here.
//
// Asked rather than assumed: the answer differs between a workstation, a
// container, and a sudo shell on a CI runner, and getting it wrong means either
// a VM that never starts or one nothing is supervising.
var userSystemd = func() bool {
	return currentSystemdState() == systemdAvailable
}

// defaultUserSystemd is the real session probe, captured for restoration.
var defaultUserSystemd = userSystemd

// SetUserSystemd overrides the systemd-session probe (for tests). Passing
// nil restores the real one, matching the SetSystemctl/SetPortProbe seams.
// A non-nil pin is read once, now, to pin the tri-state identically (a
// pinned bool is certain); restores must pass nil rather than a captured
// func, so no live probe ever runs at restore time.
func SetUserSystemd(f func() bool) {
	if f == nil {
		userSystemd = defaultUserSystemd
		systemdStatePin = nil
		return
	}
	if f() {
		s := systemdAvailable
		systemdStatePin = &s
	} else {
		s := systemdUnavailable
		systemdStatePin = &s
	}
	userSystemd = f
}

// startDirectLocked launches QEMU as a detached process and records its pid,
// reporting the ownership token for the running state this call started
// plus whether this call started it (false = it was already running; the
// token is then invalid and must not be used). The caller holds the
// lifecycle lock for name (see lifecycle_lock.go), so the absent-check →
// exec → record sequence is atomic against concurrent starts, stops and
// force recreations of the same VM.
//
// The token is captured at launch from what this call wrote — never from a
// later re-read of the record. A launch whose record cannot be verified on
// disk fails with its child reaped (observed retirement), so started=true
// with an unverifiable record cannot happen: the caller either owns a
// proven child or owns nothing.
func startDirectLocked(name string) (StartToken, bool, error) {
	var tok StartToken
	pid, _, st := directState(name)
	switch st {
	case pidOwned:
		fmt.Fprintf(os.Stderr, "VM %q is already running (pid %d).\n", name, pid)
		return tok, false, nil
	case pidUnknown:
		path := filepath.Join(VMHome(), name, pidFile)
		if pid <= 0 {
			return tok, false, fmt.Errorf("VM %q has an unreadable pid file — refusing to start a second QEMU on the same disk (inspect %s, stop any stale QEMU holding the disk, and remove the file to proceed)", name, path)
		}
		return tok, false, fmt.Errorf("VM %q may already be running (pid %d), but that process cannot be proven to be this VM's QEMU — refusing to start a second QEMU on the same disk (inspect pid %d, stop it if it is stale, and remove %s to proceed)",
			name, pid, pid, path)
	case pidAbsent, pidAlien:
		// A stale or foreign pid file is overwritten on success below.
	}
	// A live unit already holds the disk: launching directly now would run
	// a second QEMU on it (see deftoken.go). Refuse, never double-start.
	// Only a positively active answer refuses — on a direct-only host the
	// query failure is the ordinary case.
	if err := refuseActiveUnitForDirect(name); err != nil {
		return tok, false, err
	}
	command, err := readLaunchCommand(name)
	if err != nil {
		return tok, false, err
	}
	vmDir := filepath.Join(VMHome(), name)
	logPath := filepath.Join(vmDir, directLog)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return tok, false, fmt.Errorf("opening %s: %w", logPath, err)
	}
	// The QEMU child holds its own descriptor, so this close only releases
	// ours; nothing reads the result.
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(command.Binary, command.Args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// Its own session, so the VM outlives the shell that started it — which is
	// the one thing the systemd unit was doing for us.
	cmd.SysProcAttr = newSessionAttr()
	if err := cmd.Start(); err != nil {
		return tok, false, fmt.Errorf("starting QEMU: %w (see %s)", err, logPath)
	}
	pid = cmd.Process.Pid

	// Reap it in the background rather than detaching. Nothing here waits for
	// the VM — the goroutine blocks for as long as the guest runs, and dies
	// with this process — but an unreaped child becomes a zombie, and a zombie
	// answers a liveness check as if it were still running. That would make a
	// QEMU which exited on a bad flag look like a VM that is simply slow.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// A record/build/verify failure below reaps the child the start
	// created: the child is already running but unowned without a
	// verifiable record, and leaving it would orphan a QEMU nobody can
	// stop by name. Retirement is observed (killAndReap), never assumed.
	// An observed exit reports started=false (nothing is running); an
	// UNOBSERVED exit keeps the record and reports started=true with the
	// uncertainty — started=false would claim nothing is running, which
	// is exactly what is unproven, and removing the record would destroy
	// the only evidence the operator has.
	failUnstarted := func(stage string, stageErr error, raw []byte) (StartToken, bool, error) {
		cleanupErr := killAndReap(cmd.Process, exited, 5*time.Second)
		pidPath := filepath.Join(vmDir, pidFile)
		if cleanupErr != nil {
			retained := raw
			if retained == nil {
				// Build failure: no record was ever attempted. Stage a
				// legacy pid-only record (best-effort, never clobbering
				// a present file) so the VM reads unknown — fail-closed,
				// never signalled, never double-started — instead of
				// absent. A present file is left alone: without bytes
				// of our own we cannot prove it is not foreign.
				retained = []byte(fmt.Sprintf("{\"pid\":%d}\n", pid))
				if _, serr := os.Stat(pidPath); os.IsNotExist(serr) {
					_ = os.WriteFile(pidPath, retained, 0o644)
				}
			} else if _, serr := os.Stat(pidPath); os.IsNotExist(serr) {
				// Write failure left nothing on disk: retain what was
				// attempted. (Verify failure leaves a present file
				// alone — it may be another invocation's record.)
				_ = os.WriteFile(pidPath, retained, 0o644)
			}
			utok := StartToken{Direct: true, PID: pid, Raw: retained, Resolved: true}
			if len(raw) > 0 {
				var built pidFileData
				if json.Unmarshal(raw, &built) == nil {
					utok.Start = built.Start
				}
			}
			return utok, true, fmt.Errorf("%s: %w (child pid %d exit unobserved: %v — it may still be running; record retained at %s; inspect pid %d and stop it manually if it is stale)", stage, stageErr, pid, cleanupErr, pidPath, pid)
		}
		// The exit was observed (settled): only our exact attempted
		// bytes may go. A foreign replacement (the verify-failure case)
		// belongs to someone else — removing it would orphan their
		// record — so a compare-and-remove keeps it. With no attempt
		// at all (build failure) no bytes are ours to remove: any
		// present file predates us or is foreign.
		if raw != nil {
			removePIDRecord(pidPath, raw)
		}
		return tok, false, fmt.Errorf("%s: %w (the started child was reaped, nothing is running)", stage, stageErr)
	}

	// The ownership record is built in memory first: the token below pins
	// these exact bytes.
	record, raw, err := buildPIDRecord(pid, command.Binary)
	if err != nil {
		return failUnstarted("recording the QEMU pid", err, nil)
	}
	if err := writePIDRecord(vmDir, raw); err != nil {
		return failUnstarted("recording the QEMU pid", err, raw)
	}

	// QEMU exits early on a bad flag or a missing accelerator, and a pid file
	// for a dead process is worse than an error. The exit is observed
	// (settled), but the record may still have been replaced mid-settle:
	// only our exact bytes may go.
	select {
	case waitErr := <-exited:
		removePIDRecord(filepath.Join(vmDir, pidFile), raw)
		output, _ := os.ReadFile(logPath)
		return tok, false, fmt.Errorf("QEMU exited immediately (%v): %s", waitErr, lastLines(string(output), 10))
	case <-time.After(750 * time.Millisecond):
	}

	// The record must be verifiable: what is on disk now must be exactly
	// what this launch wrote. Anything else — an unreadable file, a
	// foreign replacement, a write that landed nowhere (a pid path that
	// is a symlink to /dev/null reports success and keeps nothing) —
	// fails the start with the child reaped, rather than reporting
	// started=true for a child nobody can prove or stop.
	if err := verifyPIDRecord(vmDir, raw); err != nil {
		return failUnstarted("verifying the QEMU pid record", err, raw)
	}

	tok = StartToken{Direct: true, PID: pid, Start: record.Start, Raw: raw, Resolved: true}
	fmt.Fprintf(os.Stderr, "VM %q started (pid %d, no systemd user session — output in %s).\n", name, pid, logPath)
	return tok, true, nil
}

// killAndReap kills an owned-but-unrecorded child and reports whether its
// retirement was actually observed. It returns nil only when the child's
// exit was seen (reaped) — a Kill error or a wait timeout is uncertainty,
// never cleanup, and the caller must say so.
//
// It is a variable so tests can stage the unobservable-exit edge (a real
// SIGKILL always lands on test children) without weakening the production
// path.
var killAndReap = func(proc *os.Process, exited <-chan error, timeout time.Duration) error {
	killErr := proc.Kill()
	select {
	case <-exited:
		return nil
	case <-time.After(timeout):
		if killErr != nil {
			return fmt.Errorf("kill failed: %v; exit unobserved after %s", killErr, timeout)
		}
		return fmt.Errorf("exit unobserved after %s", timeout)
	}
}

// pidFileData is the recorded identity of a directly started QEMU child.
// Version 0 (absent) is the legacy plain-PID file; version 2 records the
// binary and start identity so a reused PID cannot be mistaken for our child.
type pidFileData struct {
	Version int    `json:"version,omitempty"`
	PID     int    `json:"pid"`
	Binary  string `json:"binary,omitempty"`
	Start   string `json:"start,omitempty"`
}

// Process identity readers. The defaults are the platform-native probes in
// probe_linux.go / probe_darwin.go; the seams exist so tests can fake
// identity without real processes. There is no cmdline matching anywhere:
// the executable evidence is the kernel-resolved binary (Linux
// /proc/pid/exe, macOS kern.proc comm), never an argument string that could
// merely mention QEMU.
var (
	procExe     = nativeExeID
	procStartID = nativeStartID
	// procSignal0 asks the kernel whether pid exists, without touching it.
	// Sealed with the identity readers so tests can pin ESRCH (gone) versus
	// EPERM (alive but uninspectable) without real foreign processes.
	procSignal0 = func(pid int) error {
		process, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		return process.Signal(syscall.Signal(0))
	}
)

// SetProcProbes overrides the identity readers (for tests). A nil slot keeps
// the real reader for that slot; there is no restore-to-real except passing
// the saved originals back, so tests must capture and restore them.
func SetProcProbes(exe func(pid int) (string, error), startID func(pid int) (string, error)) {
	if exe != nil {
		procExe = exe
	}
	if startID != nil {
		procStartID = startID
	}
}

// pidState is what a recorded pid is proven to be. Unknown is distinct from
// absent: an unverifiable live process is never reported gone (a second QEMU
// on the same disk would corrupt it) and never signalled (it may be
// somebody else's process).
type pidState int

const (
	// pidAbsent: no such process, or a zombie (dead but unreaped).
	pidAbsent pidState = iota
	// pidOwned: proven to still be the recorded child.
	pidOwned
	// pidAlien: proven NOT to be the recorded child (a reused PID).
	pidAlien
	// pidUnknown: alive, but identity is unverifiable on this platform.
	pidUnknown
)

func (s pidState) String() string {
	switch s {
	case pidOwned:
		return "owned"
	case pidAlien:
		return "alien"
	case pidUnknown:
		return "unknown"
	default:
		return "absent"
	}
}

// buildPIDRecord builds the ownership record for a child this process just
// started, returning the parsed record and its exact on-disk bytes. The
// start identity is mandatory, not best-effort: a record without a
// lifetime token can never be proven owned (see classifyPID), so silently
// omitting it would start a VM nobody can stop by name. A probe failure
// fails the build and the caller reaps the child instead of orphaning it.
func buildPIDRecord(pid int, binary string) (pidFileData, []byte, error) {
	var zero pidFileData
	start, err := procStartID(pid)
	if err != nil {
		return zero, nil, fmt.Errorf("reading the child's start identity (pid %d): %w", pid, err)
	}
	if start == "" {
		return zero, nil, fmt.Errorf("reading the child's start identity (pid %d): empty start token", pid)
	}
	data := pidFileData{Version: 2, PID: pid, Binary: binary, Start: start}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return zero, nil, err
	}
	return data, append(raw, '\n'), nil
}

// writePIDRecord writes already-built record bytes to the pid file.
func writePIDRecord(vmDir string, raw []byte) error {
	return os.WriteFile(filepath.Join(vmDir, pidFile), raw, 0o644)
}

// writePIDFile records pid plus the child identity available at start time.
// It builds and writes in one step; startDirectLocked uses the pieces to
// capture its ownership token from what it wrote.
func writePIDFile(vmDir string, pid int, binary string) error {
	_, raw, err := buildPIDRecord(pid, binary)
	if err != nil {
		return err
	}
	return writePIDRecord(vmDir, raw)
}

// verifyPIDRecord proves the pid file holds exactly the bytes this launch
// wrote. Under the caller's lifecycle lock no other lifecycle operation
// can interleave, so anything else on disk — unreadable, replaced, or a
// write that landed nowhere — fails the launch rather than the ownership.
func verifyPIDRecord(vmDir string, raw []byte) error {
	cur, err := os.ReadFile(filepath.Join(vmDir, pidFile))
	if err != nil {
		return fmt.Errorf("pid record unreadable after start: %w", err)
	}
	if !bytes.Equal(cur, raw) {
		return fmt.Errorf("pid record on disk is not what this launch wrote (replaced, or a write that landed nowhere)")
	}
	return nil
}

// readPIDFile parses both the v2 JSON file and the legacy plain-PID file.
func readPIDFile(name string) (pidFileData, error) {
	var data pidFileData
	raw, err := os.ReadFile(filepath.Join(VMHome(), name, pidFile))
	if err != nil {
		return data, err
	}
	if json.Unmarshal(raw, &data) == nil && data.PID > 0 {
		return data, nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return pidFileData{}, fmt.Errorf("unreadable pid file")
	}
	return pidFileData{PID: pid}, nil
}

// exeIDMatches compares native executable evidence against the wanted binary.
// "exe:" evidence (Linux) is a resolved absolute path and must equal the
// resolved wanted binary exactly. "comm:" evidence (macOS) is the kernel's
// basename, truncated to MAXCOMLEN (16 bytes including the NUL — 15 visible
// characters), and must equal the wanted basename under the same truncation.
// A truncated basename is corroboration only, never identity on its own: any
// two binaries sharing a 15-byte prefix match it, so classifyPID requires
// the start dimension alongside.
func exeIDMatches(id, want string) bool {
	if got, ok := strings.CutPrefix(id, "exe:"); ok {
		wantResolved := want
		if r, err := filepath.EvalSymlinks(want); err == nil {
			wantResolved = r
		}
		return got == wantResolved && got != ""
	}
	if comm, ok := strings.CutPrefix(id, "comm:"); ok {
		base := want
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if len(base) > 15 {
			base = base[:15]
		}
		return comm != "" && comm == base
	}
	return false
}

// classifyPID pins a live pid against the recorded child identity. Both
// recorded dimensions must agree for owned (AND-admit): the start token is
// the process-lifetime identity, and the executable corroborates it. Any
// dimension that disagrees proves alien; any recorded dimension that cannot
// be verified proves nothing and the pid is unknown — an exe match with an
// unavailable start token is a reused PID running the same binary until
// proven otherwise, never owned. Signal-0 liveness alone never proves
// ownership, and only ESRCH (or an already-reaped handle) proves absence:
// EPERM means the process exists but is uninspectable, which is unknown.
func classifyPID(pid int, recorded pidFileData, launchBinary string) pidState {
	// Signal 0 asks the kernel whether the process exists without touching it.
	if err := procSignal0(pid); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return pidAbsent
		}
		return pidUnknown
	}
	if nativeIsZombie(pid) {
		return pidAbsent
	}
	// Lifetime dimension: mandatory when recorded. A mismatch proves a
	// reused PID; an unreadable token proves nothing.
	startProven := false
	if recorded.Start != "" {
		current, err := procStartID(pid)
		if err != nil || current == "" {
			return pidUnknown
		}
		if current != recorded.Start {
			return pidAlien
		}
		startProven = true
	}
	// Executable dimension: mandatory when a binary is expected (recorded,
	// else the launch record's fallback). A mismatch proves alien even for
	// legacy records that carry no lifetime token.
	want := recorded.Binary
	if want == "" {
		want = launchBinary
	}
	if want == "" {
		if startProven {
			return pidOwned
		}
		return pidUnknown
	}
	id, err := procExe(pid)
	if err != nil || id == "" {
		return pidUnknown
	}
	if !exeIDMatches(id, want) {
		return pidAlien
	}
	if !startProven {
		// Lifetime was never recorded (legacy plain-PID file): the exe
		// match alone cannot distinguish our child from a reused PID
		// running the same binary — another VM's QEMU matches too.
		return pidUnknown
	}
	return pidOwned
}

// directState returns the recorded pid, its record, and what the pid is
// proven to be. A missing pid file is absent (nothing was ever recorded).
// An unreadable or malformed pid file is unknown, never absent: uncertainty
// about the record is not proof that no child exists.
func directState(name string) (int, pidFileData, pidState) {
	recorded, err := readPIDFile(name)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, pidFileData{}, pidAbsent
		}
		return 0, pidFileData{}, pidUnknown
	}
	launchBinary := ""
	if cmd, err := readLaunchCommand(name); err == nil {
		launchBinary = cmd.Binary
	}
	return recorded.PID, recorded, classifyPID(recorded.PID, recorded, launchBinary)
}

// directPID returns the recorded pid and whether that process is proven to
// still be the recorded child. Anything else — absent, alien, or unknown —
// is reported not-alive: unknown is fail-closed for liveness (readiness
// will not declare a VM ready on an unverified process) while start and
// stop consult directState for the full distinction.
func directPID(name string) (int, bool) {
	pid, _, st := directState(name)
	return pid, st == pidOwned
}

// ProvenStopped reports whether the VM is proven to have no live QEMU:
// no owned or unverifiable direct child, and (where a user session exists)
// a unit proven stopped. It is the gate for destructive same-name
// operations — --force recreation refuses unless this is true. Callers that
// need the answer atomic against concurrent start/stop hold the lifecycle
// lock (see lifecycle_lock.go); ProvenStopped itself takes no lock.
//
// Where no session is claimed, only a PROVEN-ABSENT manager proves no unit
// exists: an unanswered manager (unknown) never reports stopped.
func ProvenStopped(name string) bool {
	_, _, st := directState(name)
	switch st {
	case pidOwned, pidUnknown:
		return false
	case pidAbsent, pidAlien:
	}
	if userSystemd() {
		svc := "corral-" + name
		return unitProvenStopped(svc, filepath.Join(systemdUserDir(), svc+".service"))
	}
	return managerProvenAbsent()
}

// unitProvenStopped reports whether a systemd unit is proven stopped. Only
// positively stopped states count: active and every transitional state
// (activating, deactivating, reloading) are not stopped, and an unparsable
// answer or a query failure is uncertainty, never proof. The one exception
// is "unknown" with no unit file on disk — systemctl answers unknown for a
// unit that was never installed, and nothing that was never installed can
// be running. "Unknown" with a unit file present stays unproven: the file
// exists but the manager would not say what it is doing.
func unitProvenStopped(svc, unitPath string) bool {
	out, err := systemctlRun(context.Background(), "is-active", svc)
	switch state := strings.TrimSpace(string(out)); state {
	case "active", "activating", "deactivating", "reload", "reloading":
		return false
	case "inactive", "failed", "dead":
		return true
	case "unknown":
		if err != nil {
			if _, serr := os.Stat(unitPath); os.IsNotExist(serr) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// ValidateCommand lives in validate.go: it proves the recorded QEMU command
// is still the admitted one (binary, argv, metadata, unit) before start.

// stopDirectLocked terminates a directly started VM: ask, then insist. Every
// signal and every completion check governs the exact pinned (pid, record)
// pair captured at entry — the pid file is never re-read by name mid-stop,
// so a concurrent replacement cannot redirect signals at a new VM. An
// unverifiable pid is refused, never signalled. The caller holds the
// lifecycle lock for name (see lifecycle_lock.go).
func stopDirectLocked(name string) error {
	path := filepath.Join(VMHome(), name, pidFile)
	// The ownership record as captured at entry: no branch below removes
	// the pid file without proving these exact bytes are still there, so a
	// replacement record written by another invocation is never erased.
	wantRaw, _ := os.ReadFile(path)
	// The pid is READ (not verified) first: the lifetime handle below is
	// acquired for the recorded pid before any verification runs, so the
	// escalation can only ever signal the process the handle pins — never
	// a pid re-looked-up after classification. (This duplicates
	// directState's missing/malformed split deliberately: directState
	// classifies, and classification must come after acquisition.)
	recorded, rerr := readPIDFile(name)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			removePIDRecord(path, wantRaw)
			return nil
		}
		return fmt.Errorf("VM %q has an unreadable pid file — refusing to signal an unverified process (inspect %s, stop any stale QEMU manually, then remove the file)", name, path)
	}
	launchBinary := recorded.Binary
	if launchBinary == "" {
		if cmd, err := readLaunchCommand(name); err == nil {
			launchBinary = cmd.Binary
		}
	}
	// Acquire the lifetime handle BEFORE verifying (see pidhandle.go). A
	// handle that cannot be acquired refuses the stop — without a bound
	// target there is nothing proven-safe to signal. The handle is
	// retained across the whole escalation below and released at the end.
	h, err := procAcquire(recorded.PID)
	if err != nil {
		return fmt.Errorf("VM %q (pid %d): cannot bind a signal target for the recorded process (%v) — refusing to signal an unverified process (pid file kept)", name, recorded.PID, err)
	}
	defer h.Release()
	pid := recorded.PID
	// pinned re-checks the captured identity, never the name.
	pinned := func() pidState { return classifyPID(pid, recorded, launchBinary) }
	switch pinned() {
	case pidAbsent, pidAlien:
		removePIDRecord(path, wantRaw)
		return nil
	case pidUnknown:
		return fmt.Errorf("VM %q (pid %d) cannot be proven to be this VM's QEMU — refusing to signal an unverified process (inspect pid %d, stop it manually if it is stale, then remove %s)",
			name, pid, pid, path)
	case pidOwned:
	}
	return stopPinnedLocked(name, path, pid, pinned, h.Signal, wantRaw)
}

// Escalation waits: how long a TERM'd child gets to exit before SIGKILL,
// how long the SIGKILL is given to take effect, and how often the pinned
// identity is re-checked while waiting. Variables so tests can shrink
// them; production values are generous because a guest that ignores ACPI
// shutdown is normal, not exceptional.
var (
	stopTermWait     = 20 * time.Second
	stopKillWait     = 5 * time.Second
	stopPollInterval = 250 * time.Millisecond
)

// stopPinnedLocked runs the TERM → wait → KILL → verify escalation pinned to
// one (pid, record) identity captured by the caller. Shared by stopDirectLocked
// and token-owned stops (see ownership.go), which pin to a start token instead
// of a fresh name lookup. The caller holds the lifecycle lock for name.
func stopPinnedLocked(name, path string, pid int, pinned func() pidState, signal func(os.Signal) error, wantRaw []byte) error {
	if err := signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		if errors.Is(err, syscall.ESRCH) {
			// The pid was provably empty at signal time, so the recorded
			// child is dead — whatever (if anything) sits at that pid now
			// arrived after. Its record is stale.
			removePIDRecord(path, wantRaw)
			return nil
		}
		switch pinned() {
		case pidAbsent, pidAlien:
			removePIDRecord(path, wantRaw)
			return nil
		case pidUnknown:
			return fmt.Errorf("stopping QEMU (pid %d): signal failed (%v) and the process can no longer be proven to be VM %q's — refusing to escalate against an unverified process (pid file kept)", pid, err, name)
		case pidOwned:
			return fmt.Errorf("stopping QEMU (pid %d): %w", pid, err)
		}
	}
	deadline := time.Now().Add(stopTermWait)
	for time.Now().Before(deadline) {
		switch pinned() {
		case pidAbsent, pidAlien:
			removePIDRecord(path, wantRaw)
			return nil
		case pidOwned, pidUnknown:
		}
		time.Sleep(stopPollInterval)
	}
	// Escalation requires proven ownership HERE, not at entry: the original
	// child may have exited and the pid been reused (or identity become
	// unavailable) during the wait, and neither authorizes a SIGKILL.
	// A guest that ignores ACPI shutdown is normal, not exceptional — but
	// only a proven child is ever insisted upon.
	if pinned() != pidOwned {
		return fmt.Errorf("VM %q (pid %d) can no longer be proven to be this VM's QEMU — refusing to SIGKILL an unverified process (pid file kept)", name, pid)
	}
	_ = signal(syscall.SIGKILL)
	// Verified exit: the pid file must not be removed while the process is
	// still alive, or the VM would be orphaned under a reused PID.
	killDeadline := time.Now().Add(stopKillWait)
	for time.Now().Before(killDeadline) {
		switch pinned() {
		case pidAbsent, pidAlien:
			removePIDRecord(path, wantRaw)
			return nil
		case pidOwned, pidUnknown:
		}
		time.Sleep(stopPollInterval)
	}
	return fmt.Errorf("QEMU (pid %d) still alive after SIGKILL — pid file kept", pid)
}

// removePIDRecord removes the pid file only if it still holds the exact
// ownership bytes captured at entry. A missing file is already gone
// (success); a REPLACED file belongs to another invocation's VM and is
// kept. An unreadable file is kept: nothing provably ours to remove.
func removePIDRecord(path string, want []byte) {
	cur, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if !bytes.Equal(cur, want) {
		return
	}
	_ = os.Remove(path)
}

// lastLines is the tail of a QEMU log, for an error message.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// IsRunning reports whether a VM's QEMU process is up, whichever way it was
// started. The pid file is checked first: a VM started directly has no unit,
// and `systemctl --user is-active` on a runner with no session answers "no"
// for every VM including the ones that are running.
func IsRunning(name string) bool {
	if _, alive := directPID(name); alive {
		return true
	}
	if !userSystemd() {
		return false
	}
	out, _ := systemctlRun(context.Background(), "is-active", "corral-"+name)
	return strings.TrimSpace(string(out)) == "active"
}

// IsRunningCtx is IsRunning bounded by ctx, with no spawned goroutine and no
// abandoned subprocess. Cancellation reports not-running — the readiness
// loop treats that as "probe again" and then observes the cancelled context
// itself, so the wait ends interrupted, never ready. The caller's context
// flows into the subprocess (see bounded.go): a mid-probe cancel kills the
// systemctl group within milliseconds — a hung manager stalls at most until
// the caller gives up (or the default bound, whichever fires first), and
// the probe is slow in that case, never leaked.
func IsRunningCtx(ctx context.Context, name string) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	// The local record first: instant, no subprocess. (An unverifiable
	// record is fail-closed not-alive here; start and stop consult
	// directState for the full distinction.)
	if _, alive := directPID(name); alive {
		return true
	}
	if err := ctx.Err(); err != nil {
		return false
	}
	// The session probe is skipped deliberately: is-active is authoritative
	// (a missing session answers non-active), it costs one spawn instead of
	// two, and a wedged manager stalls one bounded call, not two.
	out, err := systemctlRun(ctx, "is-active", "corral-"+name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}
