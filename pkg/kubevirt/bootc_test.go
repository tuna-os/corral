//go:build bootc

package kubevirt

import (
	"strings"
	"testing"
)

func TestGenerateBootcVM_CloudInitTailscale(t *testing.T) {
	vm := GenerateBootcVM("testvm", "myns", "test-pvc",
		"quay.io/centos-bootc/centos-bootc:stream9",
		"abc123", "6.1.0-test", "4G", 2, "",
		"tskey-auth-kTest123CNTRL")

	if vm == nil {
		t.Fatal("GenerateBootcVM returned nil (bootc plugin not compiled in?)")
	}

	// Navigate to volumes through the public manifest structure.
	spec := vm["spec"].(map[string]any)
	tmpl := spec["template"].(map[string]any)
	vmiSpec := tmpl["spec"].(map[string]any)
	volumes := vmiSpec["volumes"].([]map[string]any)

	var ci map[string]any
	for _, v := range volumes {
		if v["name"] == "cloudinitdisk" {
			ci = v
			break
		}
	}
	if ci == nil {
		t.Fatal("no cloudinitdisk volume in bootc VM manifest")
	}

	nocloud := ci["cloudInitNoCloud"].(map[string]any)
	userData := nocloud["userData"].(string)

	if !strings.Contains(userData, "tailscale") {
		t.Error("cloud-init userData does not contain tailscale install command")
	}
	if !strings.Contains(userData, "tskey-auth-kTest123CNTRL") {
		t.Error("cloud-init userData does not contain the tailscale auth key")
	}
	if !strings.Contains(userData, "--hostname=testvm") {
		t.Error("cloud-init userData does not contain --hostname=testvm")
	}
}

func TestGenerateBootcVM_NoCloudInitWhenNoKey(t *testing.T) {
	vm := GenerateBootcVM("testvm", "myns", "test-pvc",
		"quay.io/centos-bootc/centos-bootc:stream9",
		"abc123", "6.1.0-test", "4G", 2, "",
		"") // no tailscale key

	if vm == nil {
		t.Fatal("GenerateBootcVM returned nil (bootc plugin not compiled in?)")
	}

	vmiSpec := vm["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	volumes := vmiSpec["volumes"].([]map[string]any)

	for _, v := range volumes {
		if v["name"] == "cloudinitdisk" {
			t.Error("cloudinitdisk should not be present when no tailscale key is provided")
		}
	}
}
