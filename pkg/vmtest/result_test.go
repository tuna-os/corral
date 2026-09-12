package vmtest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/qemu"
)

func TestResultWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "out")
	result := &Result{
		Name:  "gate",
		Image: "quay.io/fedora/fedora-bootc:41",
		Ready: true,
		Checks: []CheckResult{
			{Command: "systemctl is-active sshd", Passed: true, Output: "active"},
		},
		FinalFrame: &qemu.Frame{Path: "ready.png", Width: 1280, Height: 800, StdDev: 0.31},
	}
	result.pass()

	path, err := result.Write(dir)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the result: %v", err)
	}

	// It is read by other programs, so the shape matters as much as the content.
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("the result is not valid JSON: %v", err)
	}
	if parsed["status"] != "passed" || parsed["exitCode"] != float64(ExitOK) {
		t.Errorf("status/exitCode = %v/%v", parsed["status"], parsed["exitCode"])
	}
	if parsed["finished"] == nil {
		t.Error("the result should record when it finished")
	}
	if !strings.HasSuffix(path, ResultFile) {
		t.Errorf("result path = %q", path)
	}
}

func TestResultWrite_AVerdictlessRunIsAFailure(t *testing.T) {
	// A run that returned without setting a verdict crashed somewhere. Reporting
	// it as blank would let a pipeline read it as success.
	result := &Result{Name: "gate"}
	dir := t.TempDir()
	if _, err := result.Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if result.Status != "failed" || result.ExitCode == ExitOK {
		t.Errorf("status/exit = %q/%d, want a failure", result.Status, result.ExitCode)
	}
}

func TestResultFail(t *testing.T) {
	result := &Result{}
	err := result.fail(ExitNotReady, errors.New("never became ready"))
	if err == nil {
		t.Fatal("fail should return the error so a caller can return it")
	}
	if result.Status != "failed" || result.ExitCode != ExitNotReady {
		t.Errorf("result = %+v", result)
	}
	if result.Failure != "never became ready" {
		t.Errorf("failure = %q", result.Failure)
	}
}

func TestResultNote(t *testing.T) {
	result := &Result{}
	result.Note("no timelapse: %v", errors.New("ffmpeg is not installed"))
	if len(result.Notes) != 1 || !strings.Contains(result.Notes[0], "ffmpeg") {
		t.Errorf("notes = %v", result.Notes)
	}
}

func TestExitCodeOf(t *testing.T) {
	if got := ExitCodeOf(nil); got != ExitSpec {
		t.Errorf("a run with no result = %d, want %d", got, ExitSpec)
	}
	if got := ExitCodeOf(&Result{ExitCode: ExitBlank}); got != ExitBlank {
		t.Errorf("exit code = %d", got)
	}
}

func TestExitCodesAreDistinct(t *testing.T) {
	// A pipeline branches on these. Two failure classes sharing a code would
	// make that impossible, and it is an easy mistake to make while editing.
	seen := map[int]string{}
	for name, code := range map[string]int{
		"ok": ExitOK, "spec": ExitSpec, "host": ExitHost, "layer": ExitLayer,
		"build": ExitBuild, "start": ExitStart, "not-ready": ExitNotReady,
		"hook": ExitHook, "check": ExitCheck, "blank": ExitBlank,
	} {
		if other, clash := seen[code]; clash {
			t.Errorf("%s and %s share exit code %d", name, other, code)
		}
		seen[code] = name
	}
}
