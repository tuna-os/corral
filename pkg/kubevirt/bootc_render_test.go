//go:build bootc

package kubevirt

import (
	"encoding/json"
	"strings"
	"testing"
)

// The builder VM must render as a valid VirtualMachine that attaches the target
// disk as a block device and whose cloud-init carries the composefs-aware
// install recipe (backend detection, --filesystem, the EROFS initrd fixup, the
// manual composefs key injection, and the console/sshd kargs).
func TestRenderBuilderVM(t *testing.T) {
	vm := generateBuilderVM("e2e-x-bootc-builder", "corral-vms",
		"e2e-x-bootc-disk", "e2e-x-bootc-builder-cloudinit", "quay.io/centos-bootc/centos-bootc:stream9")

	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("builder VM not marshalable: %v", err)
	}
	js := string(data)

	if vm["kind"] != "VirtualMachine" {
		t.Errorf("builder must be a VirtualMachine, got %v", vm["kind"])
	}
	// The target disk is attached by serial so the guest sees virtio-target.
	if !strings.Contains(js, `"serial":"target"`) {
		t.Errorf("builder VM missing target disk serial")
	}
	// cloud-init comes from a Secret (script exceeds the 2 KB inline limit).
	if !strings.Contains(js, `"secretRef"`) || !strings.Contains(js, "e2e-x-bootc-builder-cloudinit") {
		t.Errorf("builder VM must reference the cloud-init secret")
	}

	// The recipe lives in the Secret's userdata.
	sec := generateBuilderSecret("e2e-x-bootc-builder-cloudinit", "corral-vms",
		"ghcr.io/projectbluefin/dakota:testing", "ssh-ed25519 AAAATESTKEY user@host")
	script := sec["stringData"].(map[string]any)["userdata"].(string)
	for _, want := range []string{
		"ghcr.io/projectbluefin/dakota:testing", // image substituted
		"ssh-ed25519 AAAATESTKEY user@host",     // key substituted
		"bootc install to-disk",                 // VM-builder path
		"CORRAL_BACKEND=",                       // backend detection result
		"bootupctl",                             // ostree signal (has bootupd)
		"systemd-bootx64.efi",                   // composefs signal (systemd-boot, no bootupd)
		"--composefs-backend",                   // composefs branch
		"--generic-image",                       // ostree branch (removable bootloader)
		"--filesystem",                          // fs selection (btrfs vs xfs)
		"bootc_composefs-",                      // composefs initrd fixup target
		"state/os/default/var/roothome/.ssh",    // composefs manual key injection
		"systemd.wants=sshd.service",            // sshd karg
		"CORRAL_BUILD_OK",                       // success marker
	} {
		if !strings.Contains(script, want) {
			t.Errorf("builder cloud-init missing %q", want)
		}
	}
}

// The final bootc VM must UEFI-boot the block disk PVC — no kernelBoot.
func TestRenderBootcFinalVM(t *testing.T) {
	vm := generateBootcVM("dak", "corral-vms", "dak-bootc-disk",
		"ghcr.io/projectbluefin/dakota:testing", "ssh-ed25519 AAAAREBUILDKEY u@h", "4G", 2, "")
	data, _ := json.Marshal(vm)
	js := string(data)

	if strings.Contains(js, "kernelBoot") {
		t.Errorf("final VM must not use kernelBoot (disk is self-bootable)")
	}
	if !strings.Contains(js, `"efi"`) {
		t.Errorf("final VM must boot via UEFI firmware")
	}
	if !strings.Contains(js, "dak-bootc-disk") {
		t.Errorf("final VM must boot the disk PVC")
	}
	// The SSH key is recorded on the VM so rebuild/upgrade/switch can re-bake it.
	if !strings.Contains(js, "corral.bootc/ssh-key") || !strings.Contains(js, "AAAAREBUILDKEY") {
		t.Errorf("final VM must record the SSH key annotation for rebuilds")
	}
}
