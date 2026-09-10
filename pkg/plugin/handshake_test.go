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

// Every first-party plugin must answer the handshake, because core execs it
// for metadata on every discovery pass: a plugin that does not answer is
// invisible in `corral plugin list` and its capabilities never reach the UI.
//
// The SDK unit tests cover the writer and TestMetadataHandshake_RoundTrip
// covers the protocol; this covers the thing that actually ships. It builds
// the real binaries, so it is skipped under -short.
func TestFirstPartyPluginsAnswerTheHandshake(t *testing.T) {
	if testing.Short() {
		t.Skip("builds every plugin binary")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	// corral-incus is a command, not a marketplace plugin, and corral-bootc
	// only builds under its own tag — the tagged build is covered by the bootc
	// job in CI.
	plugins := []string{"auth", "backup", "gpu", "proxmox", "schedule", "snapsched", "vdi", "windows"}

	dir := t.TempDir()
	t.Setenv("CORRAL_PLUGIN_DIR", dir)

	for _, name := range plugins {
		t.Run(name, func(t *testing.T) {
			bin := filepath.Join(dir, "corral-"+name)
			build := exec.Command("go", "build", "-o", bin, "../../cmd/corral-"+name)
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("building corral-%s: %v\n%s", name, err, out)
			}

			meta, err := Inspect(name)
			if err != nil {
				t.Fatalf("corral-%s does not answer --corral-plugin-metadata: %v", name, err)
			}
			if meta.Version == "" {
				t.Error("metadata carries no version; `corral plugin list` shows it")
			}
			if len(meta.SupportedBackends) == 0 {
				t.Error("metadata declares no supportedBackends — marketplace v2 requires it")
			}
			if meta.Description == "" {
				t.Error("metadata carries no description")
			}
		})
	}
}
