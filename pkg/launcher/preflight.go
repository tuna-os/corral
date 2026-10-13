package launcher

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

// PreflightOpts is what the launcher checks before touching disk or QEMU.
// All probes are injectable so tests pin behavior without a hypervisor.
type PreflightOpts struct {
	HostOS   string
	HostArch string
	// AllowEmulation permits a same-arch TCG fallback when the native
	// hypervisor is unavailable. AllowCrossArch permits a guest arch that
	// differs from the host (always TCG). Both flow straight into the
	// accelerator probe: one full preflight under the caller's explicit
	// policy, never a strict probe whose error is reinterpreted after.
	AllowEmulation bool
	AllowCrossArch bool
	// QEMUProbe reports whether the emulator for guestArch + qemu-img are
	// installed. Nil uses the real lookup.
	QEMUProbe func(guestArch string) error
	// FirmwareProbe reports whether firmware for guestArch+firmware is
	// present. Nil uses the real lookup (UEFI only; bios needs no files).
	FirmwareProbe func(guestArch, firmware string) error
	// AccelProbe reports the resolved accelerator ("kvm", "hvf", "tcg") and
	// whether it is native. Nil uses the real resolver.
	AccelProbe func(guestArch string, allowEmulation, allowCrossArch bool) (accel string, native bool, err error)
	// FreeBytes reports free bytes on the filesystem holding path. Nil uses
	// the real statfs.
	FreeBytes func(path string) (uint64, error)
	// TemplateInspect reports an admitted image's virtual size in bytes
	// (guest-visible demand). Nil uses the real qemu-img lookup. Used by
	// Prepare's post-admission capacity check, not by Preflight itself —
	// it rides here because Prepare builds its probes from PreflightOpts.
	TemplateInspect func(ctx context.Context, guestArch, template string) (uint64, error)
}

// PreflightResult is the human-readable outcome: one line per check.
type PreflightResult struct {
	Host     string
	Guest    string
	Accel    string
	Native   bool
	Firmware string
	FreeGB   float64
}

// Preflight verifies the host can genuinely run the manifest's guest: host
// OS/arch matrix, emulator present, firmware present (UEFI), the firmware
// the guest arch can actually boot, native or explicitly allowed emulation,
// and disk admission (free space covers the cached image plus its converted
// guest copy plus a 2 GiB headroom for vars, logs and first writes —
// creation retains both copies). The disk figure is a pre-download LOWER
// estimate from manifest sizes (the stated virtual size when the manifest
// carries one, else the image size): compressed size does not bound
// conversion demand, so the authoritative checks are the post-admission
// virtual-size inspect and creation's recheck before converting. Any
// failure is an error naming the prerequisite — never a warning that boots
// anyway.
func Preflight(vmHome string, im ManifestImage, opts PreflightOpts) (PreflightResult, error) {
	var r PreflightResult
	if err := im.Validate(); err != nil {
		return r, err
	}
	hostOS := opts.HostOS
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	hostArch := opts.HostArch
	if hostArch == "" {
		hostArch = runtime.GOARCH
	}
	if hostOS != "linux" && hostOS != "darwin" {
		return r, fmt.Errorf("unsupported host OS %q (Linux/macOS only)", hostOS)
	}
	if hostArch != "amd64" && hostArch != "arm64" {
		return r, fmt.Errorf("unsupported host arch %q (amd64/arm64 only)", hostArch)
	}
	r.Host = hostOS + "/" + hostArch
	r.Guest = im.Arch
	r.Firmware = im.Firmware

	if im.Arch == "arm64" && im.Firmware == "bios" {
		return r, fmt.Errorf("preflight: arm64 guests require UEFI — the virt board has no BIOS boot contract (a BIOS ARM VM would show a blank screen and nothing else)")
	}

	qemuProbe := opts.QEMUProbe
	if qemuProbe == nil {
		qemuProbe = defaultQEMUProbe
	}
	if err := qemuProbe(im.Arch); err != nil {
		return r, fmt.Errorf("preflight: %w", err)
	}
	fwProbe := opts.FirmwareProbe
	if fwProbe == nil {
		fwProbe = defaultFirmwareProbe
	}
	if err := fwProbe(im.Arch, im.Firmware); err != nil {
		return r, fmt.Errorf("preflight: %w", err)
	}
	accelProbe := opts.AccelProbe
	if accelProbe == nil {
		accelProbe = defaultAccelProbe
	}
	// The caller's explicit emulation policy flows into the single probe —
	// there is no second strict-then-lenient pass to disagree with it.
	accel, native, err := accelProbe(im.Arch, opts.AllowEmulation, opts.AllowCrossArch)
	if err != nil {
		return r, fmt.Errorf("preflight: %w", err)
	}
	r.Accel = accel
	r.Native = native

	freeProbe := opts.FreeBytes
	if freeProbe == nil {
		freeProbe = freeBytesOn
	}
	free, err := freeProbe(vmHome)
	if err != nil {
		return r, fmt.Errorf("preflight: cannot read free disk space: %w", err)
	}
	r.FreeGB = float64(free) / 1e9
	// The retained cache copy costs the image size; the converted guest
	// copy costs the virtual size when stated, else the image size as a
	// lower estimate (authoritative checks follow post-admission).
	converted := uint64(im.Size)
	if im.VirtualSize > int64(converted) {
		converted = uint64(im.VirtualSize)
	}
	need := uint64(im.Size) + converted + 2*1024*1024*1024
	if free < need {
		return r, fmt.Errorf("preflight: need %.1f GB free for the %.1f GB image, its converted guest copy and 2 GB headroom, have %.1f GB",
			float64(need)/1e9, float64(im.Size)/1e9, r.FreeGB)
	}
	return r, nil
}

func freeBytesOn(path string) (uint64, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return 0, err
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
