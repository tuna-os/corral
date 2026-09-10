package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMetadataHandshake_RoundTrip closes the loop on the plugin contract:
// sdk.HandleMetadata writes the JSON, and Inspect — the discovery path core
// runs for every installed plugin — decodes it. Each side has its own unit
// tests; only this one fails if the two ever stop agreeing.
func TestMetadataHandshake_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin binary")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	dir := t.TempDir()
	t.Setenv("CORRAL_PLUGIN_DIR", dir)

	bin := filepath.Join(dir, "corral-fake")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./testdata/fakeplugin")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fake plugin: %v\n%s", err, out)
	}

	meta, err := Inspect("fake")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if meta.API != "corral.plugin/v1" {
		t.Errorf("API = %q, want the SDK default filled in by the handshake", meta.API)
	}
	if meta.Name != "fake" || meta.Version != "0.1.0" {
		t.Errorf("metadata = %+v, want name fake version 0.1.0", meta)
	}
	if len(meta.Capabilities) != 1 || meta.Capabilities[0] != "cli-command" {
		t.Errorf("Capabilities = %v, want [cli-command]", meta.Capabilities)
	}
	if len(meta.SupportedBackends) != 1 || meta.SupportedBackends[0] != "all" {
		t.Errorf("SupportedBackends = %v, want [all]", meta.SupportedBackends)
	}
}

// A binary that does not implement the handshake must be reported as such
// rather than silently yielding empty metadata.
func TestInspect_NonPluginBinary(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CORRAL_PLUGIN_DIR", dir)
	writeExecutable(t, filepath.Join(dir, "corral-notaplugin"), "#!/bin/sh\nexit 3\n")

	if _, err := Inspect("notaplugin"); err == nil {
		t.Fatal("Inspect on a binary with no metadata handshake should fail")
	}
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
