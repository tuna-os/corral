package qemu

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
)

type firmwarePair struct {
	code string
	vars string
}

func candidateFirmwaresForArch(arch string) []firmwarePair {
	// Arch-specific overrides win: one variable cannot name both arches'
	// firmware, so CORRAL_OVMF_CODE_AMD64 / _ARM64 (and _VARS_) each bind
	// exactly one guest arch.
	suffix := "AMD64"
	if arch == "arm64" {
		suffix = "ARM64"
	}
	if code := os.Getenv("CORRAL_OVMF_CODE_" + suffix); code != "" {
		return []firmwarePair{{code: code, vars: os.Getenv("CORRAL_OVMF_VARS_" + suffix)}}
	}
	if code := os.Getenv("CORRAL_OVMF_CODE"); code != "" && arch == runtime.GOARCH {
		// The generic override names the operator's host firmware. It
		// applies only to same-arch guests — handing host-arch firmware
		// to a cross-arch emulator would silently misboot, so cross-arch
		// guests fall through to their own system list below.
		return []firmwarePair{{code: code, vars: os.Getenv("CORRAL_OVMF_VARS")}}
	}

	var list []firmwarePair

	switch arch {
	case "amd64":
		list = []firmwarePair{
			// Fedora / RHEL / CentOS / Bluefin
			{
				code: "/usr/share/qemu/edk2-x86_64-code.fd",
				vars: "/usr/share/qemu/edk2-i386-vars.fd",
			},
			{
				code: "/usr/share/edk2/ovmf/OVMF_CODE.fd",
				vars: "/usr/share/edk2/ovmf/OVMF_VARS.fd",
			},
			// Ubuntu / Debian
			{
				code: "/usr/share/OVMF/OVMF_CODE_4M.fd",
				vars: "/usr/share/OVMF/OVMF_VARS_4M.fd",
			},
			{
				code: "/usr/share/OVMF/OVMF_CODE.fd",
				vars: "/usr/share/OVMF/OVMF_VARS.fd",
			},
			// Arch Linux
			{
				code: "/usr/share/edk2-ovmf/x64/OVMF_CODE.fd",
				vars: "/usr/share/edk2-ovmf/x64/OVMF_VARS.fd",
			},
			// openSUSE
			{
				code: "/usr/share/qemu/ovmf-x86_64-code.bin",
				vars: "/usr/share/qemu/ovmf-x86_64-vars.bin",
			},
			// Homebrew (Linuxbrew)
			{
				code: "/home/linuxbrew/.linuxbrew/share/qemu/edk2-x86_64-code.fd",
				vars: "/home/linuxbrew/.linuxbrew/share/qemu/edk2-i386-vars.fd",
			},
			// Homebrew (macOS Intel)
			{
				code: "/usr/local/share/qemu/edk2-x86_64-code.fd",
				vars: "/usr/local/share/qemu/edk2-i386-vars.fd",
			},
			// Homebrew (macOS Apple Silicon x86)
			{
				code: "/opt/homebrew/share/qemu/edk2-x86_64-code.fd",
				vars: "/opt/homebrew/share/qemu/edk2-i386-vars.fd",
			},
		}
	case "arm64":
		list = []firmwarePair{
			// Fedora / RHEL
			{
				code: "/usr/share/qemu/edk2-aarch64-code.fd",
				vars: "/usr/share/qemu/edk2-arm-vars.fd",
			},
			// Ubuntu / Debian
			{
				code: "/usr/share/AAVMF/AAVMF_CODE.fd",
				vars: "/usr/share/AAVMF/AAVMF_VARS.fd",
			},
			// Arch Linux
			{
				code: "/usr/share/edk2-arm/aarch64/QEMU_EFI.fd",
				vars: "/usr/share/edk2-arm/aarch64/QEMU_VARS.fd",
			},
			// Homebrew (macOS Apple Silicon)
			{
				code: "/opt/homebrew/share/qemu/edk2-aarch64-code.fd",
				vars: "/opt/homebrew/share/qemu/edk2-arm-vars.fd",
			},
			// Homebrew (Linuxbrew arm64)
			{
				code: "/home/linuxbrew/.linuxbrew/share/qemu/edk2-aarch64-code.fd",
				vars: "/home/linuxbrew/.linuxbrew/share/qemu/edk2-arm-vars.fd",
			},
		}
	}
	return list
}

// firmwareFileOK requires a non-empty regular file. Symlinks to one are
// accepted (distro firmware paths are routinely symlinks); directories,
// devices and empty files are refused.
func firmwareFileOK(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.Mode().IsRegular() && st.Size() > 0
}

// firmwareSHA256 identifies firmware code bytes authoritatively: the hex
// digest of the file's current content. Creation records it alongside the
// code path, and reuse re-hashes and compares — presence on disk alone
// would admit a replacement code.fd as the same definition. The read is
// size-bounded like the firmware copy: anything past the bound is a
// mis-pointed override, not firmware.
func firmwareSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxFirmwareCopy+1))
	if err != nil {
		return "", err
	}
	if n > maxFirmwareCopy {
		return "", fmt.Errorf("%s exceeds %d bytes — not firmware", path, maxFirmwareCopy)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FindUEFIFirmwareForArch searches known system and Homebrew locations for
// UEFI code and vars template files for an explicit guest architecture. The
// binding is by path convention — distro and Homebrew paths encode the arch
// they ship — not by verified firmware content: lookup alone cannot prove
// the bytes are that arch's firmware, and no claim to that effect is made.
// Callers that need a proven image must qualify it themselves.
func FindUEFIFirmwareForArch(guestArch string) (codePath, varsTemplate string, err error) {
	arch := guestArch
	if arch == "" {
		arch = runtime.GOARCH
	}
	for _, cand := range candidateFirmwaresForArch(arch) {
		if cand.code == "" {
			continue
		}
		if firmwareFileOK(cand.code) {
			if cand.vars != "" {
				if firmwareFileOK(cand.vars) {
					return cand.code, cand.vars, nil
				}
			} else {
				return cand.code, "", nil
			}
		}
	}
	if arch != runtime.GOARCH {
		suffix := "AMD64"
		if arch == "arm64" {
			suffix = "ARM64"
		}
		return "", "", fmt.Errorf("UEFI firmware for %s guests not found on host (install edk2-ovmf or ovmf, or set CORRAL_OVMF_CODE_%s/CORRAL_OVMF_VARS_%s to that arch's firmware)", arch, suffix, suffix)
	}
	return "", "", fmt.Errorf("UEFI firmware not found on host (install edk2-ovmf or ovmf)")
}

// FindUEFIFirmware searches known system and Homebrew locations for UEFI code
// and vars template files on the host. Legacy wrapper: it binds firmware to
// the host arch, which is only correct for same-arch guests. New code passes
// an explicit guest arch to FindUEFIFirmwareForArch.
func FindUEFIFirmware() (codePath, varsTemplate string, err error) {
	return FindUEFIFirmwareForArch(runtime.GOARCH)
}

// UEFIAvailable reports whether UEFI firmware is present on the host.
func UEFIAvailable() bool {
	_, _, err := FindUEFIFirmware()
	return err == nil
}

// UEFIAvailableForArch reports whether UEFI firmware for an explicit guest
// arch is present on the host.
func UEFIAvailableForArch(guestArch string) bool {
	_, _, err := FindUEFIFirmwareForArch(guestArch)
	return err == nil
}

// maxFirmwareCopy bounds a firmware-template copy: firmware fds are KBs to a
// few MB, so anything larger is a mis-pointed override, not firmware. A
// variable so tests can trip the bound without a 256 MB fixture.
var maxFirmwareCopy int64 = 256 * 1024 * 1024

func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(d, io.LimitReader(s, maxFirmwareCopy+1))
	// The admission is only as proven as this close: a failed close can
	// mean buffered bytes never reached the disk.
	cerr := d.Close()
	if err != nil {
		_ = os.Remove(dst)
		return err
	}
	if n > maxFirmwareCopy {
		_ = os.Remove(dst)
		return fmt.Errorf("copying %s: file exceeds %d bytes — not firmware", src, maxFirmwareCopy)
	}
	if cerr != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("copying %s: %w", src, cerr)
	}
	return nil
}
