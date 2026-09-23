package qemu

import (
	"fmt"
	"io"
	"os"
	"runtime"
)

type firmwarePair struct {
	code string
	vars string
}

func candidateFirmwares() []firmwarePair {
	if code := os.Getenv("CORRAL_OVMF_CODE"); code != "" {
		vars := os.Getenv("CORRAL_OVMF_VARS")
		return []firmwarePair{{code: code, vars: vars}}
	}

	arch := runtime.GOARCH
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

// FindUEFIFirmware searches known system and Homebrew locations for UEFI code
// and vars template files on the host.
func FindUEFIFirmware() (codePath, varsTemplate string, err error) {
	for _, cand := range candidateFirmwares() {
		if cand.code == "" {
			continue
		}
		if _, err := os.Stat(cand.code); err == nil {
			if cand.vars != "" {
				if _, err := os.Stat(cand.vars); err == nil {
					return cand.code, cand.vars, nil
				}
			} else {
				return cand.code, "", nil
			}
		}
	}
	return "", "", fmt.Errorf("UEFI firmware not found on host (install edk2-ovmf or ovmf)")
}

// UEFIAvailable reports whether UEFI firmware is present on the host.
func UEFIAvailable() bool {
	_, _, err := FindUEFIFirmware()
	return err == nil
}

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
	defer func() { _ = d.Close() }()
	_, err = io.Copy(d, s)
	return err
}
