package cmd

import (
	"strings"
	"testing"
)

func TestCreateGuestOptionsValidation(t *testing.T) {
	oldArch, oldFirmware := createArch, createFirmware
	t.Cleanup(func() { createArch, createFirmware = oldArch, oldFirmware })
	for _, tc := range []struct {
		backend, arch, firmware, wantError string
	}{
		{"qemu", "", "", ""},
		{"qemu", "amd64", "uefi", ""},
		{"qemu", "arm64", "uefi", ""},
		{"qemu", "amd64", "bios", ""},
		{"qemu", "x86_64", "uefi", "unsupported guest architecture"},
		{"qemu", "riscv64", "uefi", "unsupported guest architecture"},
		{"qemu", "amd64", "efi", "unsupported firmware"},
		{"kubevirt", "amd64", "uefi", "not supported on backend kubevirt"},
		{"incus", "arm64", "", "not supported on backend incus"},
		{"libvirt", "amd64", "", "not supported on backend libvirt"},
		{"proxmox", "amd64", "", "not supported on backend proxmox"},
		{"bootc", "amd64", "", "not supported on backend bootc"},
		{"kubevirt", "", "uefi", ""},
	} {
		t.Run(tc.backend+"/"+tc.arch+"/"+tc.firmware, func(t *testing.T) {
			createArch, createFirmware = tc.arch, tc.firmware
			err := validateCreateGuestOptions(tc.backend)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("validation error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestExecuteCreateRefusesUnsupportedArchitectureBeforeDispatch(t *testing.T) {
	createScratch(t)
	oldArch, oldFirmware, oldBootc := createArch, createFirmware, createBootc
	oldKubevirt, oldIncus := createKubevirt, createIncus
	t.Cleanup(func() {
		createArch, createFirmware, createBootc = oldArch, oldFirmware, oldBootc
		createKubevirt, createIncus = oldKubevirt, oldIncus
	})
	createArch, createFirmware, createBootc = "amd64", "uefi", ""
	createKubevirt, createIncus = false, false
	for _, selected := range []string{"kubevirt", "incus", "libvirt", "proxmox", "unknown-backend"} {
		rootBackend = selected
		err := executeCreate("guest", false)
		if err == nil || !strings.Contains(err.Error(), "--arch is not supported on backend "+selected) {
			t.Fatalf("backend %s dispatched instead of refusing --arch: %v", selected, err)
		}
		assertNoLocalVM(t, "guest")
	}
}

func TestRunQemuCreateRefusesInvalidGuestOptions(t *testing.T) {
	createScratch(t)
	oldArch, oldFirmware := createArch, createFirmware
	t.Cleanup(func() { createArch, createFirmware = oldArch, oldFirmware })
	for _, tc := range []struct{ arch, firmware string }{{"mips", "uefi"}, {"amd64", "invalid"}} {
		createArch, createFirmware = tc.arch, tc.firmware
		if err := runQemuCreate("guest"); err == nil {
			t.Fatalf("QEMU create accepted arch=%q firmware=%q", tc.arch, tc.firmware)
		}
		assertNoLocalVM(t, "guest")
	}
}

func TestCreateLoopbackOptionsValidation(t *testing.T) {
	oldArch, oldFirmware, oldLoopback := createArch, createFirmware, createLoopback
	t.Cleanup(func() {
		createArch, createFirmware, createLoopback = oldArch, oldFirmware, oldLoopback
	})
	createArch, createFirmware, createLoopback = "", "", true
	for _, backend := range []string{"qemu", "kubevirt", "incus", "libvirt", "proxmox", "bootc", "unknown-backend"} {
		t.Run(backend, func(t *testing.T) {
			err := validateCreateGuestOptions(backend)
			if backend == "qemu" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "--loopback is supported only on local QEMU create") {
				t.Fatalf("backend %s accepted --loopback: %v", backend, err)
			}
		})
	}
}

func TestExecuteCreateRefusesUnsupportedLoopbackBeforeDispatch(t *testing.T) {
	createScratch(t)
	oldArch, oldFirmware, oldLoopback := createArch, createFirmware, createLoopback
	oldBootc, oldKubevirt, oldIncus := createBootc, createKubevirt, createIncus
	t.Cleanup(func() {
		createArch, createFirmware, createLoopback = oldArch, oldFirmware, oldLoopback
		createBootc, createKubevirt, createIncus = oldBootc, oldKubevirt, oldIncus
	})
	createArch, createFirmware, createLoopback = "", "", true
	createBootc, createKubevirt, createIncus = "", false, false
	for _, selected := range []string{"kubevirt", "incus", "libvirt", "proxmox", "unknown-backend"} {
		rootBackend = selected
		err := executeCreate("guest", false)
		if err == nil || !strings.Contains(err.Error(), "--loopback is supported only on local QEMU create") {
			t.Fatalf("backend %s dispatched instead of refusing --loopback: %v", selected, err)
		}
		assertNoLocalVM(t, "guest")
	}
	rootBackend, createBootc = "qemu", "quay.io/example/guest:latest"
	if err := executeCreate("guest", false); err == nil || !strings.Contains(err.Error(), "--loopback is supported only on local QEMU create") {
		t.Fatalf("bootc dispatched instead of refusing --loopback: %v", err)
	}
	assertNoLocalVM(t, "guest")
}
