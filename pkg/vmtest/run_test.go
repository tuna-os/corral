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

// The marker is matched against the whole console, not line by line, so one
// pattern can require two markers that a guest prints on separate lines and
// in either order. A gate that needs both a base and a desktop contract to
// pass relies on that.
func TestWaitReady_MarkerSpansTheWholeConsole(t *testing.T) {
	const both = `(?s)BASE_OK.*DESKTOP_OK|DESKTOP_OK.*BASE_OK`
	for _, tc := range []struct {
		name    string
		console string
		ready   bool
	}{
		{"base first", "BASE_OK\nnoise\nDESKTOP_OK\n", true},
		{"desktop first", "DESKTOP_OK\nnoise\nBASE_OK\n", true},
		{"only one", "DESKTOP_OK\nnoise\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := vmState(t, "gate")
			if err := os.WriteFile(filepath.Join(dir, "serial.log"), []byte(tc.console), 0o644); err != nil {
				t.Fatal(err)
			}
			spec := &Spec{Name: "gate", ReadyMarker: both}
			spec.WithDefaults()
			spec.Timeout = Duration(10 * time.Millisecond)
			err := waitReady(spec, Keypair{}, &Result{}, func(string) {})
			if tc.ready && err != nil {
				t.Errorf("waitReady: %v", err)
			}
			if !tc.ready && err == nil {
				t.Error("one marker of two should not count as ready")
			}
		})
	}
}

// A failure line on the console does not end the wait: the marker gate only
// knows what ready looks like, so a guest that reports its own failure is
// waited on until the timeout.
func TestWaitReady_MarkerGateWaitsOutAReportedFailure(t *testing.T) {
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "serial.log"),
		[]byte("Entering emergency mode\nCONTRACT_FAIL dbus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := &Spec{Name: "gate", ReadyMarker: "CONTRACT_OK"}
	spec.WithDefaults()
	spec.Timeout = Duration(10 * time.Millisecond)
	err := waitReady(spec, Keypair{}, &Result{}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "did not appear") {
		t.Errorf("want the marker timeout, got %v", err)
	}
}

// The SSH gate is the default. When it times out, the error carries the tail
// of the console, because the console is the only witness to why.
func TestWaitReady_SSHTimeoutCarriesTheConsoleTail(t *testing.T) {
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"name":"gate","ssh_port":2242}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "serial.log"), []byte("Kernel panic - not syncing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	qemu.SetSSHRunner(func(string, ...string) ([]byte, error) {
		return []byte("Connection refused"), errors.New("exit status 255")
	})
	t.Cleanup(func() { qemu.SetSSHRunner(nil) })

	spec := &Spec{Name: "gate"}
	spec.WithDefaults()
	spec.Timeout = Duration(time.Millisecond)
	result := &Result{}
	err := waitReady(spec, Keypair{PrivatePath: "/run/key"}, result, func(string) {})
	if err == nil {
		t.Fatal("expected an SSH timeout")
	}
	if !strings.Contains(err.Error(), "Kernel panic") {
		t.Errorf("the error should carry the console tail: %v", err)
	}
	if result.ReadyBy != "" {
		t.Errorf("readyBy = %q for a guest that never answered", result.ReadyBy)
	}
}

// A failure marker ends the wait the moment the console shows it, and is
// checked before the ready marker: a guest that printed both has failed.
func TestWaitReady_FailMarkerEndsTheWait(t *testing.T) {
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "serial.log"),
		[]byte("DESKTOP_OK\nBASE_FAIL no system bus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := &Spec{Name: "gate", ReadyMarker: "DESKTOP_OK", FailMarker: "BASE_FAIL|emergency mode"}
	spec.WithDefaults() // fifteen minutes: the test passes only if the wait ends early
	result := &Result{}
	err := waitReady(spec, Keypair{}, result, func(string) {})
	if err == nil {
		t.Fatal("a reported failure should fail the wait")
	}
	if !strings.Contains(err.Error(), "BASE_FAIL no system bus") {
		t.Errorf("the error should quote the failure line: %v", err)
	}
	if result.ReadyBy != "" {
		t.Errorf("readyBy = %q for a guest that reported a failure", result.ReadyBy)
	}
}

// With a failure marker that never appears, the ready marker decides exactly
// as it does on its own.
func TestWaitReady_FailMarkerAbsentLeavesTheReadyGate(t *testing.T) {
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = 2 * time.Second })
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "serial.log"), []byte("boot\nDESKTOP_OK now\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := &Spec{Name: "gate", ReadyMarker: "DESKTOP_OK", FailMarker: "BASE_FAIL"}
	spec.WithDefaults()
	result := &Result{}
	if err := waitReady(spec, Keypair{}, result, func(string) {}); err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if result.ReadyBy != readyByMarker || !strings.Contains(result.ReadyEvidence, "DESKTOP_OK now") {
		t.Errorf("readyBy/evidence = %q/%q", result.ReadyBy, result.ReadyEvidence)
	}

	if err := os.WriteFile(filepath.Join(dir, "serial.log"), []byte("boot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec.Timeout = Duration(10 * time.Millisecond)
	err := waitReady(spec, Keypair{}, &Result{}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "did not appear") {
		t.Errorf("want the marker timeout, got %v", err)
	}
}

// Without a ready marker, a failure marker still watches the console while
// the SSH probe decides readiness.
func TestWaitReady_FailMarkerWatchesAnSSHGate(t *testing.T) {
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = 2 * time.Second })
	dir := vmState(t, "gate")
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"name":"gate","ssh_port":2242}`), 0o644); err != nil {
		t.Fatal(err)
	}
	serial := filepath.Join(dir, "serial.log")
	if err := os.WriteFile(serial, []byte("booting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	qemu.SetSSHRunner(func(string, ...string) ([]byte, error) {
		attempts++
		if attempts == 2 {
			if err := os.WriteFile(serial, []byte("booting\nYou are in emergency mode\n"), 0o644); err != nil {
				t.Error(err)
			}
		}
		return []byte("Connection refused"), errors.New("exit status 255")
	})
	t.Cleanup(func() { qemu.SetSSHRunner(nil) })

	spec := &Spec{Name: "gate", FailMarker: "emergency mode"}
	spec.WithDefaults()
	err := waitReady(spec, Keypair{PrivatePath: "/run/key"}, &Result{}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "You are in emergency mode") {
		t.Errorf("want the failure marker, got %v", err)
	}

	// And a guest that answers is ready by SSH.
	if err := os.WriteFile(serial, []byte("booting\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	qemu.SetSSHRunner(func(string, ...string) ([]byte, error) { return nil, nil })
	result := &Result{}
	if err := waitReady(spec, Keypair{PrivatePath: "/run/key"}, result, func(string) {}); err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if result.ReadyBy != readyBySSH {
		t.Errorf("readyBy = %q, want %q", result.ReadyBy, readyBySSH)
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

// TestVerdict covers the precedence order verdict decides Run's outcome by,
// entirely off already-collected Result state — no VM, no SSH, no host.
func TestVerdict(t *testing.T) {
	blankFrame := &qemu.Frame{StdDev: 0.0}
	paintedFrame := &qemu.Frame{StdDev: 1.0}

	for _, tc := range []struct {
		name          string
		spec          *Spec
		result        *Result
		sshReachable  bool
		wantExitCode  int
		wantStatus    string
		wantErrSubstr string
	}{
		{
			name:         "passes with nothing to report",
			spec:         &Spec{},
			result:       &Result{},
			sshReachable: true,
			wantExitCode: ExitOK,
			wantStatus:   "passed",
		},
		{
			name:          "a hook that never reported outranks a passing check",
			spec:          &Spec{Checks: []string{"true"}},
			result:        &Result{Hook: &HookResult{Ran: false}, Checks: []CheckResult{{Passed: true}}},
			sshReachable:  true,
			wantExitCode:  ExitHook,
			wantStatus:    "failed",
			wantErrSubstr: "never reported",
		},
		{
			name:          "a hook that ran and failed outranks a passing check",
			spec:          &Spec{Checks: []string{"true"}},
			result:        &Result{Hook: &HookResult{Ran: true, ExitCode: 3}, Checks: []CheckResult{{Passed: true}}},
			sshReachable:  true,
			wantExitCode:  ExitHook,
			wantStatus:    "failed",
			wantErrSubstr: "exit 3",
		},
		{
			name:          "a hook that ran and passed does not mask unreachable-SSH checks",
			spec:          &Spec{Checks: []string{"true"}},
			result:        &Result{Hook: &HookResult{Ran: true, ExitCode: 0}},
			sshReachable:  false,
			wantExitCode:  ExitCheck,
			wantStatus:    "failed",
			wantErrSubstr: "SSH never answered",
		},
		{
			name:         "unreachable SSH is not a failure when there are no checks to run",
			spec:         &Spec{},
			result:       &Result{},
			sshReachable: false,
			wantExitCode: ExitOK,
			wantStatus:   "passed",
		},
		{
			name:          "the first failing check wins, later checks notwithstanding",
			spec:          &Spec{Checks: []string{"a", "b", "c"}},
			result:        &Result{Checks: []CheckResult{{Command: "a", Passed: true}, {Command: "b", Passed: false}, {Command: "c", Passed: true}}},
			sshReachable:  true,
			wantExitCode:  ExitCheck,
			wantStatus:    "failed",
			wantErrSubstr: "check failed: b",
		},
		{
			name:         "a blank final frame fails only when RequirePaint asked for it",
			spec:         &Spec{Screenshots: Screenshots{RequirePaint: false}},
			result:       &Result{FinalFrame: blankFrame},
			sshReachable: true,
			wantExitCode: ExitOK,
			wantStatus:   "passed",
		},
		{
			name:          "a blank final frame fails when RequirePaint is set",
			spec:          &Spec{Screenshots: Screenshots{RequirePaint: true}},
			result:        &Result{FinalFrame: blankFrame},
			sshReachable:  true,
			wantExitCode:  ExitBlank,
			wantStatus:    "failed",
			wantErrSubstr: "never painted anything",
		},
		{
			name:         "a painted final frame passes even with RequirePaint set",
			spec:         &Spec{Screenshots: Screenshots{RequirePaint: true}},
			result:       &Result{FinalFrame: paintedFrame},
			sshReachable: true,
			wantExitCode: ExitOK,
			wantStatus:   "passed",
		},
		{
			name:         "a passing check and a painted frame both clear",
			spec:         &Spec{Checks: []string{"a"}, Screenshots: Screenshots{RequirePaint: true}},
			result:       &Result{Checks: []CheckResult{{Command: "a", Passed: true}}, FinalFrame: paintedFrame},
			sshReachable: true,
			wantExitCode: ExitOK,
			wantStatus:   "passed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verdict(tc.spec, tc.result, tc.sshReachable)
			if tc.wantExitCode == ExitOK {
				if err != nil {
					t.Fatalf("verdict() = %v, want nil", err)
				}
			} else {
				if err == nil {
					t.Fatal("verdict() = nil, want an error")
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("verdict() error = %q, want it to contain %q", err.Error(), tc.wantErrSubstr)
				}
			}
			if tc.result.ExitCode != tc.wantExitCode {
				t.Errorf("result.ExitCode = %d, want %d", tc.result.ExitCode, tc.wantExitCode)
			}
			if tc.result.Status != tc.wantStatus {
				t.Errorf("result.Status = %q, want %q", tc.result.Status, tc.wantStatus)
			}
		})
	}
}
