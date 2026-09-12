package qemu

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// probeVM points the package at a throwaway state directory and writes one
// VM's metadata, which is where the forwarded port comes from.
func probeVM(t *testing.T, name string, metadata string) string {
	t.Helper()
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if metadata != "" {
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(metadata), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSSHEndpoint(t *testing.T) {
	probeVM(t, "gate", `{"name":"gate","ssh_port":2242,"tailscale_ip":"100.64.0.5"}`)
	endpoint, err := SSHEndpoint("gate")
	if err != nil {
		t.Fatalf("SSHEndpoint: %v", err)
	}
	if endpoint.String() != "100.64.0.5:2242" {
		t.Errorf("endpoint = %s", endpoint)
	}
}

func TestSSHEndpoint_LoopbackFallback(t *testing.T) {
	// A CI runner has no tailnet, so QEMU bound the forward to loopback — which
	// is exactly where a probe should look.
	probeVM(t, "gate", `{"name":"gate","ssh_port":2242}`)
	endpoint, err := SSHEndpoint("gate")
	if err != nil {
		t.Fatalf("SSHEndpoint: %v", err)
	}
	if endpoint.Host != "127.0.0.1" {
		t.Errorf("host = %q, want loopback", endpoint.Host)
	}
}

func TestSSHEndpoint_Errors(t *testing.T) {
	probeVM(t, "gate", `{"name":"gate"}`)
	if _, err := SSHEndpoint("gate"); err == nil || !strings.Contains(err.Error(), "forwarded SSH port") {
		t.Errorf("a VM with no forwarded port should say so: %v", err)
	}
	if _, err := SSHEndpoint("missing"); err == nil {
		t.Error("an unknown VM should be an error")
	}
}

func TestProbeArgs(t *testing.T) {
	args := probeArgs("root", "/run/key", Endpoint{Host: "127.0.0.1", Port: 2242}, 7)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"BatchMode=yes",                // never block on a password prompt
		"StrictHostKeyChecking=no",     // a rebuilt VM reuses the port
		"UserKnownHostsFile=/dev/null", // …and must not poison a real one
		"ConnectTimeout=7",
		"-p 2242",
		"-i /run/key",
		"IdentitiesOnly=yes", // a crowded agent gets the connection closed
		"root@127.0.0.1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("probe args are missing %q: %s", want, joined)
		}
	}
	// The target must be last, so a command can be appended after it.
	if args[len(args)-1] != "root@127.0.0.1" {
		t.Errorf("the target should be the last argument: %v", args)
	}
}

func TestProbeArgs_NoIdentity(t *testing.T) {
	args := probeArgs("core", "", Endpoint{Host: "h", Port: 22}, 0)
	if strings.Contains(strings.Join(args, " "), "-i ") {
		t.Errorf("no identity file should mean no -i: %v", args)
	}
}

func TestExec(t *testing.T) {
	probeVM(t, "gate", `{"name":"gate","ssh_port":2242}`)
	var got []string
	SetSSHRunner(func(bin string, args ...string) ([]byte, error) {
		got = args
		return []byte("active\n"), nil
	})
	t.Cleanup(func() { SetSSHRunner(nil) })

	out, err := Exec("gate", "systemctl is-active sshd", ExecOpts{User: "core", IdentityFile: "/run/key"})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(out) != "active\n" {
		t.Errorf("output = %q", out)
	}
	if got[len(got)-1] != "systemctl is-active sshd" {
		t.Errorf("the command should be the last argument: %v", got)
	}
	if !strings.Contains(strings.Join(got, " "), "core@127.0.0.1") {
		t.Errorf("args = %v", got)
	}
}

func TestExec_ReportsTheCommandsFailure(t *testing.T) {
	probeVM(t, "gate", `{"name":"gate","ssh_port":2242}`)
	SetSSHRunner(func(string, ...string) ([]byte, error) {
		return []byte("Unit not found"), errors.New("exit status 3")
	})
	t.Cleanup(func() { SetSSHRunner(nil) })

	out, err := Exec("gate", "systemctl is-active nope", ExecOpts{})
	if err == nil {
		t.Fatal("a command that exits nonzero must be an error here — that is what a check needs")
	}
	if !strings.Contains(string(out), "Unit not found") {
		t.Errorf("the guest's own output should come back: %q", out)
	}
}

func TestWaitSSHKey_SucceedsOnceTheGuestAnswers(t *testing.T) {
	probeVM(t, "gate", `{"name":"gate","ssh_port":2242}`)
	attempts := 0
	SetSSHRunner(func(string, ...string) ([]byte, error) {
		attempts++
		if attempts < 2 {
			return []byte("Connection refused"), errors.New("exit status 255")
		}
		return nil, nil
	})
	t.Cleanup(func() { SetSSHRunner(nil) })

	if err := WaitSSHKey("gate", "root", "/run/key", 30*time.Second); err != nil {
		t.Fatalf("WaitSSHKey: %v", err)
	}
	if attempts < 2 {
		t.Errorf("expected a retry, got %d attempts", attempts)
	}
}

func TestWaitSSHKey_TimeoutKeepsTheLastError(t *testing.T) {
	probeVM(t, "gate", `{"name":"gate","ssh_port":2242}`)
	SetSSHRunner(func(string, ...string) ([]byte, error) {
		return []byte("Connection refused"), errors.New("exit status 255")
	})
	t.Cleanup(func() { SetSSHRunner(nil) })

	err := WaitSSHKey("gate", "root", "", time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("the timeout should carry the last error: %v", err)
	}
}

func TestSerialLog(t *testing.T) {
	dir := probeVM(t, "gate", "")
	if _, err := SerialLog("gate"); !errors.Is(err, ErrNoSerialLog) {
		t.Errorf("a VM with no console capture should report it: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "serial.log"), []byte("boot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := SerialLog("gate")
	if err != nil || string(data) != "boot\n" {
		t.Errorf("log = %q, %v", data, err)
	}
	if SerialLogPath("gate") != filepath.Join(dir, "serial.log") {
		t.Errorf("SerialLogPath = %q", SerialLogPath("gate"))
	}
}

func TestWaitSerial(t *testing.T) {
	dir := probeVM(t, "gate", "")
	if err := os.WriteFile(filepath.Join(dir, "serial.log"),
		[]byte("[ 1.0] starting\n[ 8.4] guest: CORRAL_VM_READY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	line, err := WaitSerial("gate", regexp.MustCompile(".*CORRAL_VM_READY.*"), time.Second)
	if err != nil {
		t.Fatalf("WaitSerial: %v", err)
	}
	if !strings.Contains(line, "8.4") {
		t.Errorf("the whole matched line should come back: %q", line)
	}
}

func TestWaitSerial_NoPattern(t *testing.T) {
	probeVM(t, "gate", "")
	if _, err := WaitSerial("gate", nil, time.Second); err == nil {
		t.Error("a nil pattern is a programming error, not a wait")
	}
}

func TestWaitSerial_MissingLogIsReportedAsSuch(t *testing.T) {
	probeVM(t, "gate", "")
	_, err := WaitSerial("gate", regexp.MustCompile("READY"), time.Millisecond)
	if !errors.Is(err, ErrNoSerialLog) {
		t.Errorf("a VM with no console capture should say so rather than time out silently: %v", err)
	}
}
