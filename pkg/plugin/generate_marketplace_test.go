package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestGenerateMarketplaceScript runs scripts/generate-marketplace.sh — the
// publish-plugins step that turns marketplace/index.json plus built binaries
// into the published v2 index — against fake binaries. The dist directory also
// holds the Sigstore bundle and SBOM sidecars CI writes next to each binary;
// they must not become platforms or change any digest.
func TestGenerateMarketplaceScript(t *testing.T) {
	for _, tool := range []string{"bash", "jq", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "marketplace", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var curated Index
	if err := json.Unmarshal(raw, &curated); err != nil {
		t.Fatal(err)
	}

	dist := t.TempDir()
	want := map[string]map[string]string{} // plugin -> platform -> sha256
	for _, p := range curated.Plugins {
		if p.Name == "incus" { // the script drops it: Incus is a built-in backend now
			continue
		}
		want[p.Name] = map[string]string{}
		for platform := range p.Platforms {
			arch := strings.TrimPrefix(platform, "linux/")
			file := filepath.Join(dist, "corral-"+p.Name+"-linux-"+arch)
			body := []byte("binary " + p.Name + " " + arch)
			if err := os.WriteFile(file, body, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, sidecar := range []string{".sigstore.json", ".spdx.json", ".sbom.sigstore.json"} {
				if err := os.WriteFile(file+sidecar, []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			sum := sha256.Sum256(body)
			want[p.Name][platform] = hex.EncodeToString(sum[:])
		}
	}

	out := filepath.Join(t.TempDir(), "marketplace-index.json")
	cmd := exec.Command("bash", "scripts/generate-marketplace.sh", dist, "plugins-abc123", out)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GITHUB_REPOSITORY=tuna-os/corral")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate-marketplace.sh: %v\n%s", err, msg)
	}
	if err := ValidateIndexFile(out); err != nil {
		t.Fatalf("generated index does not validate: %v", err)
	}

	raw, err = os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got Index
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Plugins) != len(want) {
		t.Fatalf("got %d plugins, want %d", len(got.Plugins), len(want))
	}
	for _, p := range got.Plugins {
		platforms, ok := want[p.Name]
		if !ok {
			t.Errorf("unexpected plugin %q", p.Name)
			continue
		}
		var gotKeys, wantKeys []string
		for k := range p.Platforms {
			gotKeys = append(gotKeys, k)
		}
		for k := range platforms {
			wantKeys = append(wantKeys, k)
		}
		sort.Strings(gotKeys)
		sort.Strings(wantKeys)
		if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
			t.Errorf("%s platforms = %v, want %v", p.Name, gotKeys, wantKeys)
			continue
		}
		for platform, b := range p.Platforms {
			if b.SHA256 != platforms[platform] {
				t.Errorf("%s %s sha256 = %s, want %s", p.Name, platform, b.SHA256, platforms[platform])
			}
			wantURL := "https://github.com/tuna-os/corral/releases/download/plugins-abc123/corral-" +
				p.Name + "-linux-" + strings.TrimPrefix(platform, "linux/")
			if b.URL != wantURL {
				t.Errorf("%s %s url = %s, want %s", p.Name, platform, b.URL, wantURL)
			}
		}
	}
}
