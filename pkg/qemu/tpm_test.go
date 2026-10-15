package qemu

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeSwtpm puts a "swtpm" on PATH that behaves like `swtpm socket --daemon`:
// it creates the control socket path and the pid file, leaves a background
// process named swtpm running, and logs each invocation. The kernel names a
// script's process after the script, so the daemon's comm really is "swtpm".
func fakeSwtpm(t *testing.T, exitCode string) (logPath string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc to name the daemon process")
	}
	bin := t.TempDir()
	logPath = filepath.Join(bin, "invocations")
	script := `#!/bin/sh
echo "$@" >> "` + logPath + `"
if [ "` + exitCode + `" != 0 ]; then echo "swtpm: TPM state is locked" >&2; exit ` + exitCode + `; fi
while [ $# -gt 0 ]; do
  case "$1" in
    --ctrl) sock="${2#*path=}"; shift ;;
    --pid) pid="${2#file=}"; shift ;;
  esac
  shift
done
: > "$sock"
( trap 'exit 0' TERM; while :; do sleep 0.1; done ) >/dev/null 2>&1 &
echo $! > "$pid"
`
	if err := os.WriteFile(filepath.Join(bin, "swtpm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func withVMHome(t *testing.T) {
	t.Helper()
	SetStateDirs(t.TempDir(), t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
}

func invocations(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

func TestSwtpm_StartIsIdempotentAndStopEndsTheDaemon(t *testing.T) {
	log := fakeSwtpm(t, "0")
	withVMHome(t)
	t.Cleanup(func() { _ = StopSwtpm("win") })

	if err := StartSwtpm("win"); err != nil {
		t.Fatalf("StartSwtpm: %v", err)
	}
	pid, alive := tpmPID("win")
	if !alive {
		t.Fatalf("daemon not running after start (pid %d)", pid)
	}
	args := strings.Fields(strings.TrimSpace(mustRead(t, log)))
	for _, want := range []string{"socket", "--tpm2", "--daemon", "dir=" + TPMDir("win"), "type=unixio,path=" + TPMSock("win")} {
		if !slices.Contains(args, want) {
			t.Errorf("swtpm args %v missing %q", args, want)
		}
	}

	if err := StartSwtpm("win"); err != nil {
		t.Fatalf("second StartSwtpm: %v", err)
	}
	if n := invocations(t, log); n != 1 {
		t.Errorf("swtpm launched %d times; a running daemon must be reused", n)
	}

	if err := os.WriteFile(filepath.Join(TPMDir("win"), "tpm2-00.permall"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StopSwtpm("win"); err != nil {
		t.Fatalf("StopSwtpm: %v", err)
	}
	if _, alive := tpmPID("win"); alive {
		t.Error("daemon still alive after stop")
	}
	if err := syscall.Kill(pid, 0); err == nil && !isZombie(pid) {
		t.Errorf("swtpm pid %d still running after stop", pid)
	}
	for _, gone := range []string{TPMPidFile("win"), TPMSock("win")} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s left behind after stop", gone)
		}
	}
	// The TPM state is the guest's (BitLocker keys, measured boot): keep it.
	if _, err := os.Stat(filepath.Join(TPMDir("win"), "tpm2-00.permall")); err != nil {
		t.Errorf("stop discarded the TPM state: %v", err)
	}
}

// After a crash or a host reboot the pid file can name a PID that now
// belongs to some unrelated process. Stopping the VM must not kill it.
func TestStopSwtpm_StalePidFileNeverSignalsAnotherProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on /proc to name the process")
	}
	withVMHome(t)
	bystander := exec.Command("sleep", "30")
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = bystander.Wait(); close(exited) }()
	t.Cleanup(func() { _ = bystander.Process.Kill(); <-exited })

	if err := EnsureTPMDir("win", true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(TPMPidFile("win"), []byte(strconv.Itoa(bystander.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, alive := tpmPID("win"); alive {
		t.Error("tpmPID took an unrelated live process for swtpm")
	}
	if err := StopSwtpm("win"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		t.Fatal("StopSwtpm killed an unrelated process named by a stale pid file")
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := os.Stat(TPMPidFile("win")); !os.IsNotExist(err) {
		t.Error("stale pid file not cleared")
	}
}

func TestStartSwtpm_Failures(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		withVMHome(t)
		t.Setenv("PATH", t.TempDir())
		if err := StartSwtpm("win"); err == nil || !strings.Contains(err.Error(), "swtpm not found") {
			t.Errorf("err = %v, want an install hint", err)
		}
		if ok, why := TPMHostAvailable(); ok || !strings.Contains(why, "swtpm not found") {
			t.Errorf("TPMHostAvailable = %v, %q", ok, why)
		}
	})
	t.Run("daemon fails", func(t *testing.T) {
		fakeSwtpm(t, "1")
		withVMHome(t)
		err := StartSwtpm("win")
		if err == nil || !strings.Contains(err.Error(), "TPM state is locked") {
			t.Errorf("err = %v, want swtpm's own message", err)
		}
	})
}

func TestEnsureTPMDir_KeepPreservesState(t *testing.T) {
	withVMHome(t)
	if err := EnsureTPMDir("win", true); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(TPMDir("win"), "tpm2-00.permall")
	if err := os.WriteFile(state, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTPMDir("win", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); err != nil {
		t.Errorf("keep=true discarded state: %v", err)
	}
	if err := EnsureTPMDir("win", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Error("keep=false kept the old TPM state")
	}
	if info, err := os.Stat(TPMDir("win")); err != nil || !info.IsDir() {
		t.Errorf("TPM dir missing after reset: %v", err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func isZombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true // gone
	}
	f := strings.Fields(string(b))
	return len(f) > 2 && f[2] == "Z"
}
