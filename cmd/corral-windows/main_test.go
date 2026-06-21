package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/shell"
)

func TestGenerateWindowsVM_Shape(t *testing.T) {
	vm := generateWindowsVM("win11", "tailvm", "8Gi", 4)
	b, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("manifest does not marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"kind":"VirtualMachine"`,
		`"name":"win11"`,
		`"type":"q35"`,
		`"efi":{"secureBoot":false}`,
		`"tpm":{}`,
		`"hyperv"`,
		`"model":"e1000e"`,
		`"claimName":"win11-disk"`,
		`"claimName":"win11-iso"`,
		VirtioWinImage,
		`"cores":4`,
		`"memory":"8Gi"`,
		`"corral.dev/os":"windows"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("windows VM manifest missing %q", want)
		}
	}
}

func TestGenerateWindowsVM_BootOrder(t *testing.T) {
	vm := generateWindowsVM("win11", "tailvm", "8Gi", 4)
	spec := vm["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	disks := spec["domain"].(map[string]any)["devices"].(map[string]any)["disks"].([]map[string]any)

	order := map[string]any{}
	for _, d := range disks {
		order[d["name"].(string)] = d["bootOrder"]
	}
	// Disk first so an installed system boots straight from disk; the empty
	// disk falls through to the installer ISO on first boot.
	if order["rootdisk"] != 1 || order["windows-iso"] != 2 {
		t.Errorf("boot order = %v, want rootdisk=1 windows-iso=2", order)
	}
	if _, hasOrder := disks[2]["bootOrder"]; hasOrder {
		t.Error("the driver CD-ROM must not be bootable")
	}
}

func TestCreateWindowsVM_AppliesAllManifests(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
	})
	t.Setenv("HOME", t.TempDir()) // registry write goes to a scratch HOME

	if err := createWindowsVM("win11", "tailvm", "https://example.com/win11.iso",
		"64Gi", "8Gi", 4, false); err != nil {
		t.Fatalf("createWindowsVM: %v", err)
	}
	applies := 0
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
		}
	}
	if applies != 3 { // ISO DataVolume + boot PVC + VM
		t.Errorf("applied %d manifests, want 3", applies)
	}
}

func TestCreateWindowsVM_WithRDP(t *testing.T) {
	fake := shell.NewFake()
	fake.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)
	fake.AddResponseKV("kubectl", []string{"get", "sc", "-o", "json"}, `{"items":[]}`, nil)
	kubevirt.SetApplyRunner(fake)
	kubevirt.SetPackageRunner(fake)
	t.Cleanup(func() {
		kubevirt.SetApplyRunner(shell.Real{})
		kubevirt.SetPackageRunner(shell.Real{})
	})
	t.Setenv("HOME", t.TempDir())

	// ApplyProxy only exposes the VM when the Tailscale operator is present
	// (it probes for the `tailscale` IngressClass) — pretend it is here.
	fake.AddResponseKV("kubectl", []string{"get", "ingressclass", "tailscale"}, "tailscale", nil)

	if err := createWindowsVM("win11", "tailvm", "https://example.com/win11.iso",
		"64Gi", "8Gi", 4, true); err != nil {
		t.Fatalf("createWindowsVM --rdp: %v", err)
	}
	// ISO DV + boot PVC + VM + proxy RBAC + proxy Service + proxy Deployment
	applies, sawRDPPort := 0, false
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "apply" {
			applies++
			if strings.Contains(c.Stdin, "3389") {
				sawRDPPort = true
			}
		}
	}
	if applies < 6 {
		t.Errorf("applied %d manifests, want >= 6 (VM trio + proxy trio)", applies)
	}
	if !sawRDPPort {
		t.Error("no applied manifest exposes port 3389")
	}
}

func TestAttachDrivers(t *testing.T) {
	fake := shell.NewFake()
	fake.AddPrefixResponse("kubectl patch vm win11 -n tailvm --type json -p", "patched", nil)
	runner = fake
	t.Cleanup(func() { runner = shell.Real{} })

	if err := attachDrivers("win11", "tailvm"); err != nil {
		t.Fatalf("attachDrivers: %v", err)
	}
	patch := strings.Join(fake.Calls()[0].Args, " ")
	if !strings.Contains(patch, VirtioWinImage) || !strings.Contains(patch, `"cdrom"`) {
		t.Errorf("patch missing driver CD-ROM: %s", patch)
	}
}

func TestAttachDrivers_VMNotFound(t *testing.T) {
	fake := shell.NewFake() // unregistered → patch fails
	runner = fake
	t.Cleanup(func() { runner = shell.Real{} })

	if err := attachDrivers("ghost", "tailvm"); err == nil {
		t.Fatal("attachDrivers should fail when the VM does not exist")
	}
}
