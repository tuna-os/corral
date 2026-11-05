package macos

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mediaDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func TestPinnedDownloadPublishesOnlyVerifiedContent(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid", false: "checksum-mismatch"}[valid], func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte("pinned source"))
			}))
			defer server.Close()
			dir := t.TempDir()
			path := filepath.Join(dir, "source.py")
			want := mediaDigest("pinned source")
			if !valid {
				want = mediaDigest("different source")
			}
			err := ensurePinned(context.Background(), server.Client(), server.URL, path, want)
			if !valid {
				if err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
					t.Fatalf("want checksum failure, got %v", err)
				}
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unverified source was published: %v", err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 0 {
					t.Fatalf("partial download not cleaned: %v, %v", entries, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := ensurePinned(context.Background(), server.Client(), server.URL, path, want); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 {
				t.Fatalf("verified cache was downloaded again: %d requests", requests.Load())
			}
			if err := os.WriteFile(path, []byte("modified source"), 0o600); err != nil {
				t.Fatal(err)
			}
			err = ensurePinned(context.Background(), server.Client(), server.URL, path, want)
			if err == nil || !strings.Contains(err.Error(), "SHA256 mismatch for cached") {
				t.Fatalf("modified cache accepted: %v", err)
			}
			if requests.Load() != 1 {
				t.Fatal("corrupt cache silently overwritten")
			}
		})
	}
}

func TestPinnedDownloadCancellationDoesNotPublishPartial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "OpenCore.qcow2")
	err := ensurePinned(ctx, server.Client(), server.URL, path, mediaDigest("complete"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial image was published: %v", err)
	}
}

func TestCompletedRecoveryRequiresMatchingSourceAndCompleteImage(t *testing.T) {
	qemu := requireQEMU(t)
	dir := t.TempDir()
	image := filepath.Join(dir, "Recovery.qcow2")
	receipt := image + ".verified.json"
	raw := filepath.Join(dir, "recovery.raw")
	if err := os.WriteFile(raw, []byte(strings.Repeat("verified Apple recovery bytes", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runMediaCommand(context.Background(), qemu, "convert", "-f", "raw", "-O", "qcow2", raw, image); err != nil {
		t.Fatal(err)
	}
	if ok, err := completedRecovery(context.Background(), image, receipt, "source"); err != nil || ok {
		t.Fatalf("image without receipt accepted: %t, %v", ok, err)
	}
	hash, err := fileSHA256(context.Background(), image)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(recoveryReceipt{1, "source", hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipt, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := completedRecovery(context.Background(), image, receipt, "source"); err != nil || !ok {
		t.Fatalf("matching image rejected: %t, %v", ok, err)
	}
	if ok, err := completedRecovery(context.Background(), image, receipt, "different source"); err != nil || ok {
		t.Fatalf("wrong source accepted: %t, %v", ok, err)
	}
	if err := os.Truncate(image, 5); err != nil {
		t.Fatal(err)
	}
	if ok, err := completedRecovery(context.Background(), image, receipt, "source"); err != nil || ok {
		t.Fatalf("truncated image accepted: %t, %v", ok, err)
	}
	if err := os.WriteFile(receipt, []byte("{partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := completedRecovery(context.Background(), image, receipt, "source"); err != nil || ok {
		t.Fatalf("partial receipt accepted: %t, %v", ok, err)
	}
}

func TestPrepareMediaCanceledBeforeFilesystemChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := filepath.Join(t.TempDir(), "cache")
	if _, err := PrepareMedia(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled prepare changed filesystem: %v", err)
	}
}

func TestRecoveryVerifierNonTTYGeometryKeepsAssertionsEnabled(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	// The upstream verifier relies on assert, so inherited optimization must
	// not disable signature checks. Invalid fd guarantees a non-TTY path.
	t.Setenv("PYTHONOPTIMIZE", "2")
	code := terminalGeometry + `import sys
if sys.flags.optimize != 0:
    raise RuntimeError("assertions disabled")
if os.get_terminal_size(-1) != os.terminal_size((80, 24)):
    raise RuntimeError("non-TTY geometry not supplied")
try:
    assert False
except AssertionError:
    pass
else:
    raise RuntimeError("verification assertions ignored")
`
	if err := runMediaCommand(context.Background(), python, "-I", "-c", code); err != nil {
		t.Fatal(err)
	}
}

func TestMediaCommandHonorsCancellation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := runMediaCommand(ctx, python, "-I", "-c", "import time; time.sleep(60)"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
}

func TestMediaCommandOutputRetainsBoundedTail(t *testing.T) {
	var output mediaCommandOutput
	input := strings.Repeat("a", 5000) + "final diagnosis"
	for start := 0; start < len(input); start += 117 {
		end := min(start+117, len(input))
		if n, err := output.Write([]byte(input[start:end])); err != nil || n != end-start {
			t.Fatalf("write: %d, %v", n, err)
		}
	}
	if got := string(output.data); got != input[len(input)-4096:] {
		t.Fatal("command output did not retain the final diagnosis")
	}
}

func requireQEMU(t *testing.T) string {
	t.Helper()
	qemu, err := exec.LookPath("qemu-img")
	if err != nil {
		t.Skip("qemu-img unavailable")
	}
	return qemu
}

func TestRecoveryCacheRejectsExternalDependenciesAndRebuildsStandalone(t *testing.T) {
	qemu := requireQEMU(t)
	for _, kind := range []string{"backing-file", "external-data"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			work := filepath.Join(dir, "work")
			if err := os.Mkdir(work, 0o700); err != nil {
				t.Fatal(err)
			}
			raw := filepath.Join(work, "verified.raw")
			if err := os.WriteFile(raw, []byte(strings.Repeat("verified recovery content", 8192)), 0o600); err != nil {
				t.Fatal(err)
			}
			image := filepath.Join(dir, "Recovery.qcow2")
			dependency := raw
			if kind == "backing-file" {
				if err := runMediaCommand(ctx, qemu, "create", "-f", "qcow2", "-F", "raw", "-b", raw, image); err != nil {
					t.Fatal(err)
				}
			} else {
				dependency = filepath.Join(dir, "external.raw")
				if err := runMediaCommand(ctx, qemu, "convert", "-f", "raw", "-O", "qcow2", "-o", "data_file="+dependency+",data_file_raw=on", raw, image); err != nil {
					t.Fatal(err)
				}
			}
			// Both containers expose exactly the verified logical contents:
			// compare alone cannot establish a self-contained image.
			if err := runMediaCommand(ctx, qemu, "compare", "-f", "raw", "-F", "qcow2", raw, image); err != nil {
				t.Fatal(err)
			}
			hash, err := fileSHA256(ctx, image)
			if err != nil {
				t.Fatal(err)
			}
			receipt := image + ".verified.json"
			if err := writePrivateJSON(ctx, receipt, recoveryReceipt{1, "source", hash}); err != nil {
				t.Fatal(err)
			}
			if ok, err := completedRecovery(ctx, image, receipt, "source"); err != nil || ok {
				t.Fatalf("dependent cache accepted: %t, %v", ok, err)
			}
			if err := convertRecoveryRaw(ctx, qemu, raw, image); err != nil {
				t.Fatal(err)
			}
			if ok, err := standaloneQCOW2(ctx, qemu, image); err != nil || !ok {
				t.Fatalf("rebuilt image has external dependencies: %t, %v", ok, err)
			}
			if err := runMediaCommand(ctx, qemu, "compare", "-f", "raw", "-F", "qcow2", raw, image); err != nil {
				t.Fatal(err)
			}
			before, err := fileSHA256(ctx, image)
			if err != nil {
				t.Fatal(err)
			}
			// A standalone matching image is adopted without replacing it.
			if err := convertRecoveryRaw(ctx, qemu, raw, image); err != nil {
				t.Fatal(err)
			}
			after, err := fileSHA256(ctx, image)
			if err != nil || after != before {
				t.Fatalf("standalone cache changed: %v", err)
			}
			expected := filepath.Join(dir, "expected.raw")
			if err := runMediaCommand(ctx, qemu, "convert", "-f", "qcow2", "-O", "raw", image, expected); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dependency, []byte("modified external dependency"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := runMediaCommand(ctx, qemu, "compare", "-f", "raw", "-F", "qcow2", expected, image); err != nil {
				t.Fatalf("published image still depends on external content: %v", err)
			}
		})
	}
}

func TestRecoveryConversionCancellationPreservesExistingImage(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "Recovery.qcow2")
	const previous = "previous recovery image"
	if err := os.WriteFile(image, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := convertRecoveryRaw(ctx, "unused", filepath.Join(dir, "missing.raw"), image); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	body, err := os.ReadFile(image)
	if err != nil || string(body) != previous {
		t.Fatalf("canceled conversion replaced existing image: %v", err)
	}
}

func TestRecoveryProvenanceRejectsUnknownAndOtherReleaseMedia(t *testing.T) {
	for _, location := range []string{"root", "stage"} {
		t.Run(location, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			appleDir := dir
			if location == "stage" {
				appleDir = filepath.Join(dir, ".apple-recovery")
				if err := os.Mkdir(appleDir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"BaseSystem.dmg", "BaseSystem.chunklist"} {
				if err := os.WriteFile(filepath.Join(appleDir, name), []byte("legacy signed Sonoma media"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := prepareAppleRecovery(ctx, "must-not-execute", "unused", dir); err == nil || !strings.Contains(err.Error(), "unknown provenance") {
				t.Fatalf("legacy cache was adopted: %v", err)
			}
			receipt := recoveryRequest()
			receipt.Release = "sonoma"
			if err := writePrivateJSON(ctx, filepath.Join(appleDir, "recovery-request.json"), receipt); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareAppleRecovery(ctx, "must-not-execute", "unused", dir); err == nil || !strings.Contains(err.Error(), "different release") {
				t.Fatalf("Sonoma provenance was adopted: %v", err)
			}
		})
	}
}

func TestRecoveryProvenanceBindsBothVerifiedInputs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	for _, name := range []string{"BaseSystem.dmg", "BaseSystem.chunklist"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writePrivateJSON(ctx, filepath.Join(dir, "recovery-request.json"), recoveryRequest()); err != nil {
		t.Fatal(err)
	}
	if err := readRecoveryProvenance(ctx, dir, false); err != nil {
		t.Fatalf("interrupted request lost its release binding: %v", err)
	}
	if err := readRecoveryProvenance(ctx, dir, true); err == nil {
		t.Fatal("unbound root inputs accepted")
	}
	if err := bindRecoveryProvenance(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := readRecoveryProvenance(ctx, dir, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"BaseSystem.dmg", "BaseSystem.chunklist"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("different"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := readRecoveryProvenance(ctx, dir, false); err == nil {
			t.Fatalf("modified %s retained provenance", name)
		}
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKextExtractionRejectsUnsafeOrIncompleteArchives(t *testing.T) {
	for _, mutation := range []string{"none", "traversal", "symlink", "missing-binary", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "release.zip")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			members := []string{"VMHide.kext/", "VMHide.kext/Contents/Info.plist", "VMHide.kext.dSYM/ignored"}
			if mutation != "missing-binary" {
				members = append(members, "VMHide.kext/Contents/MacOS/VMHide")
			}
			if mutation == "traversal" {
				members = append(members, "VMHide.kext/../../escape")
			}
			if mutation == "duplicate" {
				members = append(members, "VMHide.kext/Contents/Info.plist")
			}
			for _, name := range members {
				header := &zip.FileHeader{Name: name, Method: zip.Deflate}
				if strings.HasSuffix(name, "/") {
					header.SetMode(os.ModeDir | 0o755)
				} else if mutation == "symlink" && strings.HasSuffix(name, "/VMHide") {
					header.SetMode(os.ModeSymlink | 0o777)
				} else {
					header.SetMode(0o644)
				}
				member, err := writer.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasSuffix(name, "/") {
					if _, err := member.Write([]byte("published bytes")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			err = extractKext(context.Background(), path, dir, "VMHide.kext")
			if mutation == "none" {
				if err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(filepath.Join(dir, "VMHide.kext", "Contents", "MacOS", "VMHide"))
				if err != nil || string(body) != "published bytes" {
					t.Fatalf("published binary changed: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "VMHide.kext.dSYM")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("debug symbols were extracted")
				}
			} else if err == nil {
				t.Fatalf("unsafe archive accepted: %s", mutation)
			}
		})
	}
}
