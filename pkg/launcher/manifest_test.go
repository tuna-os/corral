package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testImage() ManifestImage {
	data := []byte("tuna-image-bytes")
	sum := sha256.Sum256(data)
	return ManifestImage{
		URL:      "https://example.com/tuna.qcow2",
		SHA256:   hex.EncodeToString(sum[:]),
		Size:     int64(len(data)),
		Arch:     "amd64",
		Firmware: "uefi",
		Format:   "qcow2",
	}
}

func TestManifestValidate_OK(t *testing.T) {
	m := Manifest{Version: "1", Name: "tuna-v1", Image: testImage()}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestManifestValidate_FailClosed(t *testing.T) {
	base := testImage()
	for _, tc := range []struct {
		name   string
		mutate func(*ManifestImage)
		want   string
	}{
		{name: "http refused", mutate: func(im *ManifestImage) { im.URL = "http://example.com/tuna.qcow2" }, want: "https://"},
		{name: "empty url", mutate: func(im *ManifestImage) { im.URL = "" }, want: "url"},
		{name: "short digest", mutate: func(im *ManifestImage) { im.SHA256 = "abc" }, want: "sha256"},
		{name: "non-hex digest", mutate: func(im *ManifestImage) { im.SHA256 = strings.Repeat("z", 64) }, want: "hex"},
		{name: "uppercase digest", mutate: func(im *ManifestImage) { im.SHA256 = strings.ToUpper(im.SHA256) }, want: "lowercase"},
		{name: "zero size", mutate: func(im *ManifestImage) { im.Size = 0 }, want: "size"},
		{name: "bad arch", mutate: func(im *ManifestImage) { im.Arch = "riscv64" }, want: "arch"},
		{name: "bad firmware", mutate: func(im *ManifestImage) { im.Firmware = "seabios" }, want: "firmware"},
		{name: "bad format", mutate: func(im *ManifestImage) { im.Format = "vmdk" }, want: "format"},
		{name: "negative virtual size", mutate: func(im *ManifestImage) { im.VirtualSize = -1 }, want: "virtual_size"},
	} {
		im := base
		tc.mutate(&im)
		if err := im.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.want)
		}
	}
	if err := (Manifest{Version: "2", Name: "x", Image: base}).Validate(); err == nil {
		t.Error("version 2 must be refused")
	}
	if err := (Manifest{Version: "1", Image: base}).Validate(); err == nil {
		t.Error("empty name must be refused")
	}
}

func TestParseManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tuna.json")
	im := testImage()
	body := fmt.Sprintf(`{"version":"1","name":"tuna-v1","image":{"url":%q,"sha256":%q,"size":%d,"arch":"amd64","firmware":"uefi","format":"qcow2"}}`,
		im.URL, im.SHA256, im.Size)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(path)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Image.SHA256 != im.SHA256 || m.Image.Size != im.Size {
		t.Errorf("manifest round trip wrong: %+v", m.Image)
	}
	if _, err := ParseManifest(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file must error")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":"1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(bad); err == nil {
		t.Error("invalid manifest must error")
	}
}

func TestManifestValidate_VirtualSizeOptional(t *testing.T) {
	im := testImage()
	im.VirtualSize = 20 * 1024 * 1024 * 1024
	if err := im.Validate(); err != nil {
		t.Errorf("stated virtual size must validate: %v", err)
	}
	im.VirtualSize = 0
	if err := im.Validate(); err != nil {
		t.Errorf("unstated virtual size must validate: %v", err)
	}
}

func TestParseManifest_Bounded(t *testing.T) {
	dir := t.TempDir()
	// A FIFO cannot hang the parse: non-regular files are refused before
	// a byte is read.
	fifo := filepath.Join(dir, "trap.json")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ParseManifest(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "non-regular") {
			t.Errorf("FIFO manifest must be refused as non-regular, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ParseManifest hung on a FIFO")
	}
	// An unbounded file is refused on size, not read whole.
	big := filepath.Join(dir, "big.json")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, maxManifestBytes+1)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := ParseManifest(big); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("overlong manifest must be refused, got %v", err)
	}
}

func TestVerifyBytes(t *testing.T) {
	data := []byte("tuna-image-bytes")
	im := testImage()
	if err := im.VerifyBytes(data); err != nil {
		t.Fatalf("VerifyBytes: %v", err)
	}
	if err := im.VerifyBytes([]byte("tuna-image-bytez")); err == nil {
		t.Error("one-byte digest change must fail")
	}
	if err := im.VerifyBytes([]byte("short")); err == nil {
		t.Error("length change must fail")
	}
}

func TestCacheKey_IgnoresBasename(t *testing.T) {
	a := testImage()
	a.URL = "https://a.example.com/tuna.qcow2"
	b := testImage()
	b.URL = "https://b.example.com/tuna.qcow2"
	b.SHA256 = strings.Repeat("1", 64)
	if a.CacheKey() == b.CacheKey() {
		t.Error("same basename with different digests must not share a cache key")
	}
	if !strings.Contains(a.CacheKey(), a.SHA256) {
		t.Errorf("cache key must carry the digest: %q", a.CacheKey())
	}
}
