//go:build bootc

package kubevirt

import "testing"

// The bootc final VM takes the same placement as a plain VM, and keeps the
// labels and annotations bootc rebuild relies on.
func TestRenderBootcFinalVM_Placement(t *testing.T) {
	raw := generateBootcVM("perf-b", "corral-vms", "perf-b-bootc-disk",
		"ghcr.io/tuna-os/marlin:latest", "ssh-ed25519 AAAAKEY u@h", "6G", 4, "n1")
	ApplyPlacement(raw, Placement{DedicatedCPUs: true, Tags: map[string]string{"pair": "run42"}})
	vm := render(t, raw)

	dom := vm.Spec.Template.Spec.Domain
	if dom.CPU["dedicatedCpuPlacement"] != true {
		t.Errorf("cpu = %v, want dedicatedCpuPlacement", dom.CPU)
	}
	if _, ok := dom.CPU["isolateEmulatorThread"]; ok {
		t.Errorf("isolateEmulatorThread set without being asked: %v", dom.CPU)
	}
	if _, ok := dom.CPU["maxSockets"]; ok {
		t.Errorf("cpu keeps maxSockets: %v", dom.CPU)
	}
	if dom.Resources.Requests["memory"] != "6144Mi" || dom.Resources.Limits["memory"] != "6144Mi" {
		t.Errorf("resources = %+v, want memory request == limit == 6144Mi", dom.Resources)
	}
	labels := vm.Metadata.Labels
	if labels["corral.dev/tag.pair"] != "run42" || labels["corral"] != "perf-b" {
		t.Errorf("labels = %v", labels)
	}
	if _, ok := labels["corral-bootc-image"]; !ok {
		t.Errorf("bootc label lost: %v", labels)
	}
	ann := raw["metadata"].(map[string]any)["annotations"].(map[string]any)
	if ann["corral.bootc/image"] != "ghcr.io/tuna-os/marlin:latest" {
		t.Errorf("bootc image annotation lost: %v", ann)
	}
}
