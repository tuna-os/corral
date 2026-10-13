package launcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prepareImage writes data to a temp source file and returns the manifest
// image plus a LaunchOpts admitting it via file:// with stubbed probes.
func prepareImage(t *testing.T, data []byte, accel func(arch string, e, c bool) (string, bool, error)) LaunchOpts {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src.qcow2")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	im := downloadImage("file://"+src, data)
	return LaunchOpts{
		Name:  "tuna-v1",
		Image: &im,
		PreflightProbes: &PreflightOpts{
			HostOS:          "linux",
			HostArch:        "amd64",
			QEMUProbe:       func(arch string) error { return nil },
			FirmwareProbe:   func(arch, fw string) error { return nil },
			AccelProbe:      accel,
			FreeBytes:       func(path string) (uint64, error) { return 100 * 1024 * 1024 * 1024, nil },
			TemplateInspect: func(ctx context.Context, arch, template string) (uint64, error) { return 1 << 30, nil },
		},
	}
}

func emulationOnlyAccel(arch string, e, c bool) (string, bool, error) {
	if e || c {
		return "tcg", false, nil
	}
	return "", false, fmt.Errorf("native kvm unavailable on linux/amd64 — pass --allow-emulation for slower TCG emulation (never reported as native)")
}

func TestPrepare_HonorsAllowEmulation(t *testing.T) {
	data := []byte("prepare-image-bytes")
	// Without the flag, the native gate refuses.
	opts := prepareImage(t, data, emulationOnlyAccel)
	opts.VMHome = t.TempDir()
	if _, _, err := Prepare(opts); err == nil || !strings.Contains(err.Error(), "--allow-emulation") {
		t.Fatalf("strict prepare must name --allow-emulation, got %v", err)
	}
	// With the flag, the single preflight resolves TCG and admission
	// proceeds — the old code returned the strict error even here.
	opts = prepareImage(t, data, emulationOnlyAccel)
	opts.VMHome = t.TempDir()
	opts.AllowEmulation = true
	imagePath, im, err := Prepare(opts)
	if err != nil {
		t.Fatalf("allowed prepare: %v", err)
	}
	if err := VerifyFile(imagePath, im); err != nil {
		t.Errorf("prepared image does not verify: %v", err)
	}
}

func TestPrepare_HonorsAllowCrossArch(t *testing.T) {
	data := []byte("cross-arch-bytes!")
	accel := func(arch string, e, c bool) (string, bool, error) {
		if c {
			return "tcg", false, nil
		}
		return "", false, fmt.Errorf("arm64 guest on amd64 host needs --allow-cross-arch")
	}
	opts := prepareImage(t, data, accel)
	opts.VMHome = t.TempDir()
	opts.Image.Arch = "arm64" // guest differs from the stubbed amd64 host
	opts.AllowCrossArch = true
	if _, _, err := Prepare(opts); err != nil {
		t.Fatalf("cross-arch prepare: %v", err)
	}
}

func TestPrepare_OtherGateFailureSurvivesFlags(t *testing.T) {
	data := []byte("prepare-image-bytes")
	opts := prepareImage(t, data, emulationOnlyAccel)
	opts.VMHome = t.TempDir()
	opts.AllowEmulation = true
	// Emulation is allowed but QEMU is missing: the error must name QEMU,
	// the real failure — not the accelerator, and not a stale strict error.
	opts.PreflightProbes.QEMUProbe = func(arch string) error { return fmt.Errorf("QEMU missing") }
	if _, _, err := Prepare(opts); err == nil || !strings.Contains(err.Error(), "QEMU") {
		t.Errorf("QEMU failure under --allow-emulation must name QEMU, got %v", err)
	}
}

func TestPrepare_MeasuredCapacityRefuses(t *testing.T) {
	data := []byte("prepare-image-bytes")
	opts := prepareImage(t, data, func(arch string, e, c bool) (string, bool, error) { return "kvm", true, nil })
	opts.VMHome = t.TempDir()
	// The manifest estimate passes preflight's own check only when the
	// disk is roomy; starve the post-admission measured check instead by
	// reporting just under image+headroom after admission. Both stages
	// share the probe, so report roomy-then-tight.
	calls := 0
	opts.PreflightProbes.FreeBytes = func(path string) (uint64, error) {
		calls++
		if calls == 1 {
			return 100 * 1024 * 1024 * 1024, nil
		}
		return 1024, nil
	}
	if _, _, err := Prepare(opts); err == nil || !strings.Contains(err.Error(), "converted guest copy") {
		t.Errorf("tight post-admission disk must fail naming conversion, got %v", err)
	}
}

func TestPrepare_HugeVirtualRefuses(t *testing.T) {
	data := []byte("tiny-but-dense!")
	opts := prepareImage(t, data, func(arch string, e, c bool) (string, bool, error) { return "kvm", true, nil })
	opts.VMHome = t.TempDir()
	// A 16-byte admitted file implying a 100 GiB virtual disk, with 10 GiB
	// free: the file-size bound would admit this; the virtual-size bound
	// refuses it. (Preflight's manifest-size lower estimate passes; the
	// post-admission inspect governs.)
	opts.PreflightProbes.TemplateInspect = func(ctx context.Context, arch, template string) (uint64, error) {
		return 100 * 1024 * 1024 * 1024, nil
	}
	opts.PreflightProbes.FreeBytes = func(path string) (uint64, error) { return 10 * 1024 * 1024 * 1024, nil }
	if _, _, err := Prepare(opts); err == nil || !strings.Contains(err.Error(), "converted guest copy") {
		t.Errorf("huge virtual disk must fail naming conversion, got %v", err)
	}
}
