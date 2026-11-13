package qemu

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

func TestCreateGuestArchitectureRecordsRuntime(t *testing.T) {
	for _, arch := range []types.GuestArch{"", types.ArchAMD64, types.ArchARM64} {
		t.Run("guest="+string(arch), func(t *testing.T) {
			home, units := vmHomeOverride, unitDirOverride
			oldSystemctl := systemctlRun
			SetStateDirs(t.TempDir(), t.TempDir())
			SetSystemctl(func(...string) ([]byte, error) { return nil, nil })
			t.Cleanup(func() {
				SetStateDirs(home, units)
				systemctlRun = oldSystemctl
			})
			bin := t.TempDir()
			for _, name := range []string{"qemu-system-x86_64", "qemu-system-aarch64", "qemu-img"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			code, vars := filepath.Join(t.TempDir(), "code.fd"), filepath.Join(t.TempDir(), "vars.fd")
			for _, path := range []string{code, vars} {
				if err := os.WriteFile(path, []byte("test firmware"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("CORRAL_OVMF_CODE", code)
			t.Setenv("CORRAL_OVMF_VARS", vars)
			vmDir := filepath.Join(VMHome(), "guest")
			if err := os.MkdirAll(vmDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(vmDir, "disk.qcow2"), []byte("prepared disk"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := Create(types.CreateOpts{Name: "guest", Arch: arch, ExistingDisk: true, Force: true, UEFI: true, CPU: 4}); err != nil {
				t.Fatal(err)
			}
			effective := arch
			if effective == "" {
				effective = types.GuestArch(runtime.GOARCH)
			}
			command, err := readLaunchCommand("guest")
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(bin, qemuSystemBinary(effective)); command.Binary != want {
				t.Errorf("recorded emulator = %q, want %q", command.Binary, want)
			}
			data, err := Info("guest")
			if err != nil {
				t.Fatal(err)
			}
			var meta vmMetadata
			if err := json.Unmarshal(data, &meta); err != nil {
				t.Fatal(err)
			}
			kvmAccessible := false
			if device, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err == nil {
				kvmAccessible = device.Close() == nil
			}
			wantAccel := acceleratorFor(runtime.GOOS, runtime.GOARCH, effective, kvmAccessible)
			if meta.Arch != effective || meta.Acceleration != wantAccel || meta.Firmware != "uefi" {
				t.Fatalf("metadata does not describe guest runtime: %+v", meta)
			}
			args := strings.Join(command.Args, " ")
			for _, part := range []string{"accel=" + wantAccel, "readonly=on,file=" + code, "file=" + filepath.Join(vmDir, "vars.fd")} {
				if !strings.Contains(args, part) {
					t.Errorf("recorded runtime %q lacks %q", args, part)
				}
			}
			if string(effective) != runtime.GOARCH && (wantAccel != "tcg" || strings.Contains(args, "-cpu host")) {
				t.Fatalf("cross-architecture runtime uses host acceleration/CPU: %s", args)
			}
			unit, err := os.ReadFile(filepath.Join(systemdUserDir(), "corral-guest.service"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(unit), "ExecStart="+command.Binary+" "+args) {
				t.Fatalf("systemd and direct runtime commands differ: %s", unit)
			}
		})
	}
}

func TestGuestArchitectureDeviceArguments(t *testing.T) {
	for _, arch := range []types.GuestArch{types.ArchAMD64, types.ArchARM64} {
		args := strings.Join(qemuArgs(generateUnitOpts{Arch: arch, Acceleration: "tcg", CPU: 2,
			Mem: "4G", DiskPath: "/disk.qcow2", HasISO: true, ISOPath: "/install.iso"}), " ")
		if arch == types.ArchAMD64 {
			for _, want := range []string{"q35,accel=tcg", "-cpu max", "-vga virtio", "-cdrom /install.iso"} {
				if !strings.Contains(args, want) {
					t.Errorf("amd64 runtime %q lacks %q", args, want)
				}
			}
		} else {
			for _, want := range []string{"virt,accel=tcg", "-cpu max", "-device virtio-gpu-pci", "-device usb-kbd", "-device usb-tablet", "scsi-cd,drive=cdrom,bootindex=1"} {
				if !strings.Contains(args, want) {
					t.Errorf("arm64 runtime %q lacks %q", args, want)
				}
			}
			if strings.Contains(args, "-vga") || strings.Contains(args, "-cdrom") {
				t.Fatalf("arm64 virt runtime has PC-only default devices: %s", args)
			}
		}
	}
}

func TestGuestArchitectureRefusesInvalidBeforeCreatingState(t *testing.T) {
	oldHome := vmHomeOverride
	vmHomeOverride = t.TempDir()
	t.Cleanup(func() { vmHomeOverride = oldHome })
	for _, opts := range []types.CreateOpts{
		{Name: "invalid", Arch: "riscv64", UEFI: true},
		{Name: "invalid", Arch: types.ArchARM64},
	} {
		if err := Create(opts); err == nil {
			t.Fatalf("Create(%+v) succeeded", opts)
		}
		if _, err := os.Stat(filepath.Join(VMHome(), opts.Name)); !os.IsNotExist(err) {
			t.Fatalf("invalid guest left VM state: %v", err)
		}
	}
	if _, _, err := findQEMU("mips"); err == nil {
		t.Fatal("invalid architecture selected an emulator")
	}
	if _, _, err := FindUEFIFirmwareForArch("mips"); err == nil {
		t.Fatal("invalid architecture selected firmware")
	}
}

func TestGuestFirmwareCandidatesDoNotUseHostArchitecture(t *testing.T) {
	t.Setenv("CORRAL_OVMF_CODE", "")
	for _, arch := range []types.GuestArch{types.ArchAMD64, types.ArchARM64} {
		candidates := candidateFirmwares(arch)
		if len(candidates) == 0 {
			t.Fatalf("no firmware candidates for %s", arch)
		}
		appleSilicon := false
		for _, candidate := range candidates {
			lower := strings.ToLower(candidate.code)
			if arch == types.ArchAMD64 && (strings.Contains(lower, "aarch64") || strings.Contains(lower, "aavmf")) {
				t.Errorf("amd64 selected ARM firmware: %s", candidate.code)
			}
			if arch == types.ArchARM64 && (strings.Contains(lower, "x86_64") || strings.Contains(lower, "ovmf")) {
				t.Errorf("arm64 selected x86 firmware: %s", candidate.code)
			}
			if strings.HasPrefix(candidate.code, "/opt/homebrew/") {
				appleSilicon = true
			}
		}
		if !appleSilicon {
			t.Errorf("no Apple Silicon Homebrew firmware candidate for %s", arch)
		}
	}
}

func TestGuestFirmwareOverrideDoesNotFallBack(t *testing.T) {
	t.Setenv("CORRAL_OVMF_CODE", filepath.Join(t.TempDir(), "missing-code.fd"))
	for _, arch := range []types.GuestArch{types.ArchAMD64, types.ArchARM64} {
		if _, _, err := FindUEFIFirmwareForArch(arch); err == nil {
			t.Fatalf("missing explicit %s firmware silently fell back", arch)
		}
	}
}

func TestFindQEMUDoesNotSwitchGuestArchitecture(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"qemu-img", "qemu-system-aarch64"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// A non-executable x86 file must not be accepted just because it exists.
	if err := os.WriteFile(filepath.Join(bin, "qemu-system-x86_64"), []byte("not executable"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	q, _, err := findQEMU(types.ArchAMD64)
	if err == nil && (filepath.Base(q) != "qemu-system-x86_64" || q == filepath.Join(bin, "qemu-system-x86_64")) {
		t.Fatalf("amd64 selected a wrong/non-executable emulator: %s", q)
	}
}
