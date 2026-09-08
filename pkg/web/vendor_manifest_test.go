package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
)

type vendorManifest struct {
	SchemaVersion string `json:"schemaVersion"`
	Assets        []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Bytes  int    `json:"bytes"`
	} `json:"assets"`
}

// TestVendorManifest verifies that the provenance manifest describes the exact
// third-party browser assets embedded in the binary. This makes asset updates
// fail CI unless their recorded digest and size are updated at the same time.
func TestVendorManifest(t *testing.T) {
	raw, err := staticFS.ReadFile("static/vendor/MANIFEST.json")
	if err != nil {
		t.Fatalf("read vendor manifest: %v", err)
	}

	var manifest vendorManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse vendor manifest: %v", err)
	}
	if manifest.SchemaVersion != "corral.vendor/v1" {
		t.Fatalf("schemaVersion = %q, want corral.vendor/v1", manifest.SchemaVersion)
	}

	recorded := make(map[string]bool, len(manifest.Assets))
	for _, asset := range manifest.Assets {
		if recorded[asset.Path] {
			t.Errorf("duplicate manifest path %q", asset.Path)
			continue
		}
		recorded[asset.Path] = true

		data, err := staticFS.ReadFile("static/" + asset.Path)
		if err != nil {
			t.Errorf("read manifest asset %q: %v", asset.Path, err)
			continue
		}
		if len(data) != asset.Bytes {
			t.Errorf("%s bytes = %d, manifest records %d", asset.Path, len(data), asset.Bytes)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); !strings.EqualFold(got, asset.SHA256) {
			t.Errorf("%s sha256 = %s, manifest records %s", asset.Path, got, asset.SHA256)
		}
	}

	var embedded []string
	err = fs.WalkDir(staticFS, "static", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(filePath, "static/")
		if strings.HasPrefix(rel, "vendor/") && rel != "vendor/MANIFEST.json" && rel != "vendor/README.md" {
			embedded = append(embedded, rel)
		} else if path.Dir(rel) == "." && strings.HasSuffix(rel, ".min.js") {
			embedded = append(embedded, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("enumerate embedded vendor assets: %v", err)
	}
	sort.Strings(embedded)
	for _, asset := range embedded {
		if !recorded[asset] {
			t.Errorf("embedded vendor asset %q is missing from vendor/MANIFEST.json", asset)
		}
	}
}
