package qemu

import (
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

// userSystemd reports whether a systemd user session can run units here.
//
// Asked rather than assumed: the answer differs between a workstation, a
// container, and a sudo shell on a CI runner, and getting it wrong means either
// a VM that never starts or one nothing is supervising.
var userSystemd = func() bool {
	out, err := systemctlRun("is-system-running")
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
			return false
		}
	}
	if err == nil {
		return true
	}
	// A degraded or still-starting manager exits nonzero and is perfectly able
	// to run a unit.
	for _, state := range []string{"running", "degraded", "starting", "maintenance", "stopping"} {
		if strings.Contains(text, state) {
			return true
		}
	}
	return false
}

// SetUserSystemd overrides the systemd-session probe (for tests).
func SetUserSystemd(f func() bool) {
	if f == nil {
		return
	}
	userSystemd = f
}

// startDirect launches QEMU as a detached process and records its pid.
func startDirect(name string) error {
	if pid, alive := directPID(name); alive {
		fmt.Fprintf(os.Stderr, "VM %q is already running (pid %d).\n", name, pid)
		return nil
	}
	command, err := readLaunchCommand(name)
	if err != nil {
		return err
	}
	vmDir := filepath.Join(VMHome(), name)
	logPath := filepath.Join(vmDir, directLog)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", logPath, err)
	}
	// The QEMU child holds its own descriptor, so this close only releases
	// ours; nothing reads the result.
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(command.Binary, command.Args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	// Its own session, so the VM outlives the shell that started it — which is
	// the one thing the systemd unit was doing for us.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting QEMU: %w (see %s)", err, logPath)
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(filepath.Join(vmDir, pidFile), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return fmt.Errorf("recording the QEMU pid: %w", err)
	}

	// Reap it in the background rather than detaching. Nothing here waits for
	// the VM — the goroutine blocks for as long as the guest runs, and dies
	// with this process — but an unreaped child becomes a zombie, and a zombie
	// answers a liveness check as if it were still running. That would make a
	// QEMU which exited on a bad flag look like a VM that is simply slow.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// QEMU exits early on a bad flag or a missing accelerator, and a pid file
	// for a dead process is worse than an error.
	select {
	case waitErr := <-exited:
		_ = os.Remove(filepath.Join(vmDir, pidFile))
		output, _ := os.ReadFile(logPath)
		return fmt.Errorf("QEMU exited immediately (%v): %s", waitErr, lastLines(string(output), 10))
	case <-time.After(750 * time.Millisecond):
	}
	fmt.Fprintf(os.Stderr, "VM %q started (pid %d, no systemd user session — output in %s).\n", name, pid, logPath)
	return nil
}

// directPID returns the recorded pid and whether that process is alive.
func directPID(name string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(VMHome(), name, pidFile))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	// Signal 0 asks the kernel whether the process exists without touching it.
	if err := process.Signal(syscall.Signal(0)); err != nil {
		return pid, false
	}
	// A zombie still answers signal 0. Where /proc says so, say it is gone.
	if state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if fields := strings.Fields(string(state)); len(fields) > 2 && fields[2] == "Z" {
			return pid, false
		}
	}
	return pid, true
}

// stopDirect terminates a directly started VM: ask, then insist.
func stopDirect(name string) error {
	pid, alive := directPID(name)
	path := filepath.Join(VMHome(), name, pidFile)
	if !alive {
		_ = os.Remove(path)
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("stopping QEMU (pid %d): %w", pid, err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, alive := directPID(name); !alive {
			_ = os.Remove(path)
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	// A guest that ignores ACPI shutdown is normal, not exceptional.
	_ = process.Signal(syscall.SIGKILL)
	_ = os.Remove(path)
	return nil
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
	out, _ := systemctlRun("is-active", "corral-"+name)
	return strings.TrimSpace(string(out)) == "active"
}
