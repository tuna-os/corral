package main

import (
	"os"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/device"
	"github.com/tuna-os/corral/pkg/shell"
)

func withFake(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	runner = fake
	t.Cleanup(func() { runner = shell.Real{} })
	return fake
}

const kvCRWithAMD = `{
  "spec": {"configuration": {"permittedHostDevices": {
    "pciHostDevices": [
      {"pciVendorSelector": "1002:744c", "resourceName": "amd.com/gpu"}
    ]
  }}}
}`

func TestIsDeviceResource(t *testing.T) {
	tests := map[string]bool{
		"cpu":                     false,
		"memory":                  false,
		"hugepages-2Mi":           false,
		"devices.kubevirt.io/kvm": false,
		"amd.com/gpu":             true,
		"nvidia.com/GA102":        true,
		"intel.com/sriov_dp":      true,
	}
	for name, want := range tests {
		if got := isDeviceResource(name); got != want {
			t.Errorf("isDeviceResource(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNodeDeviceResources(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "nodes", "-o", "json"}, `{
	  "items": [
	    {"metadata": {"name": "karnataka"},
	     "status": {"allocatable": {"cpu": "32", "amd.com/gpu": "1", "devices.kubevirt.io/kvm": "1k"}}},
	    {"metadata": {"name": "bihar"},
	     "status": {"allocatable": {"cpu": "8"}}}
	  ]
	}`, nil)

	nodes, err := nodeDeviceResources()
	if err != nil {
		t.Fatalf("nodeDeviceResources: %v", err)
	}
	if len(nodes) != 1 || nodes["karnataka"]["amd.com/gpu"] != "1" {
		t.Errorf("nodes = %v", nodes)
	}
}

func TestEnableDevice_AppendsAndPreserves(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "kubevirt", "kubevirt", "-n", "kubevirt", "-o", "json"},
		kvCRWithAMD, nil)
	fake.AddPrefixResponse("kubectl patch kubevirt kubevirt -n kubevirt --type merge -p", "patched", nil)

	if err := enableDevice("10de:2204", "nvidia.com/GA102"); err != nil {
		t.Fatalf("enableDevice: %v", err)
	}
	var patch string
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "patch" {
			patch = strings.Join(c.Args, " ")
		}
	}
	// Existing AMD entry preserved, new NVIDIA entry added.
	for _, want := range []string{"1002:744c", "amd.com/gpu", "10de:2204", "nvidia.com/GA102"} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch missing %q: %s", want, patch)
		}
	}
}

func TestEnableDevice_Idempotent(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "kubevirt", "kubevirt", "-n", "kubevirt", "-o", "json"},
		kvCRWithAMD, nil)

	if err := enableDevice("1002:744c", "amd.com/gpu"); err != nil {
		t.Fatalf("enableDevice: %v", err)
	}
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "patch" {
			t.Error("already-permitted device should not be patched again")
		}
	}
}

func TestAttachGPU_FirstDevice(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "web", "-n", "tailvm", "-o",
		"jsonpath={.spec.template.spec.domain.devices.gpus}"}, "", nil)
	fake.AddPrefixResponse("kubectl patch vm web -n tailvm --type merge -p", "patched", nil)

	if err := attachGPU("web", "tailvm", "amd.com/gpu", ""); err != nil {
		t.Fatalf("attachGPU: %v", err)
	}
	var patch string
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "patch" {
			patch = strings.Join(c.Args, " ")
		}
	}
	for _, want := range []string{`"gpus":[`, `"name":"gpu1"`, `"deviceName":"amd.com/gpu"`} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch missing %q: %s", want, patch)
		}
	}
}

func TestAttachGPU_DuplicateName(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "web", "-n", "tailvm", "-o",
		"jsonpath={.spec.template.spec.domain.devices.gpus}"},
		`[{"name":"gpu1","deviceName":"amd.com/gpu"}]`, nil)

	if err := attachGPU("web", "tailvm", "amd.com/gpu", "gpu1"); err == nil {
		t.Fatal("duplicate GPU name should fail")
	}
}

func TestDetachGPU(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "web", "-n", "tailvm", "-o",
		"jsonpath={.spec.template.spec.domain.devices.gpus}"},
		`[{"name":"gpu1","deviceName":"amd.com/gpu"},{"name":"gpu2","deviceName":"nvidia.com/GA102"}]`, nil)
	fake.AddPrefixResponse("kubectl patch vm web -n tailvm --type merge -p", "patched", nil)

	if err := detachGPU("web", "tailvm", "gpu1"); err != nil {
		t.Fatalf("detachGPU: %v", err)
	}
	var patch string
	for _, c := range fake.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "patch" {
			patch = strings.Join(c.Args, " ")
		}
	}
	if strings.Contains(patch, "gpu1") || !strings.Contains(patch, "gpu2") {
		t.Errorf("detach patch should keep gpu2 only: %s", patch)
	}
}

func TestDetachGPU_NotFound(t *testing.T) {
	fake := withFake(t)
	fake.AddResponseKV("kubectl", []string{"get", "vm", "web", "-n", "tailvm", "-o",
		"jsonpath={.spec.template.spec.domain.devices.gpus}"}, "", nil)

	if err := detachGPU("web", "tailvm", "gpu9"); err == nil {
		t.Fatal("detaching a nonexistent GPU should fail")
	}
}

// ── context and reference plumbing ────────────────────────────────

func TestTarget_DefaultsAndUnknownContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got, err := target("")
	if err != nil {
		t.Fatalf("target(\"\"): %v", err)
	}
	if got.Backend == "" {
		t.Error("the default context must name a backend")
	}

	// Passing a device through to a VM on the wrong host takes the device away
	// from that host, so an unknown context has to stop rather than default.
	_, err = target("no-such-context")
	if err == nil {
		t.Fatal("an unknown context must be an error")
	}
	if !strings.Contains(err.Error(), "corral context ls") {
		t.Errorf("error = %v, want it to point at `corral context ls`", err)
	}
}

// A namespace only means something on KubeVirt. Carrying one onto a libvirt or
// Incus reference would make two references to the same instance compare
// unequal, and the registry keys off exactly this.
func TestInstanceRef_NamespaceOnlyForKubevirt(t *testing.T) {
	kube := instanceRef(config.ContextConfig{Backend: "kubevirt", Context: "prod"}, "web-1", "corral-vms")
	if kube.Namespace != "corral-vms" {
		t.Errorf("kubevirt ref lost its namespace: %+v", kube)
	}
	if kube.Backend != "kubevirt" || kube.Context != "prod" || kube.Name != "web-1" {
		t.Errorf("kubevirt ref = %+v", kube)
	}

	for _, backend := range []string{"libvirt", "incus", "qemu"} {
		ref := instanceRef(config.ContextConfig{Backend: backend}, "vm-1", "corral-vms")
		if ref.Namespace != "" {
			t.Errorf("%s ref carries a namespace it has no concept of: %+v", backend, ref)
		}
	}
}

func TestOrDash(t *testing.T) {
	if got := orDash(""); got != "—" {
		t.Errorf("orDash(\"\") = %q, want an em dash so a column never renders empty", got)
	}
	if got := orDash("nvidia"); got != "nvidia" {
		t.Errorf("orDash(%q) = %q", "nvidia", got)
	}
}

// --yes is what unattended callers pass. Without it, confirm reads stdin — and
// an empty answer means no, because the default for "take this device away
// from the host" cannot be yes.
func TestConfirm_AssumeYesSkipsThePrompt(t *testing.T) {
	dev := device.Device{ID: "0000:01:00.0", Description: "NVIDIA GPU"}
	if err := confirm(dev, []device.Consequence{"the host loses this device"}, true); err != nil {
		t.Fatalf("confirm with --yes: %v", err)
	}
}

func TestConfirm_EmptyAnswerCancels(t *testing.T) {
	stdin, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = orig })

	err = confirm(device.Device{ID: "0000:01:00.0"}, nil, false)
	if err == nil {
		t.Fatal("an empty answer must cancel, not proceed")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error = %v, want a cancellation", err)
	}
}
