package kubevirt

// Tests for the kubevirt cells the parity matrix ships without a covering
// test: SSH (virtctl ssh), VNC/RDP (RealConsoleDialer through virtctl), and
// export (virtctl vmexport).
//
// SSH and export end in real interactive subprocesses, so the doubles are
// stub kubectl/virtctl binaries on PATH: the tests assert the argv Corral
// builds and the contract around it (ephemeral VMs have nothing to back up),
// not the session or download itself. The console dialer goes through an
// injected command constructor instead: Dial kills the proxy on timeout, so
// a stub binary on PATH would race the kill and flake.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/shell"
)

// stubTools puts recording kubectl/virtctl stubs on PATH and returns the file
// their argv lands in. The virtctl stub additionally materialises any
// --output= file it is asked for, so vmexport download produces an artifact
// finish() can stat.
func stubTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$ARGV_LOG\"\n" +
		"for a in \"$@\"; do case \"$a\" in --output=*) printf 'fake-disk-bytes' > \"${a#--output=}\";; esac; done\n" +
		"exit 0\n"
	for _, bin := range []string{"kubectl", "virtctl"} {
		if err := os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ARGV_LOG", log)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func recordedArgv(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(os.Getenv("ARGV_LOG"))
	if err != nil {
		t.Fatalf("reading argv log: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

// lookPathRunner answers LookPath from a fixed binary while delegating Run,
// so ensureVirtctl resolves the stub without the package fake's /fake/bin
// path (which no real exec could run).
type lookPathRunner struct {
	shell.Runner
	bin string
}

func (r lookPathRunner) LookPath(name string) (string, error) {
	if name == "virtctl" {
		return r.bin, nil
	}
	return r.Runner.LookPath(name)
}

func TestSSHInvokesVirtctlSSH(t *testing.T) {
	stubTools(t)
	virtctl := filepath.Join(filepath.Dir(os.Getenv("ARGV_LOG")), "virtctl")
	c := NewClientForContext("default", "")
	c.Runner = lookPathRunner{Runner: shell.NewFake(), bin: virtctl}

	if err := c.SSH("web-1", "debian", "", "uptime", 0, "", nil); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	argv := recordedArgv(t)
	for _, want := range []string{
		"ssh", "--namespace=default", "--username=debian",
		"--command=uptime", "vm/web-1",
		"--local-ssh-opts=-o StrictHostKeyChecking=no",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}
}

// stubDialler records the proxy argv Dial builds and returns a process that
// exits at once without listening, so Dial takes its timeout path. A stub
// binary on PATH cannot do this job: Dial kills the proxy on timeout, which
// races the stub's own startup and flakes.
func stubDialler(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := consoleCommand
	consoleCommand = func(_ string, name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		return exec.Command("true")
	}
	t.Cleanup(func() { consoleCommand = old })
	oldTries, oldInterval := consoleDialTries, consoleDialInterval
	consoleDialTries, consoleDialInterval = 1, time.Millisecond
	t.Cleanup(func() { consoleDialTries, consoleDialInterval = oldTries, oldInterval })
	return &got
}

// dialPort extracts the --port value so the test can pin the free port Dial
// picked into the timeout error: the error must name the port the proxy was
// told to open, not some other number.
func dialPort(t *testing.T, argv []string) string {
	t.Helper()
	for i, a := range argv {
		if a == "--port" && i+1 < len(argv) {
			return argv[i+1]
		}
		if strings.HasPrefix(a, "--port=") {
			return strings.TrimPrefix(a, "--port=")
		}
	}
	t.Fatalf("argv %q has no --port", argv)
	return ""
}

func TestDialVNCUsesProxyOnly(t *testing.T) {
	got := stubDialler(t)

	_, err := RealConsoleDialer{}.Dial("default", "web-1", VNC)
	if err == nil {
		t.Fatal("expected a timeout: the stub never listens")
	}
	argv := strings.Join(*got, " ")
	// virtctl vnc <name> -n <ns> --proxy-only --port <free port>: the proxy
	// binds locally and the browser reaches it over the websocket bridge —
	// never a direct VNC dial to the cluster.
	for _, want := range []string{"vnc web-1", "-n default", "--proxy-only", "--port "} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}
	if port := dialPort(t, *got); !strings.Contains(err.Error(), "default/web-1") ||
		!strings.Contains(err.Error(), port) {
		t.Errorf("error %q should name the VM and the port %s", err, port)
	}
}

func TestDialRDPUsesPortForward(t *testing.T) {
	got := stubDialler(t)

	_, err := RealConsoleDialer{}.Dial("default", "web-1", RDP)
	if err == nil {
		t.Fatal("expected a timeout: the stub never listens")
	}
	if !strings.Contains(err.Error(), "did not open port") {
		t.Fatalf("Dial: %v — want the proxy timeout, not a spawn failure", err)
	}
	argv := strings.Join(*got, " ")
	// virtctl port-forward vm/<name> <port>:3389: RDP is the guest's 3389
	// carried over the same proxy mechanism as VNC.
	for _, want := range []string{"port-forward vm/web-1", ":3389", "-n default"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}
}

func TestDialUnknownConsoleIsAnError(t *testing.T) {
	d := RealConsoleDialer{}
	if _, err := d.Dial("default", "web-1", Console(99)); err == nil {
		t.Fatal("expected an error for an unknown console kind")
	}
}

func TestExportRawDownloadsViaVmexport(t *testing.T) {
	stubTools(t)
	c := NewClientForContext("testns", "")
	c.Runner = shell.Real{}
	out := filepath.Join(t.TempDir(), "web-1.img.gz")

	// An explicit volume skips the primary-PVC lookup, so no kubectl JSON is
	// needed: the test pins the download contract, not inventory.
	got, err := c.ExportRaw("web-1", "web-1-root", out)
	if err != nil {
		t.Fatalf("ExportRaw: %v", err)
	}
	if got != out {
		t.Errorf("path = %q, want %q", got, out)
	}
	if data, err := os.ReadFile(out); err != nil || string(data) != "fake-disk-bytes" {
		t.Errorf("artifact = %q, %v — want the stub's download bytes", data, err)
	}
	argv := recordedArgv(t)
	for _, want := range []string{
		"vmexport download web-1-export", "--vm=web-1", "--volume=web-1-root",
		"--format=raw", "--port-forward",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q should contain %q", argv, want)
		}
	}
	// An interrupted run must not poison the next one: the stale export is
	// cleared before the download starts.
	if !strings.Contains(argv, "delete vmexport web-1-export") {
		t.Errorf("argv %q should clear a stale vmexport first", argv)
	}
}

func TestExportRefusesAnEphemeralVM(t *testing.T) {
	f := shell.NewFake()
	f.AddPrefixResponse("kubectl",
		`{"spec":{"template":{"spec":{"volumes":[{"name":"root","containerDisk":{"image":"img"}}]}}}}`, nil)
	SetDefaultRunner(f)
	t.Cleanup(func() { SetDefaultRunner(nil) })

	_, err := NewClientForContext("testns", "").Export("web-1", "", "")
	if err == nil {
		t.Fatal("expected a refusal: a container-disk VM has no PVC to export")
	}
	if !strings.Contains(err.Error(), "no persistent disk") {
		t.Errorf("refusal %q should say there is no persistent disk", err)
	}
}
