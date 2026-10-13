package launcher

import (
	"fmt"
	"strings"
	"testing"
)

func preflightImage() ManifestImage {
	return testImage()
}

func TestPreflight_OK(t *testing.T) {
	im := preflightImage()
	res, err := Preflight(t.TempDir(), im, PreflightOpts{
		HostOS:        "linux",
		HostArch:      "amd64",
		QEMUProbe:     func(arch string) error { return nil },
		FirmwareProbe: func(arch, fw string) error { return nil },
		AccelProbe:    func(arch string, e, c bool) (string, bool, error) { return "kvm", true, nil },
		FreeBytes:     func(path string) (uint64, error) { return 100 * 1024 * 1024 * 1024, nil },
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if res.Accel != "kvm" || !res.Native {
		t.Errorf("result wrong: %+v", res)
	}
}

func TestPreflight_FailuresNamePrerequisite(t *testing.T) {
	im := preflightImage()
	okFree := func(path string) (uint64, error) { return 100 * 1024 * 1024 * 1024, nil }
	okQEMU := func(arch string) error { return nil }
	okFW := func(arch, fw string) error { return nil }
	okAccel := func(arch string, e, c bool) (string, bool, error) { return "kvm", true, nil }

	if _, err := Preflight(t.TempDir(), im, PreflightOpts{
		HostOS: "windows", HostArch: "amd64",
		QEMUProbe: okQEMU, FirmwareProbe: okFW, AccelProbe: okAccel, FreeBytes: okFree,
	}); err == nil || !strings.Contains(err.Error(), "Linux/macOS") {
		t.Errorf("bad OS: %v", err)
	}
	if _, err := Preflight(t.TempDir(), im, PreflightOpts{
		HostOS: "linux", HostArch: "amd64",
		QEMUProbe:     func(arch string) error { return fmt.Errorf("QEMU missing") },
		FirmwareProbe: okFW, AccelProbe: okAccel, FreeBytes: okFree,
	}); err == nil || !strings.Contains(err.Error(), "QEMU") {
		t.Errorf("missing QEMU: %v", err)
	}
	if _, err := Preflight(t.TempDir(), im, PreflightOpts{
		HostOS: "linux", HostArch: "amd64",
		QEMUProbe:     okQEMU,
		FirmwareProbe: func(arch, fw string) error { return fmt.Errorf("firmware missing") },
		AccelProbe:    okAccel, FreeBytes: okFree,
	}); err == nil || !strings.Contains(err.Error(), "firmware") {
		t.Errorf("missing firmware: %v", err)
	}
	if _, err := Preflight(t.TempDir(), im, PreflightOpts{
		HostOS: "linux", HostArch: "amd64",
		QEMUProbe: okQEMU, FirmwareProbe: okFW,
		AccelProbe: func(arch string, e, c bool) (string, bool, error) {
			return "", false, fmt.Errorf("native kvm unavailable")
		},
		FreeBytes: okFree,
	}); err == nil || !strings.Contains(err.Error(), "native") {
		t.Errorf("no native accel: %v", err)
	}
	if _, err := Preflight(t.TempDir(), im, PreflightOpts{
		HostOS: "linux", HostArch: "amd64",
		QEMUProbe: okQEMU, FirmwareProbe: okFW, AccelProbe: okAccel,
		FreeBytes: func(path string) (uint64, error) { return 1024, nil },
	}); err == nil || !strings.Contains(err.Error(), "free") {
		t.Errorf("low disk: %v", err)
	}
	bad := im
	bad.SHA256 = "short"
	if _, err := Preflight(t.TempDir(), bad, PreflightOpts{}); err == nil {
		t.Error("invalid manifest must fail preflight")
	}
}

func TestPreflight_PassesEmulationPolicyThrough(t *testing.T) {
	im := preflightImage()
	var gotEmu, gotCross bool
	probes := func() PreflightOpts {
		return PreflightOpts{
			HostOS:        "linux",
			HostArch:      "amd64",
			QEMUProbe:     func(arch string) error { return nil },
			FirmwareProbe: func(arch, fw string) error { return nil },
			AccelProbe: func(arch string, e, c bool) (string, bool, error) {
				gotEmu, gotCross = e, c
				return "tcg", false, nil
			},
			FreeBytes: func(path string) (uint64, error) { return 100 * 1024 * 1024 * 1024, nil },
		}
	}
	opts := probes()
	opts.AllowEmulation = true
	if _, err := Preflight(t.TempDir(), im, opts); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !gotEmu || gotCross {
		t.Errorf("policy (emu) reached probe as emu=%v cross=%v", gotEmu, gotCross)
	}
	opts = probes()
	opts.AllowCrossArch = true
	if _, err := Preflight(t.TempDir(), im, opts); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if gotEmu || !gotCross {
		t.Errorf("policy (cross) reached probe as emu=%v cross=%v", gotEmu, gotCross)
	}
	// Default is strict: neither flag reaches the probe.
	opts = probes()
	if _, err := Preflight(t.TempDir(), im, opts); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if gotEmu || gotCross {
		t.Errorf("default policy must be strict, reached probe as emu=%v cross=%v", gotEmu, gotCross)
	}
}

func TestPreflight_RefusesARMBIOS(t *testing.T) {
	im := preflightImage()
	im.Arch = "arm64"
	im.Firmware = "bios"
	if _, err := Preflight(t.TempDir(), im, PreflightOpts{
		HostOS:   "linux",
		HostArch: "arm64",
		// Even willing probes cannot admit a combination with no boot
		// contract.
		QEMUProbe:     func(arch string) error { return nil },
		FirmwareProbe: func(arch, fw string) error { return nil },
		AccelProbe:    func(arch string, e, c bool) (string, bool, error) { return "kvm", true, nil },
		FreeBytes:     func(path string) (uint64, error) { return 100 * 1024 * 1024 * 1024, nil },
	}); err == nil || !strings.Contains(err.Error(), "UEFI") {
		t.Errorf("arm64+bios must be refused, got %v", err)
	}
}

func TestPreflight_CapacityCoversImageAndConversion(t *testing.T) {
	im := preflightImage() // 16 bytes: need is ~2 GiB headroom + 2x image
	probes := PreflightOpts{
		HostOS:        "linux",
		HostArch:      "amd64",
		QEMUProbe:     func(arch string) error { return nil },
		FirmwareProbe: func(arch, fw string) error { return nil },
		AccelProbe:    func(arch string, e, c bool) (string, bool, error) { return "kvm", true, nil },
	}
	probes.FreeBytes = func(path string) (uint64, error) { return 1024, nil }
	if _, err := Preflight(t.TempDir(), im, probes); err == nil || !strings.Contains(err.Error(), "converted guest copy") {
		t.Errorf("tiny disk must fail naming the conversion copy, got %v", err)
	}
	probes.FreeBytes = func(path string) (uint64, error) { return 100 * 1024 * 1024 * 1024, nil }
	if _, err := Preflight(t.TempDir(), im, probes); err != nil {
		t.Errorf("roomy disk must pass: %v", err)
	}
}

func TestPreflight_VirtualSizeBoundsEstimate(t *testing.T) {
	probes := PreflightOpts{
		HostOS:        "linux",
		HostArch:      "amd64",
		QEMUProbe:     func(arch string) error { return nil },
		FirmwareProbe: func(arch, fw string) error { return nil },
		AccelProbe:    func(arch string, e, c bool) (string, bool, error) { return "kvm", true, nil },
		FreeBytes:     func(path string) (uint64, error) { return 10 * 1024 * 1024 * 1024, nil },
	}
	// A stated 50 GiB virtual size refuses on a 10 GiB disk even though
	// the 16-byte image would fit a hundred times over.
	im := preflightImage()
	im.VirtualSize = 50 * 1024 * 1024 * 1024
	if _, err := Preflight(t.TempDir(), im, probes); err == nil || !strings.Contains(err.Error(), "converted guest copy") {
		t.Errorf("stated virtual size must bound the estimate, got %v", err)
	}
	// Unstated falls back to the image-size lower estimate (which fits),
	// with the authoritative post-admission checks to follow.
	im.VirtualSize = 0
	if _, err := Preflight(t.TempDir(), im, probes); err != nil {
		t.Errorf("unstated virtual size must use the lower estimate: %v", err)
	}
}
