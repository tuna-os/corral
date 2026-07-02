package kubevirt

import (
	"encoding/json"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

func newOptionsFakeClient() (*Client, *shell.Fake) {
	c, r := newFakeClient()
	SetApplyRunner(r)
	r.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	return c, r
}

const optionsVMJSON = `{
	"metadata": {"name": "testvm", "namespace": "tailvm"},
	"spec": {
		"running": true,
		"template": {
			"spec": {
				"domain": {
					"firmware": {"bootloader": {"bios": {}}},
					"devices": {
						"disks": [{"name": "rootdisk"}, {"name": "cdrom0"}],
						"interfaces": [{"name": "default"}]
					}
				}
			}
		}
	}
}`

func appliedManifest(t *testing.T, r *shell.Fake) map[string]any {
	t.Helper()
	for _, c := range r.Calls() {
		if c.Name == "kubectl" && len(c.Args) > 0 && c.Args[0] == "apply" {
			var m map[string]any
			if err := json.Unmarshal([]byte(c.Stdin), &m); err != nil {
				t.Fatalf("applied manifest is not valid JSON: %v", err)
			}
			return m
		}
	}
	t.Fatal("no kubectl apply call recorded")
	return nil
}

func TestSetVMOptions_RunStrategy_ReplacesRunning(t *testing.T) {
	c, r := newOptionsFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vm", "testvm", "-n", "tailvm", "-o", "json"}, optionsVMJSON, nil)

	rs := "Manual"
	if err := c.SetVMOptions("testvm", VMOptions{RunStrategy: &rs}); err != nil {
		t.Fatalf("SetVMOptions: %v", err)
	}
	spec := appliedManifest(t, r)["spec"].(map[string]any)
	if spec["runStrategy"] != "Manual" {
		t.Errorf("runStrategy = %v, want Manual", spec["runStrategy"])
	}
	if _, ok := spec["running"]; ok {
		t.Error("expected the legacy `running` field to be removed when runStrategy is set")
	}
}

func TestSetVMOptions_Firmware_UEFI(t *testing.T) {
	c, r := newOptionsFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vm", "testvm", "-n", "tailvm", "-o", "json"}, optionsVMJSON, nil)

	fw := "uefi"
	if err := c.SetVMOptions("testvm", VMOptions{Firmware: &fw}); err != nil {
		t.Fatalf("SetVMOptions: %v", err)
	}
	m := appliedManifest(t, r)
	domain := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["domain"].(map[string]any)
	bootloader := domain["firmware"].(map[string]any)["bootloader"].(map[string]any)
	if _, ok := bootloader["efi"]; !ok {
		t.Errorf("bootloader = %v, want an efi entry", bootloader)
	}
}

func TestSetVMOptions_MachineType(t *testing.T) {
	c, r := newOptionsFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vm", "testvm", "-n", "tailvm", "-o", "json"}, optionsVMJSON, nil)

	mt := "q35"
	if err := c.SetVMOptions("testvm", VMOptions{MachineType: &mt}); err != nil {
		t.Fatalf("SetVMOptions: %v", err)
	}
	m := appliedManifest(t, r)
	domain := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["domain"].(map[string]any)
	if domain["machine"].(map[string]any)["type"] != "q35" {
		t.Errorf("machine.type = %v, want q35", domain["machine"])
	}
}

func TestSetVMOptions_BootOrder_PreservesOtherDevices(t *testing.T) {
	c, r := newOptionsFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vm", "testvm", "-n", "tailvm", "-o", "json"}, optionsVMJSON, nil)

	if err := c.SetVMOptions("testvm", VMOptions{BootOrder: map[string]int{"rootdisk": 1}}); err != nil {
		t.Fatalf("SetVMOptions: %v", err)
	}
	m := appliedManifest(t, r)
	domain := m["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["domain"].(map[string]any)
	disks := domain["devices"].(map[string]any)["disks"].([]any)
	if len(disks) != 2 {
		t.Fatalf("expected both disks preserved, got %d", len(disks))
	}
	root := disks[0].(map[string]any)
	if root["name"] != "rootdisk" || root["bootOrder"] != float64(1) {
		t.Errorf("rootdisk = %v, want bootOrder 1", root)
	}
	cdrom := disks[1].(map[string]any)
	if cdrom["name"] != "cdrom0" {
		t.Errorf("cdrom0 disk was dropped: %v", disks)
	}
	if _, ok := cdrom["bootOrder"]; ok {
		t.Error("cdrom0 should be untouched (no bootOrder requested for it)")
	}
	ifaces := domain["devices"].(map[string]any)["interfaces"].([]any)
	if len(ifaces) != 1 {
		t.Errorf("interfaces should be preserved untouched, got %v", ifaces)
	}
}

func TestGetVMManifest(t *testing.T) {
	c, r := newOptionsFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vm", "testvm", "-n", "tailvm", "-o", "json"}, optionsVMJSON, nil)

	m, err := c.GetVMManifest("testvm")
	if err != nil {
		t.Fatalf("GetVMManifest: %v", err)
	}
	if m["metadata"].(map[string]any)["name"] != "testvm" {
		t.Errorf("manifest = %v", m)
	}
}
