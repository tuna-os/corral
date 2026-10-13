package backend

// Inspection gaps: five of the nine backends record no per-instance firmware
// fact, so Inspect must return them as unknown — never as "BIOS".
//
// The zero GuestInfo is the unknown representation, and pkg/move downgrades an
// unknown firmware to a warning rather than asserting compatibility. A backend
// that silently reported BIOS=false-as-fact would let a UEFI guest move to a
// BIOS-default target and boot to a blank screen; a backend that errored would
// push every call site into handling a non-failure. Both failure modes are
// pinned here, including the qemu gap the existing table already names.

import (
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

// Backends with no firmware source: no Inspector, and Inspect reports unknown.
func TestInspect_BackendsWithoutFirmwareReportUnknown(t *testing.T) {
	for _, backendName := range []string{"qemu", "virtualbox", "vsphere", "xen", "nutanix"} {
		t.Run(backendName, func(t *testing.T) {
			adapter, err := probe(backendName)
			if err != nil {
				t.Fatalf("probing %s: %v", backendName, err)
			}
			if _, ok := adapter.(Inspector); ok {
				t.Fatalf("%s implements Inspector: move the backend to the known-firmware table", backendName)
			}
			if got := Inspect(types.InstanceRef{Backend: backendName, Name: "web"}); got != (GuestInfo{}) {
				t.Fatalf("Inspect(%s) = %+v, want zero (unknown), not a firmware claim", backendName, got)
			}
		})
	}
}

// The whole registry, classified: when a backend gains a firmware source its
// adapter grows an Inspector and this table — not a silent default — is what
// changes.
func TestInspect_EveryBackendIsClassified(t *testing.T) {
	known := map[string]bool{"kubevirt": true, "libvirt": true, "proxmox": true, "incus": true}
	for _, backendName := range []string{
		"kubevirt", "qemu", "incus", "libvirt", "proxmox", "virtualbox", "vsphere", "xen", "nutanix",
	} {
		adapter, err := probe(backendName)
		if err != nil {
			t.Fatalf("probing %s: %v", backendName, err)
		}
		_, ok := adapter.(Inspector)
		if ok != known[backendName] {
			t.Errorf("%s implements Inspector = %v, want %v", backendName, ok, known[backendName])
		}
		if !known[backendName] {
			if got := Inspect(types.InstanceRef{Backend: backendName, Name: "web"}); got != (GuestInfo{}) {
				t.Errorf("Inspect(%s) = %+v, want unknown", backendName, got)
			}
		}
	}
}

// qemu is the sharpest gap: it is a first-class move source and destination,
// its generated unit carries no firmware line, and UEFI=false must read as
// "nobody recorded this" all the way to preflight's unknown-OS warning —
// never as "this guest is BIOS".
func TestInspect_QemuFirmwareIsUnknownNotBIOS(t *testing.T) {
	got := Inspect(types.InstanceRef{Backend: "qemu", Name: "vm-1"})
	if got.UEFI {
		t.Fatal("qemu reported EFI it cannot know about")
	}
	if got.OSType != "" {
		t.Fatalf("qemu reported OS %q it cannot know about", got.OSType)
	}
	if got != (GuestInfo{}) {
		t.Fatalf("qemu inspection = %+v: any non-zero field reads as a fact downstream", got)
	}
}
