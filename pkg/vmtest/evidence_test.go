package vmtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/shell"
)

func fixturePNG(t *testing.T, path string, blank bool) *qemu.Frame {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	if !blank {
		for y := 0; y < 16; y++ {
			for x := 8; x < 16; x++ {
				img.Set(x, y, color.White)
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	// Report a dishonest deviation: the adapter must analyze actual bytes.
	return &qemu.Frame{Path: path, StdDev: 1}
}

func TestRequiredCaptureFixtures(t *testing.T) {
	for _, kind := range []string{"valid", "nil", "blank", "unreadable", "empty", "stale", "wrong-run", "symlink", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			host := &fakeHost{readyBy: readyBySSH, now: time.Now()}
			host.captureFn = func(dir string) *qemu.Frame {
				if kind == "nil" {
					return nil
				}
				path := filepath.Join(dir, "ready.png")
				if kind == "wrong-run" {
					path = filepath.Join(t.TempDir(), "ready.png")
				}
				frame := fixturePNG(t, path, kind == "blank")
				switch kind {
				case "unreadable":
					if err := os.WriteFile(path, []byte("not PNG"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "empty":
					if err := os.Truncate(path, 0); err != nil {
						t.Fatal(err)
					}
				case "oversized":
					if err := os.Truncate(path, maxCaptureBytes+1); err != nil {
						t.Fatal(err)
					}
				case "stale":
					old := time.Now().Add(-time.Hour)
					if err := os.Chtimes(path, old, old); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					target := filepath.Join(t.TempDir(), "secret.png")
					fixturePNG(t, target, false)
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
				return frame
			}
			spec := &Spec{Name: "fixture", Bootc: "fixture", Artifacts: t.TempDir(), Screenshots: Screenshots{RequirePaint: true, PublishFinal: true}}
			result, err := host.runner().Run(spec, nil)
			if kind == "valid" {
				if err != nil || result.Evidence.Paint.Outcome != "passed" {
					t.Fatalf("valid: %+v %v", result.Evidence, err)
				}
				cap := result.Evidence.Capture
				data, err := os.ReadFile(filepath.Join(spec.Artifacts, "publication", "final.png"))
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(data)
				if cap.RunID != result.Evidence.RunID || cap.BootID != result.Evidence.BootID || cap.SHA256 != hex.EncodeToString(sum[:]) {
					t.Fatal("capture binding mismatch")
				}
			} else {
				if err == nil || result.ExitCode != ExitBlank || result.Evidence.Paint.Outcome != "failed" {
					t.Fatalf("accepted %s: %+v %v", kind, result.Evidence, err)
				}
				reasons := map[string]string{"nil": "capture-missing-or-error", "blank": "capture-blank", "unreadable": "capture-unreadable", "empty": "capture-empty", "stale": "capture-stale", "wrong-run": "capture-wrong-run-path", "symlink": "capture-unexpected-path", "oversized": "capture-too-large"}
				if result.Evidence.Paint.Reason != reasons[kind] {
					t.Fatalf("reason %q, want %q", result.Evidence.Paint.Reason, reasons[kind])
				}
			}
		})
	}
}

func TestOptionalCaptureAndPublicationBoundary(t *testing.T) {
	const secret = "DUMMY_SENTINEL_PRIVATE_CREDENTIAL"
	host := &fakeHost{readyBy: readyBySSH, now: time.Now(), derived: true, hook: &HookResult{Ran: true, Log: secret}}
	host.captureFn = func(dir string) *qemu.Frame { return fixturePNG(t, filepath.Join(dir, "ready.png"), false) }
	spec := &Spec{Name: secret, Bootc: secret, Artifacts: t.TempDir(), Users: []User{{Name: "test", Password: secret}}, Packages: []string{secret}}
	// Plant operational files that must never be swept into an export.
	for _, name := range []string{"key", "recipe.yaml", "disk.raw", "serial.log", "secret-entry.png"} {
		if err := os.WriteFile(filepath.Join(spec.Artifacts, name), []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := host.runner().Run(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Evidence.Paint.Required {
		t.Fatal("optional capture counted as required")
	}
	if !result.Evidence.Derived || result.Evidence.Classification != "modified-canary" || result.Evidence.ShippedImageClaim {
		t.Fatal("untruthful image classification")
	}
	if result.Evidence.OriginalDigest != "unknown" || result.Evidence.TestedImageID != "unknown" {
		t.Fatal("missing provenance not explicit")
	}
	entries, err := os.ReadDir(filepath.Join(spec.Artifacts, "publication"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "receipt.json" {
		t.Fatalf("unexpected exports: %v", entries)
	}
	data, err := os.ReadFile(filepath.Join(spec.Artifacts, "publication", "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("sentinel leaked")
	}
	var receipt EvidenceReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	// Export must reject a pre-existing path without following it.
	dest := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dest, "publication")); err != nil {
		t.Fatal(err)
	}
	if err := exportEvidence(dest, spec, result); err == nil {
		t.Fatal("accepted symlink publication")
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatal("wrote through publication symlink")
	}
}

func TestEvidenceFailureAndFirstFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "passed-run", true: "failed-run"}[failed], func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "publication"), 0o755); err != nil {
				t.Fatal(err)
			}
			host := &fakeHost{readyBy: readyBySSH, now: time.Now()}
			if failed {
				host.preflightErr = errors.New("first failure")
			}
			result, err := host.runner().Run(&Spec{Name: "fixture", Bootc: "fixture", Artifacts: dir}, nil)
			if err == nil || result.Status != "failed" {
				t.Fatal("export failure became successful")
			}
			if failed {
				if result.ExitCode != ExitHost || !strings.Contains(result.Failure, "first failure") {
					t.Fatal("lost first failure")
				}
			} else if result.ExitCode != ExitEvidence {
				t.Fatal("wrong evidence exit code")
			}
		})
	}
}

func TestOptionalMissingCapture(t *testing.T) {
	host := &fakeHost{readyBy: readyBySSH, now: time.Now()}
	result, err := host.runner().Run(&Spec{Name: "fixture", Bootc: "fixture", Artifacts: t.TempDir()}, nil)
	if err != nil || result.Evidence.Paint.Required || result.Evidence.Paint.Outcome == "passed" {
		t.Fatalf("optional missing: %+v %v", result.Evidence.Paint, err)
	}
}

func TestProductionImageIdentity(t *testing.T) {
	fake := fakeRunner(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	id := "sha256:" + strings.Repeat("b", 64)
	fake.AddPrefixResponse("podman pull", "", nil)
	fake.AddPrefixResponse("podman image inspect --format {{.Digest}}|{{.Id}}", digest+"|"+id, nil)
	spec := &Spec{Bootc: "registry.example/os:mutable"}
	spec.WithDefaults()
	layer, err := (hostImage{}).Layer(spec, "public-key", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if layer.Image != id || layer.ImageID != id || layer.BaseDigest != digest || layer.Derived {
		t.Fatalf("unbound image: %+v", layer)
	}
}

func TestResultWriteFailureFailsGate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ResultFile), 0o755); err != nil {
		t.Fatal(err)
	}
	host := &fakeHost{readyBy: readyBySSH, now: time.Now()}
	result, err := host.runner().Run(&Spec{Name: "fixture", Bootc: "fixture", Artifacts: dir}, nil)
	if err == nil || result.ExitCode != ExitEvidence {
		t.Fatalf("write failure passed: %+v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "publication", "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt EvidenceReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "failed" || receipt.FirstFailure != ExitEvidence {
		t.Fatal("receipt hid result failure")
	}
}

func TestExportBound(t *testing.T) {
	spec := &Spec{Checks: make([]string, 257)}
	result := &Result{Status: "passed", Evidence: newReceipt(spec, "fixture", time.Now())}
	if err := exportEvidence(t.TempDir(), spec, result); err == nil {
		t.Fatal("unbounded assertions accepted")
	}
}

// buildIDRunner simulates podman's build-owned ID file, independently of the
// tag a different build could move immediately after completion.
type buildIDRunner struct {
	shell.Runner
	id string
}

func (b buildIDRunner) Run(name string, args ...string) ([]byte, error) {
	if name == "podman" && len(args) > 0 && args[0] == "build" {
		for i, arg := range args {
			if arg == "--iidfile" && i+1 < len(args) {
				if err := os.WriteFile(args[i+1], []byte(b.id), 0o600); err != nil {
					return nil, err
				}
			}
		}
	}
	return b.Runner.Run(name, args...)
}

func TestDerivedImageUsesBuildOwnedIdentity(t *testing.T) {
	fake := fakeRunner(t)
	noRemora(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	baseID := "sha256:" + strings.Repeat("b", 64)
	builtID := "sha256:" + strings.Repeat("c", 64)
	fake.AddPrefixResponse("podman pull", "", nil)
	fake.AddPrefixResponse("podman image inspect", digest+"|"+baseID, nil)
	fake.AddPrefixResponse("podman build", "", nil)
	SetRunner(buildIDRunner{Runner: fake, id: builtID})
	spec := &Spec{Bootc: "registry.example/os:mutable", Files: []File{{Path: "/etc/test", Content: "fixture"}}}
	spec.WithDefaults()
	dir := t.TempDir()
	layer, err := (hostImage{}).Layer(spec, "public-key", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !layer.Derived || layer.Image != builtID || layer.ImageID != builtID || layer.BaseDigest != digest {
		t.Fatalf("wrong identity: %+v", layer)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Containerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("FROM "+baseID)) {
		t.Fatal("base was not pinned")
	}
}

func TestCaptureTransportErrorFailsRequiredPaint(t *testing.T) {
	spec := &Spec{Name: "absent-vm", Screenshots: Screenshots{RequirePaint: true}}
	result := &Result{Evidence: newReceipt(spec, "fixture", time.Now())}
	run := &Runner{Evidence: hostEvidence{}, Clock: systemClock{}}
	run.capture(spec, t.TempDir(), "ready", result)
	if result.Evidence.Paint.Outcome != "failed" || result.Evidence.Paint.Reason != "capture-missing-or-error" || len(result.Notes) == 0 {
		t.Fatalf("lost transport failure: %+v", result)
	}
}
