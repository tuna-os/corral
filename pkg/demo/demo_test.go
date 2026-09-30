package demo

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnableAndRunner(t *testing.T) {
	runner := Enable()
	if runner == nil {
		t.Fatal("expected non-nil runner from Enable()")
	}

	path, err := runner.LookPath("kubectl")
	if err != nil || !strings.Contains(path, "kubectl") {
		t.Errorf("unexpected LookPath result: %v, %v", path, err)
	}

	out, err := runner.Run("kubectl", "get", "nodes", "-o", "json")
	if err != nil {
		t.Fatalf("unexpected error running kubectl get nodes: %v", err)
	}
	var nodes map[string]any
	if err := json.Unmarshal(out, &nodes); err != nil {
		t.Fatalf("failed to parse json output: %v", err)
	}
}

func TestVirtctlCommands(t *testing.T) {
	d := newDemoCluster()
	v := d.find("win11-desktop", "corral-vms")
	if v == nil {
		t.Fatal("win11-desktop VM not found in initial demo cluster")
	}
	if v.Status != "Paused" {
		t.Errorf("expected initial status Paused, got %s", v.Status)
	}

	if _, err := d.virtctl([]string{"unpause", "vm", "win11-desktop", "-n", "corral-vms"}); err != nil {
		t.Fatalf("unpause failed: %v", err)
	}
	if v.Status != "Running" {
		t.Errorf("expected status Running after unpause, got %s", v.Status)
	}

	if _, err := d.virtctl([]string{"stop", "win11-desktop", "-n", "corral-vms"}); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if v.Status != "Stopped" {
		t.Errorf("expected status Stopped after stop, got %s", v.Status)
	}

	if _, err := d.virtctl([]string{"start", "win11-desktop", "-n", "corral-vms"}); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if v.Status != "Running" {
		t.Errorf("expected status Running after start, got %s", v.Status)
	}
}

func TestApplyAndDelete(t *testing.T) {
	d := newDemoCluster()

	manifest := `
kind: VirtualMachine
metadata:
  name: test-new-vm
  namespace: corral-vms
`
	d.applyManifest(manifest)
	if v := d.find("test-new-vm", "corral-vms"); v == nil {
		t.Fatal("expected test-new-vm to be created via applyManifest")
	}

	d.deleteVM("test-new-vm", "corral-vms")
	if v := d.find("test-new-vm", "corral-vms"); v != nil {
		t.Fatal("expected test-new-vm to be deleted")
	}
}
