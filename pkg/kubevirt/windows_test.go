package kubevirt

import (
	"encoding/json"
	"testing"
)

func TestGenerateWindowsVM_KindAndAPIVersion(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	if vm["kind"] != "VirtualMachine" {
		t.Errorf("kind = %v, want VirtualMachine", vm["kind"])
	}
	if vm["apiVersion"] != "kubevirt.io/v1" {
		t.Errorf("apiVersion = %v, want kubevirt.io/v1", vm["apiVersion"])
	}
}

func TestGenerateWindowsVM_Metadata(t *testing.T) {
	vm := GenerateWindowsVM("mywin", "myns", "8Gi", 4, false)
	meta, ok := vm["metadata"].(map[string]any)
	if !ok {
		t.Fatal("metadata is not a map")
	}
	if meta["name"] != "mywin" {
		t.Errorf("metadata.name = %v, want mywin", meta["name"])
	}
	if meta["namespace"] != "myns" {
		t.Errorf("metadata.namespace = %v, want myns", meta["namespace"])
	}
	labels, ok := meta["labels"].(map[string]any)
	if !ok {
		t.Fatal("metadata.labels is not a map")
	}
	if labels["corral.dev/os"] != "windows" {
		t.Errorf("labels[corral.dev/os] = %v, want windows", labels["corral.dev/os"])
	}
}

func TestGenerateWindowsVM_NotRunning(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "4G", 2, false)
	spec, ok := vm["spec"].(map[string]any)
	if !ok {
		t.Fatal("spec is not a map")
	}
	if spec["running"] != false {
		t.Errorf("spec.running = %v, want false", spec["running"])
	}
}

func TestGenerateWindowsVM_MachineType(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	domain := getNested(t, vm, "spec", "template", "spec", "domain").(map[string]any)
	machine := domain["machine"].(map[string]any)
	if machine["type"] != "q35" {
		t.Errorf("machine.type = %v, want q35", machine["type"])
	}
}

func TestGenerateWindowsVM_EFI(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	firmware := getNested(t, vm, "spec", "template", "spec", "domain", "firmware").(map[string]any)
	bootloader := firmware["bootloader"].(map[string]any)
	efi := bootloader["efi"].(map[string]any)
	if efi["secureBoot"] != false {
		t.Errorf("secureBoot = %v, want false", efi["secureBoot"])
	}
}

func TestGenerateWindowsVM_HyperVEnlightenments(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	features := getNested(t, vm, "spec", "template", "spec", "domain", "features").(map[string]any)
	hyperv, ok := features["hyperv"].(map[string]any)
	if !ok {
		t.Fatal("hyperv features missing")
	}
	required := []string{"relaxed", "vapic", "vpindex", "synic", "synictimer", "spinlocks", "frequencies", "ipi"}
	for _, r := range required {
		if _, exists := hyperv[r]; !exists {
			t.Errorf("missing hyperv.enlightenment %q", r)
		}
	}
	spinlocks, ok := hyperv["spinlocks"].(map[string]any)
	if ok && spinlocks["spinlocks"] != 8191 {
		t.Errorf("spinlocks.spinlocks = %v, want 8191", spinlocks["spinlocks"])
	}
}

func TestGenerateWindowsVM_ClockTimers(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	clock := getNested(t, vm, "spec", "template", "spec", "domain", "clock").(map[string]any)
	timer, ok := clock["timer"].(map[string]any)
	if !ok {
		t.Fatal("clock.timer missing")
	}
	hpet, ok := timer["hpet"].(map[string]any)
	if ok && hpet["present"] != false {
		t.Errorf("hpet.present should be false")
	}
}

func TestGenerateWindowsVM_TPM(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	devices := getNested(t, vm, "spec", "template", "spec", "domain", "devices").(map[string]any)
	if _, ok := devices["tpm"].(map[string]any); !ok {
		t.Fatal("tpm device missing (required for Windows 11)")
	}
}

func TestGenerateWindowsVM_Disks(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	devices := getNested(t, vm, "spec", "template", "spec", "domain", "devices").(map[string]any)
	disks, ok := devices["disks"].([]map[string]any)
	if !ok {
		t.Fatal("disks is not a slice")
	}
	if len(disks) != 3 {
		t.Fatalf("expected 3 disks, got %d", len(disks))
	}

	// First disk: rootdisk (virtio, bootOrder 1)
	d0 := disks[0]
	if d0["name"] != "rootdisk" || d0["bootOrder"] != 1 {
		t.Errorf("first disk unexpected: %v", d0)
	}

	// Second disk: Windows ISO (sata, bootOrder 2)
	d1 := disks[1]
	if d1["name"] != "windows-iso" || d1["bootOrder"] != 2 {
		t.Errorf("second disk unexpected: %v", d1)
	}

	// Third disk: virtio drivers (cdrom)
	d2 := disks[2]
	if d2["name"] != "virtio-drivers" {
		t.Errorf("third disk unexpected: %v", d2)
	}
}

func TestGenerateWindowsVM_Volumes(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	volumes := getNested(t, vm, "spec", "template", "spec", "volumes").([]map[string]any)
	if len(volumes) != 3 {
		t.Fatalf("expected 3 volumes, got %d", len(volumes))
	}

	v0 := volumes[0]
	if v0["name"] != "rootdisk" {
		t.Errorf("volume[0].name = %v, want rootdisk", v0["name"])
	}

	v2 := volumes[2]
	cd, ok := v2["containerDisk"].(map[string]any)
	if !ok {
		t.Fatal("volume[2] should be a containerDisk")
	}
	if cd["image"] != VirtioWinImage {
		t.Errorf("virtio-win image = %v, want %s", cd["image"], VirtioWinImage)
	}
}

func TestGenerateWindowsVM_NetworkInterface(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	interfaces := getNested(t, vm, "spec", "template", "spec", "domain", "devices", "interfaces").([]map[string]any)
	if len(interfaces) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(interfaces))
	}
	iface := interfaces[0]
	if iface["model"] != "e1000e" {
		t.Errorf("interface model = %v, want e1000e", iface["model"])
	}
}

func TestGenerateWindowsVM_InputDevice(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	inputs := getNested(t, vm, "spec", "template", "spec", "domain", "devices", "inputs").([]map[string]any)
	if len(inputs) != 1 {
		t.Fatalf("expected 1 input device, got %d", len(inputs))
	}
	input := inputs[0]
	if input["type"] != "tablet" || input["bus"] != "usb" {
		t.Errorf("input device unexpected: %v", input)
	}
}

func TestGenerateWindowsVM_CPUCores(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 8, false)
	cpu := getNested(t, vm, "spec", "template", "spec", "domain", "cpu").(map[string]any)
	if cpu["cores"] != 8 {
		t.Errorf("cpu.cores = %v, want 8", cpu["cores"])
	}
}

func TestGenerateWindowsVM_Memory(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "16Gi", 4, false)
	resources := getNested(t, vm, "spec", "template", "spec", "domain", "resources").(map[string]any)
	requests, ok := resources["requests"].(map[string]any)
	if !ok {
		t.Fatal("resources.requests missing")
	}
	if requests["memory"] != "16Gi" {
		t.Errorf("memory = %v, want 16Gi", requests["memory"])
	}
}

func TestGenerateWindowsVM_JSONSerializable(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	// Verify the manifest can be marshalled to JSON (no circular refs, etc.)
	data, err := json.Marshal(vm)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}
	if len(data) < 500 {
		t.Errorf("JSON output too short (%d bytes), expected >= 500", len(data))
	}
	// Verify it round-trips
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}
	if decoded["kind"] != "VirtualMachine" {
		t.Errorf("round-trip kind = %v, want VirtualMachine", decoded["kind"])
	}
}

func TestGenerateWindowsVM_NetworkAndPod(t *testing.T) {
	vm := GenerateWindowsVM("win11", "default", "8Gi", 4, false)
	networks := getNested(t, vm, "spec", "template", "spec", "networks").([]map[string]any)
	if len(networks) != 1 {
		t.Fatalf("expected 1 network, got %d", len(networks))
	}
	net := networks[0]
	if net["name"] != "default" {
		t.Errorf("network name = %v, want default", net["name"])
	}
	pod, ok := net["pod"].(map[string]any)
	if !ok {
		t.Error("network should have pod section")
	}
	_ = pod
}

// ── Helper ───────────────────────────────────────────────────────────────────

func getNested(t *testing.T, m map[string]any, keys ...string) any {
	t.Helper()
	current := m
	for i, key := range keys {
		if i == len(keys)-1 {
			return current[key]
		}
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("key %q (path: %v) is not a map, got %T", key, keys[:i+1], current[key])
		}
		current = next
	}
	return nil
}
