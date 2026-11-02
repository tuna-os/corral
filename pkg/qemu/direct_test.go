package qemu

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/testenv"
	"github.com/tuna-os/corral/pkg/types"
)

// directVM points the package at a throwaway state directory, says there is no
// systemd user session, and returns the VM's directory.
func directVM(t *testing.T, name string) string {
	t.Helper()
	// Native macOS TMPDIR plus the test name exceeds AF_UNIX's path limit.
	home, err := os.MkdirTemp("/tmp", "corral-qmp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
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
	// Use native QEMU, not a sleep/stub process: Stop must identify the
	// QMP endpoint and retire the process that owns it.
	binary, err := exec.LookPath(qemuSystemBinary(""))
	if err != nil {
		testenv.Skip(t, "qemu", err.Error())
	}
	args := []string{"-name", "gate", "-machine", "none", "-S", "-display", "none",
		"-qmp", "unix:" + filepath.Join(dir, "qmp.sock") + ",server=on,wait=off"}
	if err := writeLaunchCommand(dir, binary, args); err != nil {
		t.Fatal(err)
	}

	if err := Start("gate"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if pid, alive := directPID("gate"); alive {
			process, _ := os.FindProcess(pid)
			_ = process.Kill()
		}
	})
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

func TestStopDirect_RejectsForeignPIDAndPreservesDisk(t *testing.T) {
	dir := directVM(t, "foreign")
	child := exec.Command("sleep", "120")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	path := filepath.Join(dir, pidFile)
	if err := os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(dir, "disk.qcow2")
	if err := os.WriteFile(disk, []byte("owned-disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Stop("foreign"); err == nil {
		t.Fatal("Stop accepted an unverified PID")
	}
	if err := Delete("foreign"); err == nil {
		t.Fatal("Delete ignored the failed stop")
	}
	if _, alive := directPID("foreign"); !alive {
		t.Fatal("an unrelated process was signalled")
	}
	for _, retained := range []string{path, disk} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("failed cleanup discarded %s: %v", retained, err)
		}
	}
}

func TestStopDirect_RejectsOtherQEMUName(t *testing.T) {
	dir := directVM(t, "expected")
	binary, err := exec.LookPath(qemuSystemBinary(""))
	if err != nil {
		testenv.Skip(t, "qemu", err.Error())
	}
	args := []string{"-name", "different", "-machine", "none", "-S", "-display", "none",
		"-qmp", "unix:" + filepath.Join(dir, "qmp.sock") + ",server=on,wait=off"}
	if err := writeLaunchCommand(dir, binary, args); err != nil {
		t.Fatal(err)
	}
	if err := Start("expected"); err != nil {
		t.Fatal(err)
	}
	pid, _ := directPID("expected")
	t.Cleanup(func() {
		process, _ := os.FindProcess(pid)
		_ = process.Kill()
	})
	if err := Stop("expected"); err == nil {
		t.Fatal("Stop quit a different QEMU")
	}
	if _, alive := directPID("expected"); !alive {
		t.Fatal("the other QEMU was stopped")
	}
}

func TestStartDirect_PIDWriteFailureRetiresChild(t *testing.T) {
	dir := directVM(t, "record-failure")
	binary, err := exec.LookPath(qemuSystemBinary(""))
	if err != nil {
		testenv.Skip(t, "qemu", err.Error())
	}
	socket := filepath.Join(dir, "qmp.sock")
	args := []string{"-name", "record-failure", "-machine", "none", "-S", "-display", "none",
		"-qmp", "unix:" + socket + ",server=on,wait=off"}
	if err := writeLaunchCommand(dir, binary, args); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, pidFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Start("record-failure"); err == nil || !strings.Contains(err.Error(), "recording the QEMU pid") {
		t.Fatalf("expected PID recording failure, got %v", err)
	}
	if conn, err := net.DialTimeout("unix", socket, time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("untracked QEMU child retained a listener")
	}
}

func TestStartDirect_ReportsAProcessThatDiesAtOnce(t *testing.T) {
	// Re-execute this native test binary as a genuinely failing child. Record
	// its PID separately so removal of corral's PID file cannot hide a zombie.
	const childPIDEnv = "CORRAL_TEST_FAILING_CHILD_PID"
	if path := os.Getenv(childPIDEnv); path != "" {
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(43)
		}
		_, _ = os.Stderr.WriteString("corral failing child\n")
		os.Exit(42)
	}
	dir := directVM(t, "gate")
	childPIDPath := filepath.Join(dir, "failed-child.pid")
	t.Setenv(childPIDEnv, childPIDPath)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestStartDirect_ReportsAProcessThatDiesAtOnce$"}
	if err := writeLaunchCommand(dir, binary, args); err != nil {
		t.Fatal(err)
	}
	err = Start("gate")
	if err == nil {
		t.Fatal("expected an error for a process that exited at once")
	}
	for _, want := range []string{"exited immediately", "exit status 42", "corral failing child"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
	raw, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		t.Fatalf("invalid failed-child PID %q: %v", raw, err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("failed child %d is still running or unreaped", pid)
	}
	if _, err := os.Stat(filepath.Join(dir, pidFile)); !os.IsNotExist(err) {
		t.Fatalf("failed child retained corral's PID file: %v", err)
	}
	if IsRunning("gate") {
		t.Fatal("failed child is reported as a running VM")
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
	args := strings.Join(accelArgs(4, ""), " ")
	if runtime.GOOS == "darwin" {
		if !strings.Contains(args, "accel=hvf") || !strings.Contains(args, "-cpu host") {
			t.Errorf("on macOS: %s", args)
		}
	} else if device, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
		_ = device.Close()
		if !strings.Contains(args, "accel=kvm") || !strings.Contains(args, "-cpu host") {
			t.Errorf("with caller-accessible KVM: %s", args)
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

func TestStartDirect_LoopbackCreateDoesNotExposeTailnet(t *testing.T) {
	directory := directVM(t, "loopback")
	for _, executable := range []string{"qemu-system-x86_64", "qemu-img"} {
		if _, err := exec.LookPath(executable); err != nil {
			testenv.Skip(t, "qemu", err.Error())
		}
	}
	// Supply a real executable reporting an active tailnet. Local-only create
	// must not discover/use it; the actual QEMU console still has to bind.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tailscale"), []byte("#!/bin/sh\nprintf '203.0.113.1\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := Create(types.CreateOpts{Name: "loopback", Arch: types.ArchAMD64,
		Loopback: true, Force: true, Disk: "64M", Mem: "128M", CPU: 1}); err != nil {
		t.Fatal(err)
	}
	meta, err := readMetadata("loopback")
	if err != nil || meta.Tailscale != "127.0.0.1" {
		t.Fatalf("local bind metadata = %+v, error %v", meta, err)
	}
	if err := Start("loopback"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Stop("loopback") })
	address, err := VNCAddr("loopback")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.SetReadDeadline(time.Now().Add(time.Second))
	greeting := make([]byte, 12)
	_, err = io.ReadFull(connection, greeting)
	_ = connection.Close()
	if err != nil || !strings.HasPrefix(string(greeting), "RFB ") {
		t.Fatalf("actual local QEMU did not offer RFB: %q, %v", greeting, err)
	}
	// Two hundred real QMP send-key requests take over six seconds at the
	// console's normal pacing. A deadline fixed at qmpDial would expire
	// midway through this single connection, despite each request succeeding.
	typingStarted := time.Now()
	if err := SendKeys("loopback", strings.Repeat("Ab9!", 50)); err != nil {
		t.Fatalf("typing 200 characters through real QEMU QMP: %v", err)
	}
	if elapsed := time.Since(typingStarted); elapsed <= 5*time.Second {
		t.Fatalf("typing did not exercise a connection older than five seconds: %s", elapsed)
	}
	if err := Stop("loopback"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, pidFile)); !os.IsNotExist(err) {
		t.Fatalf("direct QEMU PID retained after retirement: %v", err)
	}
	// Refusing --force architecture changes must precede even disk creation
	// or firmware discovery. Preserve an actual qcow2 plus guest-owned vars
	// and metadata, not just the error returned by the guard.
	if err := os.WriteFile(filepath.Join(directory, "vars.fd"), []byte("guest-owned UEFI variables"), 0o600); err != nil {
		t.Fatal(err)
	}
	preserved := make(map[string][]byte)
	for _, name := range []string{"disk.qcow2", "vars.fd", "metadata.json"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		preserved[name] = data
	}
	err = Create(types.CreateOpts{Name: "loopback", Arch: types.ArchARM64,
		Loopback: true, Force: true, UEFI: true, Disk: "128M", Mem: "128M", CPU: 1})
	if err == nil || !strings.Contains(err.Error(), "has architecture amd64, not arm64") {
		t.Fatalf("--force did not refuse changing the known guest architecture: %v", err)
	}
	for name, original := range preserved {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Errorf("architecture refusal lost %s: %v", name, err)
		} else if !bytes.Equal(data, original) {
			t.Errorf("architecture refusal modified %s", name)
		}
	}
}
