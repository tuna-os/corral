package vmtest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
)

// vmState points pkg/qemu at a throwaway VM home and creates one VM's
// directory, so the console and metadata readers have something to read.
func vmState(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	qemu.SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { qemu.SetStateDirs("", "") })
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating the VM directory: %v", err)
	}
	return dir
}

func TestConsoleTail(t *testing.T) {
	dir := vmState(t, "gate")
	lines := []string{"one", "two", "three", "four", "five"}
	if err := os.WriteFile(filepath.Join(dir, "serial.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail := consoleTail("gate", 2)
	if !strings.Contains(tail, "four") || !strings.Contains(tail, "five") {
		t.Errorf("the tail should be the last lines: %q", tail)
	}
	if strings.Contains(tail, "one") {
		t.Errorf("the tail should not include earlier lines: %q", tail)
	}
}

func TestConsoleTail_NoLog(t *testing.T) {
	vmState(t, "gate")
	// A VM whose unit predates console capture, or one that never started.
	if tail := consoleTail("gate", 5); !strings.Contains(tail, "no console log") {
		t.Errorf("tail = %q", tail)
	}
}

func TestConsoleTail_EmptyLog(t *testing.T) {
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "serial.log"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Silence is itself a diagnosis: the guest never reached the bootloader.
	if tail := consoleTail("gate", 5); !strings.Contains(tail, "said nothing") {
		t.Errorf("tail = %q", tail)
	}
}

func TestCopySerialLog(t *testing.T) {
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "serial.log"), []byte("boot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifacts := t.TempDir()
	path, err := copySerialLog("gate", artifacts)
	if err != nil {
		t.Fatalf("copySerialLog: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "boot\n" {
		t.Errorf("copied log = %q, %v", data, err)
	}
}

func TestHookOutcome_FromConsole(t *testing.T) {
	dir := vmState(t, "gate")
	spec := &Spec{Name: "gate"}
	spec.WithDefaults()

	for _, tc := range []struct {
		name     string
		log      string
		wantRan  bool
		wantCode int
	}{
		{name: "passed", log: "boot\n" + HookOKMarker + "\n" + ReadyMarker + "\n", wantRan: true},
		{name: "failed", log: HookFailMarker + " rc=7\n" + ReadyMarker + "\n", wantRan: true, wantCode: 7},
		{name: "failed without a code", log: HookFailMarker + "\n", wantRan: true, wantCode: 1},
		{name: "never reported", log: "just a boot\n"},
	} {
		if err := os.WriteFile(filepath.Join(dir, "serial.log"), []byte(tc.log), 0o644); err != nil {
			t.Fatal(err)
		}
		// sshReachable false, so the console is the only source.
		hook := hookOutcome(spec, Keypair{}, false, nil)
		if hook.Ran != tc.wantRan {
			t.Errorf("%s: ran = %v, want %v", tc.name, hook.Ran, tc.wantRan)
		}
		if hook.ExitCode != tc.wantCode {
			t.Errorf("%s: exit = %d, want %d", tc.name, hook.ExitCode, tc.wantCode)
		}
		if tc.wantRan && hook.Source != "console" {
			t.Errorf("%s: source = %q, want console", tc.name, hook.Source)
		}
	}
}

func TestRunChecks(t *testing.T) {
	dir := vmState(t, "gate")
	// Exec needs a forwarded port, which comes from the VM's metadata.
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"),
		[]byte(`{"name":"gate","ssh_port":2242,"tailscale_ip":"127.0.0.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	qemu.SetSSHRunner(func(bin string, args ...string) ([]byte, error) {
		command := args[len(args)-1]
		if strings.Contains(command, "fails") {
			return []byte("Unit not found"), errors.New("exit status 3")
		}
		return []byte("active"), nil
	})
	t.Cleanup(func() { qemu.SetSSHRunner(nil) })

	spec := &Spec{Name: "gate", Checks: []string{"systemctl is-active sshd", "systemctl is-active fails"}}
	spec.WithDefaults()
	results := runChecks(spec, Keypair{PrivatePath: "/tmp/key"}, nil)

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 — a failing check must not stop the rest", len(results))
	}
	if !results[0].Passed || results[0].Output != "active" {
		t.Errorf("first check = %+v", results[0])
	}
	if results[1].Passed {
		t.Error("the second check should have failed")
	}
	if !strings.Contains(results[1].Output, "Unit not found") {
		t.Errorf("a failing check should keep the guest's own output: %+v", results[1])
	}
}

func TestCollectDiagnostics(t *testing.T) {
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"),
		[]byte(`{"name":"gate","ssh_port":2242,"tailscale_ip":"127.0.0.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	qemu.SetSSHRunner(func(bin string, args ...string) ([]byte, error) {
		return []byte("collected: " + args[len(args)-1]), nil
	})
	t.Cleanup(func() { qemu.SetSSHRunner(nil) })

	artifacts := t.TempDir()
	spec := &Spec{Name: "gate"}
	spec.WithDefaults()
	collectDiagnostics(spec, Keypair{}, artifacts)

	for _, d := range diagnostics {
		path := filepath.Join(artifacts, "diagnostics", d.name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s was not collected: %v", d.name, err)
			continue
		}
		if !strings.Contains(string(data), "collected:") {
			t.Errorf("%s = %q", d.name, data)
		}
	}
}

func TestFrameRecorder_CaptureOff(t *testing.T) {
	vmState(t, "gate")
	spec := &Spec{Name: "gate", Screenshots: Screenshots{Interval: Duration(-1)}}
	recorder := newFrameRecorder(spec, t.TempDir())
	recorder.start()
	recorder.stop() // must not block when capture never started
	if frames := recorder.frames(); len(frames) != 0 {
		t.Errorf("expected no frames, got %d", len(frames))
	}
}

func TestFrameRecorder_SurvivesAMissingVM(t *testing.T) {
	// Before the VM starts there is no monitor socket. Capture has to keep
	// trying rather than give up or crash.
	vmState(t, "gate")
	spec := &Spec{Name: "gate", Screenshots: Screenshots{Interval: Duration(10 * time.Millisecond)}}
	recorder := newFrameRecorder(spec, t.TempDir())
	recorder.start()
	time.Sleep(50 * time.Millisecond)
	recorder.stop()
	if frames := recorder.frames(); len(frames) != 0 {
		t.Errorf("a VM with no monitor socket cannot produce frames, got %d", len(frames))
	}
	// Stopping twice is what the deferred stop in Run does.
	recorder.stop()
}

func TestAssembleVideo_Skipped(t *testing.T) {
	spec := &Spec{}
	if path, err := assembleVideo(spec, t.TempDir()); err != nil || path != "" {
		t.Errorf("no video requested should be a no-op: %q, %v", path, err)
	}

	fakeLookPath(t, nil)
	spec.Screenshots.Video = true
	if _, err := assembleVideo(spec, t.TempDir()); err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Errorf("a missing ffmpeg should say so: %v", err)
	}
}

func TestAssembleVideo_NoFrames(t *testing.T) {
	fakeLookPath(t, map[string]bool{"ffmpeg": true})
	spec := &Spec{Screenshots: Screenshots{Video: true}}
	if _, err := assembleVideo(spec, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no frames") {
		t.Errorf("expected a no-frames error, got %v", err)
	}
}

func TestRun_RejectsABadSpecWithoutTouchingTheHost(t *testing.T) {
	fakeRunner(t)
	spec := &Spec{} // no image
	result, err := Run(spec, nil)
	if err == nil {
		t.Fatal("expected a spec error")
	}
	if ExitCodeOf(result) != ExitSpec {
		t.Errorf("exit code = %d, want %d", ExitCodeOf(result), ExitSpec)
	}
}

func TestWaitReady_MarkerGate(t *testing.T) {
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "serial.log"),
		[]byte("[   3.2] systemd: hello\n[   9.9] guest: ALL_GOOD now\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := &Spec{Name: "gate", ReadyMarker: "ALL_GOOD"}
	spec.WithDefaults()
	result := &Result{}
	if err := waitReady(spec, Keypair{}, result, func(string) {}); err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if result.ReadyBy != readyByMarker {
		t.Errorf("readyBy = %q, want %q", result.ReadyBy, readyByMarker)
	}
	// The whole line is kept, so the result shows what the guest said.
	if !strings.Contains(result.ReadyEvidence, "ALL_GOOD now") {
		t.Errorf("evidence = %q", result.ReadyEvidence)
	}
}

func TestWaitReady_MarkerTimeout(t *testing.T) {
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "serial.log"), []byte("nothing useful\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := &Spec{Name: "gate", ReadyMarker: "NEVER", Timeout: Duration(10 * time.Millisecond)}
	spec.WithDefaults()
	spec.Timeout = Duration(10 * time.Millisecond)
	err := waitReady(spec, Keypair{}, &Result{}, func(string) {})
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !strings.Contains(err.Error(), "NEVER") {
		t.Errorf("the error should name what it waited for: %v", err)
	}
}

func TestOperatorKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := operatorKey(); got != "" {
		t.Errorf("a host with no key should give no key, got %q", got)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), []byte("ssh-ed25519 AAAAoperator me@host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := operatorKey(); got != "ssh-ed25519 AAAAoperator me@host" {
		t.Errorf("operatorKey = %q", got)
	}
}
