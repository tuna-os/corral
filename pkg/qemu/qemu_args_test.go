package qemu

import (
	"strings"
	"testing"
)

// String-level qualification only: these tests prove the generator emits the
// device set each board needs. Whether ARM graphics actually render and USB
// input actually works needs a real boot/frame/input CI control — no string
// test can qualify that.

func TestQemuArgs_VirtHasExplicitGPUAndInput(t *testing.T) {
	args := strings.Join(qemuArgs(generateUnitOpts{
		Name: "armvm", Mem: "4G", CPU: 2, DiskPath: "/x/disk.qcow2",
		TailscaleIP: Loopback, VncDisplay: 1, SSHPort: 2201,
		Machine: "virt", Accel: "kvm", CPUModel: "host",
	}), " ")
	for _, want := range []string{
		"-device virtio-gpu-pci",
		"-device qemu-xhci",
		"-device usb-kbd",
		"-device usb-mouse",
		"-machine virt,accel=kvm",
		"-cpu host",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("virt args missing %q in %q", want, args)
		}
	}
	if strings.Contains(args, "-vga") {
		t.Errorf("virt must not use -vga (the board consumes no VGA device): %q", args)
	}
}

func TestQemuArgs_Q35KeepsVGA(t *testing.T) {
	args := strings.Join(qemuArgs(generateUnitOpts{
		Name: "x86vm", Mem: "4G", CPU: 2, DiskPath: "/x/disk.qcow2",
		TailscaleIP: Loopback, VncDisplay: 1, SSHPort: 2201,
		Machine: "q35", Accel: "kvm", CPUModel: "host",
	}), " ")
	if !strings.Contains(args, "-vga virtio") {
		t.Errorf("q35 must keep -vga virtio: %q", args)
	}
	for _, absent := range []string{"virtio-gpu-pci", "usb-kbd", "qemu-xhci"} {
		if strings.Contains(args, absent) {
			t.Errorf("q35 must not carry ARM devices (%s): %q", absent, args)
		}
	}
}

func TestQemuArgs_LegacyPathKeepsVGA(t *testing.T) {
	// Empty machine = the legacy x86-only path without a guest spec.
	args := strings.Join(qemuArgs(generateUnitOpts{
		Name: "legacy", Mem: "4G", CPU: 2, DiskPath: "/x/disk.qcow2",
		TailscaleIP: Loopback, VncDisplay: 1, SSHPort: 2201,
	}), " ")
	if !strings.Contains(args, "-vga virtio") {
		t.Errorf("legacy path must keep -vga virtio: %q", args)
	}
}
