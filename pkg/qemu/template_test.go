package qemu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

// infoShim drops a qemu-img shim answering `info --output=json` with canned
// JSON, so template admission is pinned without real QEMU.
func infoShim(t *testing.T, infoJSON string) string {
	t.Helper()
	dir := t.TempDir()
	shim := `#!/bin/sh
if [ "$1" = "info" ]; then cat "$(dirname "$0")/info.json"; exit 0; fi
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "qemu-img"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "info.json"), []byte(infoJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "qemu-img")
}

func TestVerifyTemplateSelfContained(t *testing.T) {
	ctx := context.Background()
	template := filepath.Join(t.TempDir(), "t.qcow2")
	if err := os.WriteFile(template, []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean := `{"format": "qcow2", "virtual-size": 21474836480}`
	if err := verifyTemplateSelfContained(ctx, infoShim(t, clean), template, "qcow2"); err != nil {
		t.Errorf("self-contained template must pass: %v", err)
	}
	backed := `{"format": "qcow2", "backing-filename": "/base/golden.qcow2", "virtual-size": 21474836480}`
	if err := verifyTemplateSelfContained(ctx, infoShim(t, backed), template, "qcow2"); err == nil ||
		!strings.Contains(err.Error(), "backing file") {
		t.Errorf("external backing file must be refused, got %v", err)
	}
	// QEMU's REAL external-data location: format-specific.data.data-file
	// (ImageInfoSpecificQCow2), not a top-level field.
	externalData := `{"format": "qcow2", "virtual-size": 21474836480, "format-specific": {"type": "qcow2", "data": {"data-file": "/base/data.raw"}}}`
	if err := verifyTemplateSelfContained(ctx, infoShim(t, externalData), template, "qcow2"); err == nil ||
		!strings.Contains(err.Error(), "data file") {
		t.Errorf("nested external data file must be refused, got %v", err)
	}
	// A top-level data-file claim trips the fail-closed wire too: no real
	// qemu-img emits one.
	topLevel := `{"format": "qcow2", "data-file": "/base/data.raw", "virtual-size": 21474836480}`
	if err := verifyTemplateSelfContained(ctx, infoShim(t, topLevel), template, "qcow2"); err == nil ||
		!strings.Contains(err.Error(), "data file") {
		t.Errorf("top-level data-file claim must be refused, got %v", err)
	}
	raw := `{"format": "raw", "virtual-size": 1024}`
	if err := verifyTemplateSelfContained(ctx, infoShim(t, raw), template, "qcow2"); err == nil ||
		!strings.Contains(err.Error(), "mismatch") {
		t.Errorf("format mismatch must be refused, got %v", err)
	}
	if err := verifyTemplateSelfContained(ctx, infoShim(t, clean), filepath.Join(t.TempDir(), "missing"), "qcow2"); err == nil {
		t.Error("missing template must be refused")
	}
	empty := filepath.Join(t.TempDir(), "empty.qcow2")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyTemplateSelfContained(ctx, infoShim(t, clean), empty, "qcow2"); err == nil {
		t.Error("empty template must be refused")
	}
	if err := verifyTemplateSelfContained(ctx, infoShim(t, clean), t.TempDir(), "qcow2"); err == nil {
		t.Error("directory template must be refused")
	}
}

func TestVerifyConversionCapacity(t *testing.T) {
	home := t.TempDir()
	SetStateDirs(home, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })
	ctx := context.Background()
	// A small, highly-compressed source with large dense guest extents:
	// the 16-byte file implies a 100 GiB virtual disk. The check must
	// govern on the VIRTUAL size, not the file length.
	template := filepath.Join(t.TempDir(), "t.qcow2")
	if err := os.WriteFile(template, []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dense := `{"format": "qcow2", "virtual-size": 107374182400}`
	// Both branches stubbed: the test must not depend on the host's
	// actual free space.
	orig := dirFreeBytes
	t.Cleanup(func() { dirFreeBytes = orig })
	dirFreeBytes = func(dir string) (uint64, error) { return 200 * 1024 * 1024 * 1024, nil }
	if err := verifyConversionCapacity(ctx, infoShim(t, dense), template); err != nil {
		t.Errorf("roomy disk must pass: %v", err)
	}
	// 10 GiB free dwarfs the 16-byte file plus headroom — but not the
	// 100 GiB virtual disk. Refusal here proves virtual-size governing.
	dirFreeBytes = func(dir string) (uint64, error) { return 10 * 1024 * 1024 * 1024, nil }
	if err := verifyConversionCapacity(ctx, infoShim(t, dense), template); err == nil ||
		!strings.Contains(err.Error(), "converted guest copy") {
		t.Errorf("disk too small for the virtual size must fail naming conversion, got %v", err)
	}
	// A small virtual disk on the same 10 GiB passes.
	small := `{"format": "qcow2", "virtual-size": 1073741824}`
	if err := verifyConversionCapacity(ctx, infoShim(t, small), template); err != nil {
		t.Errorf("small virtual disk on a roomy disk must pass: %v", err)
	}
}

func TestInspectTemplate_RequiresVirtualSize(t *testing.T) {
	template := filepath.Join(t.TempDir(), "t.qcow2")
	if err := os.WriteFile(template, []byte("fake-image-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	noVirtual := `{"format": "qcow2"}`
	if _, err := InspectTemplate(context.Background(), infoShim(t, noVirtual), template); err == nil ||
		!strings.Contains(err.Error(), "virtual size") {
		t.Errorf("missing virtual size must be refused, got %v", err)
	}
}

func TestCreate_RefusesARMBIOS(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	fakeQemuBin(t)
	// Both emulation flags so the spec resolves on any host and the
	// firmware gate — not the accelerator gate — is what refuses.
	err := Create(types.CreateOpts{Name: "armbios", GuestArch: "arm64", AllowEmulation: true, AllowCrossArch: true})
	if err == nil || !strings.Contains(err.Error(), "require UEFI") {
		t.Errorf("arm64+bios create must be refused, got %v", err)
	}
}
