package cmd

import (
	"strings"
	"testing"
)

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

func TestExecuteCreateRefusesInvalidGuestOptions(t *testing.T) {
	createScratch(t)
	oldArch, oldFirmware := createArch, createFirmware
	t.Cleanup(func() { createArch, createFirmware = oldArch, oldFirmware })
	for _, tc := range []struct{ arch, firmware string }{{"mips", "uefi"}, {"amd64", "invalid"}} {
		createArch, createFirmware = tc.arch, tc.firmware
		if err := executeCreate("guest", false); err == nil {
			t.Fatalf("QEMU create accepted arch=%q firmware=%q", tc.arch, tc.firmware)
		}
		assertNoLocalVM(t, "guest")
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
