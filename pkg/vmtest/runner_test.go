package vmtest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
)

// fakeHost implements every Runner collaborator and records the calls that
// matter for sequencing and cleanup, so Runner.Run's invariants are tested
// with no VM, no bootc and no registry.
type fakeHost struct {
	calls []string

	preflightErr error
	keyErr       error
	layerErr     error
	derived      bool
	importErr    error
	startErr     error
	readyErr     error
	readyBy      string
	sshErr       error
	deleteErr    error
	hook         *HookResult
	checks       []CheckResult
	finalFrame   *qemu.Frame

	framesStopped int
	now           time.Time
}

func (f *fakeHost) call(name string) { f.calls = append(f.calls, name) }

func (f *fakeHost) Preflight(*Spec) ([]string, error) {
	f.call("preflight")
	return []string{"a note"}, f.preflightErr
}
func (f *fakeHost) Keypair(string) (Keypair, error) {
	f.call("keypair")
	return Keypair{PrivatePath: "/keys/id", PublicKey: "ssh-ed25519 AAAA"}, f.keyErr
}
func (f *fakeHost) Layer(spec *Spec, _, _ string, _ func(string)) (Layered, error) {
	f.call("layer")
	return Layered{Image: spec.Bootc, Base: spec.Bootc, Derived: f.derived}, f.layerErr
}
func (f *fakeHost) BuildAndImport(*Spec, Layered, Keypair, func(string)) error {
	f.call("import")
	return f.importErr
}
func (f *fakeHost) Start(string) error { f.call("start"); return f.startErr }
func (f *fakeHost) WaitReady(_ *Spec, _ Keypair, result *Result, _ func(string)) error {
	f.call("wait-ready")
	f.now = f.now.Add(42 * time.Second)
	if f.readyErr == nil {
		result.ReadyBy = f.readyBy
	}
	return f.readyErr
}
func (f *fakeHost) SSHEndpoint(string) (qemu.Endpoint, error) {
	return qemu.Endpoint{Host: "127.0.0.1", Port: 2222}, nil
}
func (f *fakeHost) WaitSSH(string, string, string, time.Duration) error {
	f.call("wait-ssh")
	return f.sshErr
}
func (f *fakeHost) Delete(string) error { f.call("delete"); return f.deleteErr }
func (f *fakeHost) Hook(*Spec, Keypair, bool, func(string)) *HookResult {
	f.call("hook")
	return f.hook
}
func (f *fakeHost) Checks(*Spec, Keypair, func(string)) []CheckResult {
	f.call("checks")
	return f.checks
}
func (f *fakeHost) Diagnostics(*Spec, Keypair, string) { f.call("diagnostics") }
func (f *fakeHost) StartFrames(*Spec, string) FrameSource {
	f.call("frames")
	return &fakeFrames{host: f}
}
func (f *fakeHost) CopySerialLog(string, string) (string, error) { return "", errors.New("no log") }
func (f *fakeHost) CaptureFinal(_ *Spec, _, label string, _ *Result) *qemu.Frame {
	f.call("capture-" + label)
	return f.finalFrame
}
func (f *fakeHost) AssembleVideo(*Spec, string) (string, error) { return "", nil }
func (f *fakeHost) Record(*Spec, io.Writer)                     { f.call("record") }
func (f *fakeHost) Now() time.Time                              { return f.now }

type fakeFrames struct {
	host    *fakeHost
	stopped bool
}

func (s *fakeFrames) stop() {
	if !s.stopped {
		s.stopped = true
		s.host.framesStopped++
	}
}
func (s *fakeFrames) frames() []qemu.Frame { return nil }

func (f *fakeHost) runner() *Runner {
	return &Runner{Image: f, VM: f, Probes: f, Evidence: f, Registry: f, Clock: f}
}

func TestRunner_Run(t *testing.T) {
	painted := &qemu.Frame{StdDev: 1}
	for _, tc := range []struct {
		name    string
		host    fakeHost
		keep    bool
		checks  []string
		want    int
		errText string
		// called and notCalled are call names that must, or must not, appear.
		called    []string
		notCalled []string
		// framesStopped is how often the frame capture must have been stopped
		// (0 when it never started).
		framesStopped int
	}{
		{
			name:      "host preflight failure touches nothing else",
			host:      fakeHost{preflightErr: errors.New("no qemu")},
			want:      ExitHost,
			errText:   "no qemu",
			notCalled: []string{"keypair", "layer", "import", "start", "delete"},
		},
		{
			name:      "key failure is a host failure",
			host:      fakeHost{keyErr: errors.New("no ssh-keygen")},
			want:      ExitHost,
			notCalled: []string{"layer", "import", "start"},
		},
		{
			name:      "layer failure stops before a disk is built",
			host:      fakeHost{layerErr: errors.New("podman build")},
			want:      ExitLayer,
			notCalled: []string{"import", "record", "start", "delete"},
		},
		{
			name:      "build failure is ExitBuild and records nothing",
			host:      fakeHost{importErr: errors.New("bootc install failed")},
			want:      ExitBuild,
			notCalled: []string{"record", "frames", "start"},
		},
		{
			name:      "an import that cannot create the VM is ExitStart",
			host:      fakeHost{importErr: fmt.Errorf("creating the VM: %w", errors.New("disk full"))},
			want:      ExitStart,
			notCalled: []string{"record", "start"},
		},
		{
			name:          "start failure keeps the VM for inspection and stops capture",
			host:          fakeHost{startErr: errors.New("qemu died")},
			want:          ExitStart,
			errText:       "starting the VM",
			called:        []string{"record", "frames", "start"},
			notCalled:     []string{"wait-ready", "delete"},
			framesStopped: 1,
		},
		{
			name:          "not ready captures a failure frame and keeps the VM",
			host:          fakeHost{readyErr: errors.New("timed out")},
			want:          ExitNotReady,
			called:        []string{"capture-failure"},
			notCalled:     []string{"capture-ready", "checks", "delete"},
			framesStopped: 1,
		},
		{
			name:          "a passing run deletes the VM",
			host:          fakeHost{readyBy: readyBySSH, checks: []CheckResult{{Command: "true", Passed: true}}, finalFrame: painted},
			checks:        []string{"true"},
			want:          ExitOK,
			called:        []string{"capture-ready", "diagnostics", "checks", "delete"},
			notCalled:     []string{"wait-ssh", "hook"},
			framesStopped: 1,
		},
		{
			name:          "keep leaves a passing VM behind",
			host:          fakeHost{readyBy: readyBySSH},
			keep:          true,
			want:          ExitOK,
			notCalled:     []string{"delete"},
			framesStopped: 1,
		},
		{
			name:          "a failing check still deletes the VM",
			host:          fakeHost{readyBy: readyBySSH, checks: []CheckResult{{Command: "false"}}},
			checks:        []string{"false"},
			want:          ExitCheck,
			errText:       "check failed: false",
			called:        []string{"delete"},
			framesStopped: 1,
		},
		{
			name:          "a delete failure does not change a passing verdict",
			host:          fakeHost{readyBy: readyBySSH, deleteErr: errors.New("busy")},
			want:          ExitOK,
			called:        []string{"delete"},
			framesStopped: 1,
		},
		{
			name:          "marker-ready guest with no SSH skips probes and fails the checks",
			host:          fakeHost{readyBy: readyByMarker, sshErr: errors.New("refused")},
			checks:        []string{"true"},
			want:          ExitCheck,
			errText:       "SSH never answered",
			called:        []string{"wait-ssh", "delete"},
			notCalled:     []string{"diagnostics", "checks"},
			framesStopped: 1,
		},
		{
			name:          "a derived image's failing hook is ExitHook",
			host:          fakeHost{readyBy: readyBySSH, derived: true, hook: &HookResult{Ran: true, ExitCode: 2}},
			want:          ExitHook,
			called:        []string{"hook", "delete"},
			framesStopped: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := tc.host
			host.now = time.Unix(1_000_000, 0)
			spec := &Spec{Name: "vt", Bootc: "quay.io/x/y:latest", Artifacts: t.TempDir(), Keep: tc.keep, Checks: tc.checks}

			result, err := host.runner().Run(spec, nil)

			if got := ExitCodeOf(result); got != tc.want {
				t.Errorf("exit code = %d, want %d (err %v, calls %v)", got, tc.want, err, host.calls)
			}
			if (err == nil) != (tc.want == ExitOK) {
				t.Errorf("err = %v, want failure %v", err, tc.want != ExitOK)
			}
			if tc.errText != "" && (err == nil || !strings.Contains(err.Error(), tc.errText)) {
				t.Errorf("err = %v, want it to mention %q", err, tc.errText)
			}
			for _, c := range tc.called {
				if !slices.Contains(host.calls, c) {
					t.Errorf("%s was not called; calls %v", c, host.calls)
				}
			}
			for _, c := range tc.notCalled {
				if slices.Contains(host.calls, c) {
					t.Errorf("%s was called; calls %v", c, host.calls)
				}
			}
			if host.framesStopped != tc.framesStopped {
				t.Errorf("frame capture stopped %d times, want %d", host.framesStopped, tc.framesStopped)
			}
			// The result file is written on every path.
			if _, statErr := os.Stat(filepath.Join(spec.Artifacts, ResultFile)); statErr != nil {
				t.Errorf("no result file: %v", statErr)
			}
		})
	}
}

// TestRunner_Order pins the lifecycle sequence of a passing run: the image is
// prepared and recorded before capture starts, capture starts before the VM,
// and the VM is deleted last.
func TestRunner_Order(t *testing.T) {
	host := &fakeHost{readyBy: readyBySSH, derived: true, hook: &HookResult{Ran: true}, now: time.Unix(0, 0)}
	spec := &Spec{Name: "vt", Bootc: "img", Artifacts: t.TempDir(), Checks: []string{"true"}}
	host.checks = []CheckResult{{Command: "true", Passed: true}}

	result, err := host.runner().Run(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"preflight", "keypair", "layer", "import", "record", "frames", "start", "wait-ready",
		"capture-ready", "hook", "diagnostics", "checks", "delete"}
	if !slices.Equal(host.calls, want) {
		t.Errorf("calls = %v\nwant    %v", host.calls, want)
	}
	if result.BootSeconds != 42 {
		t.Errorf("BootSeconds = %v, want 42 from the injected clock", result.BootSeconds)
	}
	if result.SSH.Port != 2222 || !strings.Contains(result.SSH.Command, "-p 2222") {
		t.Errorf("SSH endpoint not recorded: %+v", result.SSH)
	}
}
