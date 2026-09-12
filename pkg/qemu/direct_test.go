package qemu

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// directVM points the package at a throwaway state directory, says there is no
// systemd user session, and returns the VM's directory.
func directVM(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	original := userSystemd
	SetUserSystemd(func() bool { return false })
	t.Cleanup(func() {
		SetStateDirs("", "")
		SetUserSystemd(original)
	})
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestUserSystemd_ReadsTheSessionsAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		out    string
		err    error
		wantOK bool
	}{
		{name: "running", out: "running\n", wantOK: true},
		// Degraded exits nonzero and can still run a unit.
		{name: "degraded", out: "degraded\n", err: errors.New("exit status 1"), wantOK: true},
		// The CI runner case: a healthy machine with no user manager.
		{name: "no bus", out: "Failed to connect to bus: No medium found\n", err: errors.New("exit status 1")},
		{name: "no systemctl", out: "", err: errors.New(`exec: "systemctl": executable file not found in $PATH`)},
		{name: "offline", out: "offline\n", err: errors.New("exit status 1")},
	} {
		original := userSystemd
		SetSystemctl(func(args ...string) ([]byte, error) {
			// The error text is what a caller sees when the binary is missing,
			// so it has to reach the probe.
			if tc.out == "" && tc.err != nil {
				return []byte(tc.err.Error()), tc.err
			}
			return []byte(tc.out), tc.err
		})
		got := userSystemd()
		SetSystemctl(nil)
		userSystemd = original
		if got != tc.wantOK {
			t.Errorf("%s: userSystemd() = %v, want %v", tc.name, got, tc.wantOK)
		}
	}
}

func TestLaunchCommandRoundTrip(t *testing.T) {
	dir := directVM(t, "gate")
	args := []string{"-name", "gate", "-m", "4G", "-serial", "chardev:serial0"}
	if err := writeLaunchCommand(dir, "/usr/bin/qemu-system-x86_64", args); err != nil {
		t.Fatalf("writeLaunchCommand: %v", err)
	}
	command, err := readLaunchCommand("gate")
	if err != nil {
		t.Fatalf("readLaunchCommand: %v", err)
	}
	if command.Binary != "/usr/bin/qemu-system-x86_64" {
		t.Errorf("binary = %q", command.Binary)
	}
	if strings.Join(command.Args, " ") != strings.Join(args, " ") {
		t.Errorf("args = %v", command.Args)
	}
}

func TestReadLaunchCommand_Missing(t *testing.T) {
	directVM(t, "gate")
	_, err := readLaunchCommand("gate")
	if err == nil {
		t.Fatal("expected an error for a VM with no recorded command")
	}
	// The remedy matters: an older VM has no command.json and the only fix is
	// to recreate it.
	if !strings.Contains(err.Error(), "recreate it") {
		t.Errorf("the error should say how to fix it: %v", err)
	}
}

func TestStartDirect_RunsAndStops(t *testing.T) {
	dir := directVM(t, "gate")
	// `sleep` stands in for QEMU: the harness only needs a process that stays
	// alive, holds a pid, and dies when signalled.
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	if err := writeLaunchCommand(dir, sleep, []string{"120"}); err != nil {
		t.Fatal(err)
	}

	if err := Start("gate"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid, alive := directPID("gate")
	if !alive || pid <= 0 {
		t.Fatalf("expected a live process, got pid %d alive %v", pid, alive)
	}
	if !IsRunning("gate") {
		t.Error("IsRunning should see a directly started VM")
	}
	// Starting twice must not spawn a second QEMU on the same disk.
	if err := Start("gate"); err != nil {
		t.Fatalf("Start (again): %v", err)
	}
	if again, _ := directPID("gate"); again != pid {
		t.Errorf("a second Start replaced pid %d with %d", pid, again)
	}

	if err := Stop("gate"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, alive := directPID("gate"); alive {
		t.Error("the process should be gone after Stop")
	}
	if _, err := os.Stat(filepath.Join(dir, pidFile)); !os.IsNotExist(err) {
		t.Errorf("the pid file should be removed: %v", err)
	}
	if IsRunning("gate") {
		t.Error("IsRunning should report a stopped VM as stopped")
	}
}

func TestStartDirect_ReportsAProcessThatDiesAtOnce(t *testing.T) {
	dir := directVM(t, "gate")
	// QEMU exits immediately on a bad flag. A pid file for a dead process
	// would make every later probe lie.
	if err := writeLaunchCommand(dir, "/bin/false", nil); err != nil {
		t.Fatal(err)
	}
	err := Start("gate")
	if err == nil {
		t.Fatal("expected an error for a process that exited at once")
	}
	if !strings.Contains(err.Error(), "exited immediately") {
		t.Errorf("error = %v", err)
	}
}

func TestStart_DirectPathNeedsTheVM(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	original := userSystemd
	SetUserSystemd(func() bool { return false })
	t.Cleanup(func() {
		SetStateDirs("", "")
		SetUserSystemd(original)
	})
	if err := Start("missing"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected a does-not-exist error, got %v", err)
	}
}

func TestAccelArgs(t *testing.T) {
	args := strings.Join(accelArgs(4), " ")
	if _, err := os.Stat("/dev/kvm"); err == nil {
		if !strings.Contains(args, "accel=kvm") || !strings.Contains(args, "-cpu host") {
			t.Errorf("with KVM present: %s", args)
		}
	} else {
		// The CI case. -cpu host cannot be modelled by TCG, so the pair has to
		// change together or the VM starts and then cannot find its CPU.
		if !strings.Contains(args, "accel=tcg") || !strings.Contains(args, "-cpu max") {
			t.Errorf("without KVM: %s", args)
		}
	}
	if !strings.Contains(args, "-smp 4") {
		t.Errorf("the vCPU count is missing: %s", args)
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\nd\n", 2); got != "c\nd" {
		t.Errorf("lastLines = %q", got)
	}
	if got := lastLines("only\n", 5); got != "only" {
		t.Errorf("lastLines = %q", got)
	}
}
