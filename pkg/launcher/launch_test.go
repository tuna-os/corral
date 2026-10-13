package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveImage_NoDefaultAsset(t *testing.T) {
	if _, err := ResolveImage(LaunchOpts{Name: "tuna-v1"}); err == nil || !strings.Contains(err.Error(), "no image") {
		t.Errorf("missing image must name the prerequisite, got %v", err)
	}
	im := testImage()
	if _, err := ResolveImage(LaunchOpts{ManifestPath: "/x.json", Image: &im}); err == nil {
		t.Error("both manifest and explicit image must be refused")
	}
}

func TestResolveImage_ExplicitAndManifest(t *testing.T) {
	im := testImage()
	got, err := ResolveImage(LaunchOpts{Image: &im})
	if err != nil {
		t.Fatalf("explicit: %v", err)
	}
	if got.SHA256 != im.SHA256 {
		t.Errorf("explicit round trip wrong")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "m.json")
	body := fmt.Sprintf(`{"version":"1","name":"t","image":{"url":%q,"sha256":%q,"size":%d,"arch":"amd64","firmware":"uefi"}}`,
		im.URL, im.SHA256, im.Size)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveImage(LaunchOpts{ManifestPath: path})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if got.Arch != "amd64" {
		t.Errorf("manifest arch wrong: %+v", got)
	}
}

func TestResolveImage_MismatchRefused(t *testing.T) {
	im := testImage()
	if _, err := ResolveImage(LaunchOpts{Image: &im, Arch: "arm64"}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("arch mismatch must be refused, got %v", err)
	}
	if _, err := ResolveImage(LaunchOpts{Image: &im, Firmware: "bios"}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("firmware mismatch must be refused, got %v", err)
	}
	// Matching overrides pass through.
	if _, err := ResolveImage(LaunchOpts{Image: &im, Arch: "amd64", Firmware: "uefi"}); err != nil {
		t.Errorf("matching overrides must pass: %v", err)
	}
}
