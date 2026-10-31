package vmtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
)

const maxCaptureBytes = 16 << 20

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Assertion uses the outcome vocabulary from tunaOS#3475. Not requested is
// represented as not-run with a reason, never as required coverage.
type Assertion struct {
	Required bool   `json:"required"`
	Outcome  string `json:"outcome"`
	Reason   string `json:"reason"`
}

type CaptureIdentity struct {
	RunID    string    `json:"run"`
	BootID   string    `json:"boot"`
	ID       string    `json:"id"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	SHA256   string    `json:"sha256"`
}

// EvidenceReceipt is Corral's local, versioned adapter for the acceptance
// vocabulary in tunaOS#3475, not a claim of compatibility with a future schema.
// No arbitrary guest strings, commands, paths, recipes or log text cross it.
type EvidenceReceipt struct {
	Schema            string           `json:"schema"`
	Contract          string           `json:"contract"`
	Harness           string           `json:"harness"`
	SourceModified    bool             `json:"sourceModified"`
	Source            string           `json:"source"`
	Environment       string           `json:"environment"`
	RunID             string           `json:"run"`
	Attempt           int              `json:"attempt"`
	BootID            string           `json:"boot"`
	Started           time.Time        `json:"started"`
	Finished          time.Time        `json:"finished"`
	OriginalDigest    string           `json:"originalResolvedDigest"`
	TestedImageID     string           `json:"testedImageID"`
	Derived           bool             `json:"derived"`
	Classification    string           `json:"classification"`
	ShippedImageClaim bool             `json:"shippedImageClaim"`
	Modifications     map[string]int   `json:"modifications"`
	Paint             Assertion        `json:"paint"`
	Assertions        []Assertion      `json:"assertions"`
	Capture           *CaptureIdentity `json:"capture,omitempty"`
	Cleanup           string           `json:"cleanup"`
	Status            string           `json:"status"`
	FirstFailure      int              `json:"firstFailureExitCode"`
	Warnings          []string         `json:"warnings"`
	PublishedCapture  bool             `json:"publishedCapture"`
}

func newReceipt(spec *Spec, id string, now time.Time) EvidenceReceipt {
	source := "unknown"
	modified := false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.modified" {
				modified = setting.Value == "true"
			}
			if setting.Key == "vcs.revision" {
				source = setting.Value
			}
		}
	}
	return EvidenceReceipt{
		Schema: "corral.vmtest.evidence/v1", Contract: "tuna-os/tunaOS#3475:visual-checkpoint-2026-10-29",
		Harness: "corral/vmtest/v1", Source: source, SourceModified: modified, Environment: runtime.Version() + "/" + runtime.GOOS + "/" + runtime.GOARCH,
		RunID: id, Attempt: 1, Started: now, Classification: "modified-canary", Cleanup: "not-created",
		Modifications: map[string]int{"users": len(spec.Users), "packages": len(spec.Packages), "files": len(spec.Files), "provision": len(spec.Provision), "extraRun": len(spec.ExtraRun), "kernelArguments": len(spec.kargs()), "sshKeyInjection": 1, "rootPassword": boolCount(spec.RootPassword != "")},
		Paint:         Assertion{Required: spec.Screenshots.RequirePaint, Outcome: "not-run", Reason: "capture-not-requested"},
	}
}
func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// capture binds a single transport call to an exclusive current-run directory.
// The byte snapshot, rather than a caller-supplied blankness number or a later
// file read, is the evidence validated and eventually published.
func (r *Runner) capture(spec *Spec, artifacts, label string, result *Result) {
	e := &result.Evidence
	e.Paint = Assertion{Required: spec.Screenshots.RequirePaint, Outcome: "failed", Reason: "capture-error"}
	defer func() {
		if !e.Paint.Required && e.Paint.Outcome == "failed" {
			e.Paint.Outcome = "unsupported"
		}
	}()
	dir, err := os.MkdirTemp(artifacts, "capture-")
	if err != nil {
		return
	}
	began := time.Now()
	started := r.Clock.Now()
	frame := r.Evidence.CaptureFinal(spec, dir, label, result)
	result.FinalFrame = frame
	if frame == nil {
		e.Paint.Reason = "capture-missing-or-error"
		return
	}
	data, reason := readCapture(dir, frame.Path, began)
	if reason != "" {
		e.Paint.Reason = reason
		return
	}
	analyzed, err := qemu.AnalyzePNGData(data)
	if err != nil {
		e.Paint.Reason = "capture-unreadable"
		return
	}
	analyzed.Path = frame.Path
	result.FinalFrame = &analyzed
	sum := sha256.Sum256(data)
	e.Capture = &CaptureIdentity{RunID: e.RunID, BootID: e.BootID, ID: filepath.Base(dir), Started: started, Finished: r.Clock.Now(), SHA256: hex.EncodeToString(sum[:])}
	result.captureData = data
	if analyzed.Blank() {
		e.Paint.Reason = "capture-blank"
		return
	}
	e.Paint.Outcome, e.Paint.Reason = "passed", "current-readable-nonblank-frame"
}

func readCapture(dir, path string, began time.Time) ([]byte, string) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, "capture-wrong-run-path"
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "capture-unreadable"
	}
	defer func() { _ = root.Close() }()
	// Root.Open prevents symlink escapes; reject internal symlinks as well.
	for current := rel; current != "."; current = filepath.Dir(current) {
		st, err := root.Lstat(current)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || (current == rel && !st.Mode().IsRegular()) {
			return nil, "capture-unexpected-path"
		}
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, "capture-unreadable"
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, "capture-unexpected-path"
	}
	if st.ModTime().Before(began.Truncate(time.Second)) {
		return nil, "capture-stale"
	}
	if st.Size() == 0 {
		return nil, "capture-empty"
	}
	if st.Size() > maxCaptureBytes {
		return nil, "capture-too-large"
	}
	data, err := io.ReadAll(io.LimitReader(f, maxCaptureBytes+1))
	if err != nil || len(data) > maxCaptureBytes {
		return nil, "capture-unreadable"
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 16_000_000 {
		return nil, "capture-unreadable"
	}
	return data, ""
}

// prepareEvidence redacts opaque text by omission. Only fixed reason codes
// and assertion indices are published.
func prepareEvidence(spec *Spec, result *Result) {
	e := &result.Evidence
	e.Status, e.FirstFailure = result.Status, result.ExitCode
	e.Assertions = nil
	e.Warnings = nil
	if e.Source == "unknown" {
		e.Warnings = append(e.Warnings, "harness-source-unknown")
	}
	if e.SourceModified {
		e.Warnings = append(e.Warnings, "harness-source-modified")
	}
	if result.Ready {
		e.Assertions = append(e.Assertions, Assertion{true, "passed", "guest-ready"})
	} else {
		e.Assertions = append(e.Assertions, Assertion{true, "failed", "guest-not-ready"})
	}
	if result.Hook != nil {
		outcome := "failed"
		if result.Hook.Ran && result.Hook.ExitCode == 0 {
			outcome = "passed"
		}
		e.Assertions = append(e.Assertions, Assertion{true, outcome, "post-boot-hook"})
	}
	for i := range spec.Checks {
		outcome := "not-run"
		if i < len(result.Checks) {
			outcome = "failed"
			if result.Checks[i].Passed {
				outcome = "passed"
			}
		}
		e.Assertions = append(e.Assertions, Assertion{true, outcome, fmt.Sprintf("guest-check-%d", i+1)})
	}
	if !digestPattern.MatchString(e.OriginalDigest) {
		e.OriginalDigest = "unknown"
		e.Warnings = append(e.Warnings, "original-image-identity-missing")
	}
	if !digestPattern.MatchString(e.TestedImageID) {
		e.TestedImageID = "unknown"
		e.Warnings = append(e.Warnings, "tested-image-identity-missing")
	}
	e.Warnings = append(e.Warnings, "opaque-text-redacted", "frames-and-video-excluded-unless-final-explicitly-safe")
	if len(result.Notes) > 0 {
		e.Warnings = append(e.Warnings, "operational-warnings-present")
	}
	e.PublishedCapture = spec.Screenshots.PublishFinal && len(result.captureData) > 0 && e.Capture != nil
}

// exportEvidence constructs an allowlist without walking the operational tree.
func exportEvidence(dir string, spec *Spec, result *Result) error {
	prepareEvidence(spec, result)
	e := &result.Evidence
	if len(spec.Checks) > 256 {
		return fmt.Errorf("publication assertion limit exceeded")
	}
	if spec.Screenshots.RequirePaint && e.Paint.Outcome != "passed" && result.Status == "passed" {
		return fmt.Errorf("required capture has not passed")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	// Exclusive publication directory: never retain files from an older export.
	if err := root.Mkdir("publication", 0o755); err != nil {
		return err
	}
	pub, err := root.OpenRoot("publication")
	if err != nil {
		return err
	}
	defer func() { _ = pub.Close() }()
	write := func(name string, data []byte) error {
		f, err := pub.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			return writeErr
		}
		return closeErr
	}
	if spec.Screenshots.PublishFinal && len(result.captureData) > 0 && e.Capture != nil {
		if err := write("final.png", result.captureData); err != nil {
			return err
		}
		e.PublishedCapture = true
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 256<<10 {
		return fmt.Errorf("publication receipt limit exceeded")
	}
	return write("receipt.json", append(data, '\n'))
}
