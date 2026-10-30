package kubevirt

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

const detailsVM = `{
  "metadata": {"name": "perf-a", "namespace": "corral-vms",
    "labels": {"corral": "perf-a", "corral.dev/tag.pair": "run42"},
    "annotations": {"corral.bootc/image": "ghcr.io/tuna-os/marlin:latest"}},
  "spec": {"template": {"spec": {
    "nodeSelector": {"kubernetes.io/hostname": "n1"},
    "domain": {
      "cpu": {"sockets": 4, "cores": 1, "threads": 1, "model": "host-passthrough", "dedicatedCpuPlacement": true},
      "memory": {"guest": "6144Mi"},
      "machine": {"type": "q35"},
      "firmware": {"bootloader": {"efi": {"secureBoot": false}}},
      "devices": {
        "disks": [
          {"name": "rootdisk", "disk": {"bus": "virtio"}, "bootOrder": 1},
          {"name": "iso", "cdrom": {"bus": "sata"}},
          {"name": "cloudinitdisk", "disk": {"bus": "virtio"}}
        ],
        "inputs": [{"type": "tablet", "bus": "usb", "name": "tablet"}]
      }
    },
    "volumes": [
      {"name": "rootdisk", "persistentVolumeClaim": {"claimName": "perf-a-bootc-disk"}},
      {"name": "iso", "dataVolume": {"name": "perf-a-iso"}},
      {"name": "cloudinitdisk", "cloudInitNoCloud": {"userData": "#cloud-config"}}
    ]
  }}},
  "status": {"printableStatus": "Stopped", "ready": false}
}`

const detailsPVCs = `{"items": [
  {"metadata": {"name": "perf-a-bootc-disk"}, "spec": {"resources": {"requests": {"storage": "40Gi"}}}, "status": {"capacity": {"storage": "40Gi"}}},
  {"metadata": {"name": "perf-a-iso"}, "spec": {"resources": {"requests": {"storage": "3Gi"}}}, "status": {}}
]}`

// A stopped VM: everything comes from the VM template, and the node is the
// one it is pinned to.
func TestParseVMDetails_Stopped(t *testing.T) {
	d, err := parseVMDetails([]byte(detailsVM), nil, []byte(detailsPVCs))
	if err != nil {
		t.Fatal(err)
	}
	want := types.VMDetails{
		Name: "perf-a", Namespace: "corral-vms", Backend: "kubevirt", Status: "Stopped",
		Node: "n1",
		CPU: types.VMCPU{VCPUs: 4, Sockets: 4, Cores: 1, Threads: 1, Model: "host-passthrough",
			DedicatedCPUPlacement: true},
		Memory: "6144Mi",
		Disks: []types.VMDisk{
			{Name: "rootdisk", Type: "disk", Bus: "virtio", Source: "persistentVolumeClaim", Claim: "perf-a-bootc-disk", Size: "40Gi"},
			{Name: "iso", Type: "cdrom", Bus: "sata", Source: "dataVolume", Claim: "perf-a-iso", Size: "3Gi"},
			{Name: "cloudinitdisk", Type: "disk", Bus: "virtio", Source: "cloudInitNoCloud"},
		},
		Inputs:    []types.VMInput{{Name: "tablet", Type: "tablet", Bus: "usb"}},
		Firmware:  types.VMFirmware{Type: "uefi", Machine: "q35"},
		TagValues: map[string]string{"pair": "run42"},
		Labels:    map[string]string{"corral": "perf-a", "corral.dev/tag.pair": "run42"},
		Image:     "ghcr.io/tuna-os/marlin:latest",
	}
	if !reflect.DeepEqual(d, want) {
		got, _ := json.MarshalIndent(d, "", "  ")
		exp, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("details =\n%s\nwant\n%s", got, exp)
	}
}

// A running VM: the instance supplies the real node, the IPs, and the
// expanded hardware (here an instancetype VM whose template has no cpu).
func TestParseVMDetails_RunningUsesInstance(t *testing.T) {
	vm := `{"metadata": {"name": "it", "namespace": "ns"},
	  "spec": {"template": {"spec": {"domain": {"devices": {}}}}},
	  "status": {"printableStatus": "Running", "ready": true}}`
	vmi := `{"spec": {"domain": {
	    "cpu": {"cores": 2, "threads": 2, "model": "host-model"},
	    "resources": {"requests": {"memory": "8Gi"}},
	    "machine": {"type": "pc-q35-rhel9.6.0"},
	    "firmware": {"kernelBoot": {"container": {"image": "x"}}},
	    "devices": {"inputs": [{"type": "tablet", "bus": "usb", "name": "tablet0"}]}}},
	  "status": {"nodeName": "karnataka", "interfaces": [
	    {"ipAddress": "10.42.0.7", "ipAddresses": ["10.42.0.7", "fd00::7"]},
	    {"ipAddress": "192.168.1.9"}]}}`
	d, err := parseVMDetails([]byte(vm), []byte(vmi), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "Running" || !d.Ready || d.Node != "karnataka" {
		t.Errorf("status/node = %q %v %q", d.Status, d.Ready, d.Node)
	}
	if want := (types.VMCPU{VCPUs: 4, Sockets: 1, Cores: 2, Threads: 2, Model: "host-model"}); d.CPU != want {
		t.Errorf("cpu = %+v, want %+v", d.CPU, want)
	}
	if d.Memory != "8Gi" {
		t.Errorf("memory = %q, want the request when guest is unset", d.Memory)
	}
	if d.Firmware != (types.VMFirmware{Type: "kernelBoot", Machine: "pc-q35-rhel9.6.0"}) {
		t.Errorf("firmware = %+v", d.Firmware)
	}
	if want := []string{"10.42.0.7", "fd00::7", "192.168.1.9"}; !reflect.DeepEqual(d.IPs, want) {
		t.Errorf("ips = %v, want %v", d.IPs, want)
	}
	if len(d.Inputs) != 1 || d.Inputs[0].Type != "tablet" || d.Inputs[0].Bus != "usb" {
		t.Errorf("inputs = %+v", d.Inputs)
	}
}

// BIOS is the default when no bootloader is named; efi without secureBoot
// means KubeVirt's default, which is secure boot on.
func TestParseVMDetails_Firmware(t *testing.T) {
	for body, want := range map[string]types.VMFirmware{
		`{}`:                          {Type: "bios"},
		`{"bootloader": {"efi": {}}}`: {Type: "uefi", SecureBoot: true},
		`{"bootloader": {"efi": {"secureBoot": true}}}`:   {Type: "uefi", SecureBoot: true},
		`{"bootloader": {"bios": {}}}`:                    {Type: "bios"},
		`{"bootloader": {"efi": {"secureBoot": false}}} `: {Type: "uefi"},
	} {
		vm := `{"metadata": {"name": "f"}, "spec": {"template": {"spec": {"domain": {"firmware": ` + body + `}}}}}`
		d, err := parseVMDetails([]byte(vm), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Firmware != want {
			t.Errorf("firmware %s = %+v, want %+v", body, d.Firmware, want)
		}
	}
}

// Details reads the VM, the instance, and the PVCs; a stopped VM (no
// instance) is not an error, a missing VM is.
func TestClientDetails(t *testing.T) {
	c, r := newFakeClient()
	t.Cleanup(func() { SetDefaultRunner(nil) })
	c.Context = "prod"
	r.AddResponseKV("kubectl", []string{"get", "vm", "perf-a", "-n", "tailvm", "-o", "json"}, detailsVM, nil)
	r.AddResponseKV("kubectl", []string{"get", "vmi", "perf-a", "-n", "tailvm", "-o", "json"}, "NotFound", errors.New("exit status 1"))
	r.AddResponseKV("kubectl", []string{"get", "pvc", "-n", "tailvm", "-o", "json"}, detailsPVCs, nil)

	d, err := c.Details("perf-a")
	if err != nil {
		t.Fatal(err)
	}
	if d.Context != "prod" || d.Node != "n1" || d.Disks[0].Size != "40Gi" {
		t.Errorf("details = %+v", d)
	}

	if _, err := c.Details("missing"); err == nil {
		t.Error("a missing VM was not an error")
	}
}
