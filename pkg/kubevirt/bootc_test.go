//go:build bootc

package kubevirt

import (
	"testing"
)

// Tailnet access for bootc VMs comes from the Tailscale operator proxy
// (ApplyProxy), not in-guest tailscale. The final bootc VM boots the installed
// disk via UEFI and carries no cloud-init disk of its own.
func TestGenerateBootcVM_NoInGuestCloudInit(t *testing.T) {
	vm := GenerateBootcVM("testvm", "myns", "testvm-bootc-disk",
		"quay.io/centos-bootc/centos-bootc:stream9", "ssh-ed25519 AAAAKEY u@h", "4G", 2, "")
	if vm == nil {
		t.Fatal("GenerateBootcVM returned nil (bootc plugin not compiled in?)")
	}

	vmiSpec := vm["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	for _, v := range vmiSpec["volumes"].([]map[string]any) {
		if _, ok := v["cloudInitNoCloud"]; ok {
			t.Error("final bootc VM must not carry a cloud-init disk (key baked at build, tailnet via operator proxy)")
		}
	}

	// It must boot from the disk PVC, not a kernelBoot container.
	if _, ok := vmiSpec["domain"].(map[string]any)["firmware"].(map[string]any)["kernelBoot"]; ok {
		t.Error("final bootc VM must boot via UEFI firmware, not kernelBoot")
	}
}
