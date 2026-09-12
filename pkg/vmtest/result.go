package vmtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
)

// The run's verdict, as a file a later job step can read and an exit code a
// shell can branch on.
//
// One code per failure class, so a pipeline can tell "the image does not boot"
// from "this runner has no KVM" without parsing prose. The same distinction
// TunaOS's iso-e2e.sh makes with its own exit codes, and for the same reason: a
// gate that reports every failure as 1 gets ignored.
const (
	ExitOK = 0
	// ExitSpec is a spec that cannot run: no image, a bad regular expression.
	ExitSpec = 1
	// ExitHost is this machine's fault, not the image's — no podman, no KVM,
	// no qemu.
	ExitHost = 2
	// ExitLayer is a failure building the derived image: a package that does
	// not exist, a build script that failed.
	ExitLayer = 3
	// ExitBuild is a failure turning the image into a disk (bootc install).
	ExitBuild = 4
	// ExitStart is a failure creating or starting the VM.
	ExitStart = 5
	// ExitNotReady is the image's verdict: it never became ready in time.
	ExitNotReady = 6
	// ExitHook is a post-boot hook that ran and failed.
	ExitHook = 7
	// ExitCheck is an assertion that failed in a guest that did boot.
	ExitCheck = 8
	// ExitBlank is a guest that booted and never painted anything, where the
	// spec required paint.
	ExitBlank = 9
)

// Result is the whole run, written to result.json.
type Result struct {
	Name    string  `json:"name"`
	Image   string  `json:"image"`
	Layer   Layered `json:"layer"`
	Backend string  `json:"backend"`

	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	// BootSeconds is from starting the VM to the guest being ready. The number
	// a pipeline graphs.
	BootSeconds float64 `json:"bootSeconds"`

	// Ready is the headline: did the guest come up.
	Ready bool `json:"ready"`
	// ReadyBy is "ssh" or "console-marker".
	ReadyBy string `json:"readyBy,omitempty"`
	// ReadyEvidence is the matched console line, when that was the gate.
	ReadyEvidence string `json:"readyEvidence,omitempty"`

	// SSH is how to reach the guest afterwards, so the caller can run its own
	// tests without repeating any of this.
	SSH SSHAccess `json:"ssh"`

	// Hook is the post-boot hook's outcome, when the image had one.
	Hook *HookResult `json:"hook,omitempty"`
	// Checks are the assertions, in order.
	Checks []CheckResult `json:"checks,omitempty"`

	// Frames are the captured screenshots, oldest first.
	Frames []qemu.Frame `json:"frames,omitempty"`
	// FinalFrame is the last capture, after the guest was ready.
	FinalFrame *qemu.Frame `json:"finalFrame,omitempty"`
	// Video is the assembled timelapse, when ffmpeg was available.
	Video string `json:"video,omitempty"`
	// SerialLog is the guest console, copied into the artifacts.
	SerialLog string `json:"serialLog,omitempty"`
	// Artifacts is the directory holding all of the above.
	Artifacts string `json:"artifacts"`

	// Kept reports whether the VM is still running.
	Kept bool `json:"kept"`

	// Status is "passed" or "failed", and Failure says what went wrong.
	Status   string `json:"status"`
	ExitCode int    `json:"exitCode"`
	Failure  string `json:"failure,omitempty"`
	// Notes are things a reader should know that are not failures: a skipped
	// video, a fallback generator.
	Notes []string `json:"notes,omitempty"`
}

// SSHAccess is the guest's front door once the run is over.
type SSHAccess struct {
	User         string `json:"user"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	IdentityFile string `json:"identityFile"`
	// Command is a ready-to-paste ssh invocation.
	Command string `json:"command"`
	// Password is the account password the spec asked for, repeated here
	// because a console login needs it and the spec may have generated it.
	Password string `json:"password,omitempty"`
}

// HookResult is what the post-boot hook reported.
type HookResult struct {
	// Ran reports whether a verdict was found at all. A hook that never
	// reported is not a passing hook.
	Ran      bool   `json:"ran"`
	ExitCode int    `json:"exitCode"`
	Log      string `json:"log,omitempty"`
	// Source is "status-file" (read over SSH) or "console" (the marker).
	Source string `json:"source,omitempty"`
}

// CheckResult is one assertion.
type CheckResult struct {
	Command string `json:"command"`
	Passed  bool   `json:"passed"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Note records something worth reading that is not a failure.
func (r *Result) Note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// fail marks the result failed with a code, and returns the error so a caller
// can `return r.fail(...)` in one line.
func (r *Result) fail(code int, err error) error {
	r.Status = "failed"
	r.ExitCode = code
	r.Failure = err.Error()
	return err
}

// pass marks the result passed.
func (r *Result) pass() {
	r.Status = "passed"
	r.ExitCode = ExitOK
}

// ResultFile is the name of the JSON summary inside the artifact directory.
const ResultFile = "result.json"

// Write saves the result. Called even when the run failed — especially then.
func (r *Result) Write(dir string) (string, error) {
	r.Finished = time.Now()
	if r.Status == "" {
		// A run that neither passed nor failed crashed somewhere that did not
		// set a code. Say so rather than reporting a blank verdict.
		r.Status = "failed"
		if r.ExitCode == 0 {
			r.ExitCode = ExitSpec
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ResultFile)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// ExitCodeOf reports the exit code a run should exit with. A nil result (the
// spec never parsed) is a spec error.
func ExitCodeOf(r *Result) int {
	if r == nil {
		return ExitSpec
	}
	return r.ExitCode
}
