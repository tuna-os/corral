package qemu

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Probes a running local VM without a human in front of it: run one command
// over SSH, wait for a marker on the serial console, type at the console.
//
// These are what a test harness needs and an interactive session does not.
// SSH() opens a terminal and refuses a VM with no tailnet address; a CI runner
// has no tailnet, and nothing there can answer a prompt. So the functions here
// probe loopback, never allocate a TTY, and return output rather than printing
// it.

// sshRun executes the ssh client, and sshLookPath finds it. One seam so tests
// can drive the probe loops without a guest — and without an ssh binary, since
// a fake runner never execs one.
var (
	realSSHRun = func(bin string, args ...string) ([]byte, error) {
		return exec.Command(bin, args...).CombinedOutput()
	}
	sshRun       = realSSHRun
	sshLookPath  = exec.LookPath
	fakeSSHFound = func(string) (string, error) { return "ssh", nil }
)

// SetSSHRunner overrides the ssh client runner, and with it the lookup of the
// ssh binary. Passing nil restores the real one, so a test's cleanup is one
// line and cannot leave the package stubbed for the next test.
func SetSSHRunner(f func(bin string, args ...string) ([]byte, error)) {
	if f == nil {
		sshRun, sshLookPath = realSSHRun, exec.LookPath
		return
	}
	sshRun, sshLookPath = f, fakeSSHFound
}

// Endpoint is where a VM answers SSH from this host.
type Endpoint struct {
	Host string
	Port int
}

func (e Endpoint) String() string { return fmt.Sprintf("%s:%d", e.Host, e.Port) }

// SSHEndpoint returns the host and port a VM's forwarded sshd answers on.
//
// The host falls back to loopback rather than failing: QEMU binds its hostfwd
// to the Tailscale address when there is one and to 127.0.0.1 when there is
// not, and a CI runner is always the second case.
func SSHEndpoint(name string) (Endpoint, error) {
	meta, err := readMetadata(name)
	if err != nil {
		return Endpoint{}, fmt.Errorf("VM %q not found: %w", name, err)
	}
	if meta.SSHPort == 0 {
		return Endpoint{}, fmt.Errorf("VM %q has no forwarded SSH port — recreate it with this corral version", name)
	}
	host := meta.Tailscale
	if host == "" {
		host = "127.0.0.1"
	}
	return Endpoint{Host: host, Port: meta.SSHPort}, nil
}

// ExecOpts is one non-interactive command run in a guest.
type ExecOpts struct {
	User         string
	IdentityFile string
	// Timeout bounds the ssh client, so a hung guest fails the check instead
	// of the whole job.
	Timeout time.Duration
}

// Exec runs one command in the guest over the forwarded SSH port and returns
// its combined output. The error is the ssh client's, so a command that exits
// nonzero is an error here — which is what a check wants.
func Exec(name, command string, opts ExecOpts) ([]byte, error) {
	endpoint, err := SSHEndpoint(name)
	if err != nil {
		return nil, err
	}
	sshBin, err := sshLookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("the OpenSSH client is not installed, so %q cannot be reached", name)
	}
	user := opts.User
	if user == "" {
		user = "root"
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	args := probeArgs(user, opts.IdentityFile, endpoint, int(timeout.Seconds()))
	args = append(args, command)
	return sshRun(sshBin, args...)
}

// probeArgs is the argv every non-interactive probe shares. BatchMode keeps a
// key-less guest from blocking on a password prompt, and the throwaway
// known-hosts file keeps a rebuilt VM on a reused port from tripping the
// host-key check.
func probeArgs(user, identityFile string, endpoint Endpoint, connectTimeout int) []string {
	if connectTimeout <= 0 {
		connectTimeout = 5
	}
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", fmt.Sprintf("ConnectTimeout=%d", connectTimeout),
		"-p", fmt.Sprintf("%d", endpoint.Port),
	}
	if identityFile != "" {
		// IdentitiesOnly: a runner's agent may hold dozens of keys, and sshd
		// closes the connection before it reaches the one that works.
		args = append(args, "-i", identityFile, "-o", "IdentitiesOnly=yes")
	}
	return append(args, fmt.Sprintf("%s@%s", user, endpoint.Host))
}

// WaitSSHKey polls until `ssh -i key user@host true` succeeds, or timeout
// elapses. WaitSSH is the same loop without an identity file; both exist
// because a harness that generated its own keypair must offer it explicitly,
// and a key in the agent is not enough (see IdentitiesOnly above).
func WaitSSHKey(name, user, identityFile string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		out, err := Exec(name, "true", ExecOpts{User: user, IdentityFile: identityFile, Timeout: 10 * time.Second})
		if err == nil {
			return nil
		}
		lastErr = fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
		if time.Now().After(deadline) {
			return fmt.Errorf("VM %q not SSH-reachable within %s (last error: %v)", name, timeout, lastErr)
		}
		time.Sleep(3 * time.Second)
	}
}

// ── serial console ────────────────────────────────────────────────

// SerialLogPath is where a VM's guest console is written. Created by the
// systemd unit, so it exists from the first start of any VM made by a corral
// new enough to add the chardev.
func SerialLogPath(name string) string {
	return filepath.Join(VMHome(), name, "serial.log")
}

// ErrNoSerialLog reports a VM whose unit predates serial capture.
var ErrNoSerialLog = errors.New("no serial log — recreate the VM (corral create --force ...) to pick up console capture")

// SerialLog returns the console output captured so far.
func SerialLog(name string) ([]byte, error) {
	data, err := os.ReadFile(SerialLogPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoSerialLog
	}
	return data, err
}

// WaitSerial blocks until pattern matches the guest's console output, and
// returns the matching line.
//
// This is the readiness gate for a guest that SSH cannot answer for: a desktop
// image with sshd off, a boot that stops in the initramfs, an image whose
// whole test is a message it prints. TunaOS's iso-e2e.sh gates on a serial
// marker for exactly that reason, and treats pixels as the fallback rather
// than the proof.
func WaitSerial(name string, pattern *regexp.Regexp, timeout time.Duration) (string, error) {
	if pattern == nil {
		return "", fmt.Errorf("no pattern given")
	}
	deadline := time.Now().Add(timeout)
	for {
		data, err := SerialLog(name)
		if err != nil && !errors.Is(err, ErrNoSerialLog) {
			return "", err
		}
		if line := pattern.FindString(string(data)); line != "" {
			return line, nil
		}
		if time.Now().After(deadline) {
			if errors.Is(err, ErrNoSerialLog) {
				return "", ErrNoSerialLog
			}
			return "", fmt.Errorf("%q did not appear on %s's console within %s", pattern, name, timeout)
		}
		time.Sleep(2 * time.Second)
	}
}
