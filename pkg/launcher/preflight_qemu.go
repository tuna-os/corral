package launcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/tuna-os/corral/pkg/qemu"
)

// defaultTemplateInspect reports an admitted image's virtual size via
// qemu-img info: the conversion-demand bound Prepare's post-admission
// capacity check enforces.
func defaultTemplateInspect(ctx context.Context, guestArch, template string) (uint64, error) {
	qemuImg, err := qemu.QemuImgForArch(guestArch)
	if err != nil {
		return 0, err
	}
	info, err := qemu.InspectTemplate(ctx, qemuImg, template)
	if err != nil {
		return 0, err
	}
	return info.VirtualSize, nil
}

func checkEmulatorPresent(guestArch string) error {
	emulator := "qemu-system-x86_64"
	if guestArch == "arm64" {
		emulator = "qemu-system-aarch64"
	}
	if _, err := exec.LookPath(emulator); err == nil {
		if _, err := exec.LookPath("qemu-img"); err == nil {
			return nil
		}
	}
	for _, base := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/home/linuxbrew/.linuxbrew/bin"} {
		if _, err := os.Stat(filepath.Join(base, emulator)); err == nil {
			if _, err := os.Stat(filepath.Join(base, "qemu-img")); err == nil {
				return nil
			}
		}
	}
	return fmt.Errorf("QEMU for %s not found (%s + qemu-img missing from PATH and Homebrew/system paths) — install: brew install qemu", guestArch, emulator)
}

func checkFirmwarePresent(guestArch string) error {
	return fmt.Errorf("UEFI firmware for %s guests not found on host (install edk2-ovmf or ovmf)", guestArch)
}

// Default probes wire preflight to the real qemu package. Split from
// preflight.go so the pure contract stays readable without the adapter.

func defaultQEMUProbe(guestArch string) error {
	arch, err := qemu.ParseGuestArch(guestArch)
	if err != nil {
		return err
	}
	// findQEMUForArch is unexported; Available covers x86 and the spec
	// resolver proves the rest at create time. For ARM we attempt a
	// resolution that fails fast when the emulator is missing — the error
	// names the prerequisite.
	if arch == qemu.GuestARM64 {
		// Resolution does not check the binary; do a PATH-level check here
		// so preflight fails before any disk write when ARM QEMU is absent.
		// The create path re-checks authoritatively.
		if err := checkEmulatorPresent(string(arch)); err != nil {
			return err
		}
		return nil
	}
	if !qemu.Available() {
		return checkEmulatorPresent(string(arch))
	}
	return nil
}

func defaultFirmwareProbe(guestArch, firmware string) error {
	if firmware == "bios" {
		return nil
	}
	if !qemu.UEFIAvailableForArch(guestArch) {
		return checkFirmwarePresent(guestArch)
	}
	return nil
}

func defaultAccelProbe(guestArch string, allowEmulation, allowCrossArch bool) (string, bool, error) {
	arch, err := qemu.ParseGuestArch(guestArch)
	if err != nil {
		return "", false, err
	}
	spec, err := qemu.ResolveGuestSpec(arch, qemu.ResolveOpts{
		AllowEmulation: allowEmulation,
		AllowCrossArch: allowCrossArch,
	})
	if err != nil {
		return "", false, err
	}
	return spec.Accel, spec.Native, nil
}
