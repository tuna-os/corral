package qemu

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// TPMDir is where a VM's swtpm state/socket live.
func TPMDir(name string) string { return filepath.Join(VMHome(), name, "swtpm") }

// TPMSock is the swtpm unix socket path.
func TPMSock(name string) string { return filepath.Join(TPMDir(name), "swtpm.sock") }

// TPMPidFile is the pid file for the per-VM swtpm daemon.
func TPMPidFile(name string) string { return filepath.Join(TPMDir(name), "swtpm.pid") }

// TPMArgs returns QEMU args wiring the emulated TPM via swtpm socket.
func TPMArgs(name string) []string {
	sock := TPMSock(name)
	return []string{
		"-chardev", fmt.Sprintf("socket,id=chrtpm,path=%s", sock),
		"-tpmdev", "emulator,id=tpm0,chardev=chrtpm",
		"-device", "tpm-crb,tpmdev=tpm0",
	}
}

// TPMHostAvailable reports whether swtpm is installed.
func TPMHostAvailable() (bool, string) {
	if _, err := exec.LookPath("swtpm"); err != nil {
		return false, "swtpm not found (install swtpm package)"
	}
	return true, ""
}

// EnsureTPMDir ensures the per-VM TPM state dir exists, preserving state
// unless keep==false. Mirrors iso-e2e.sh start_swtpm keep semantics.
func EnsureTPMDir(name string, keep bool) error {
	dir := TPMDir(name)
	if !keep {
		_ = os.RemoveAll(dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return nil
}

// StartSwtpm launches the per-VM swtpm daemon. Idempotent if already alive.
func StartSwtpm(name string) error {
	if pid, alive := tpmPID(name); alive {
		_ = pid
		return nil
	}
	if _, err := exec.LookPath("swtpm"); err != nil {
		return fmt.Errorf("swtpm not found: install the 'swtpm' package")
	}
	dir := TPMDir(name)
	_ = os.MkdirAll(dir, 0o755)
	sock := TPMSock(name)
	pidFile := TPMPidFile(name)
	_ = os.Remove(sock)
	_ = os.Remove(pidFile)
	cmd := exec.Command("swtpm", "socket",
		"--tpmstate", "dir="+dir,
		"--ctrl", "type=unixio,path="+sock,
		"--tpm2",
		"--flags", "startup-clear",
		"--daemon",
		"--pid", "file="+pidFile,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("swtpm: %s: %w", string(out), err)
	}
	// Wait for socket.
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("swtpm socket did not appear at %s", sock)
}

// StopSwtpm terminates the per-VM swtpm daemon.
func StopSwtpm(name string) error {
	pid, alive := tpmPID(name)
	if !alive {
		_ = os.Remove(TPMPidFile(name))
		_ = os.Remove(TPMSock(name))
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	_ = proc.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, alive := tpmPID(name); !alive {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, alive := tpmPID(name); alive {
		_ = proc.Signal(syscall.SIGKILL)
	}
	_ = os.Remove(TPMPidFile(name))
	// Keep state dir for reuse across boots, like iso-e2e's keep mode; socket is stale.
	_ = os.Remove(TPMSock(name))
	return nil
}

func tpmPID(name string) (int, bool) {
	data, err := os.ReadFile(TPMPidFile(name))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return pid, false
	}
	if state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if fields := strings.Fields(string(state)); len(fields) > 2 && fields[2] == "Z" {
			return pid, false
		}
	}
	return pid, true
}
