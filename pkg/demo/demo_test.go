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

func TestDemoCluster_MigrateAndPatch(t *testing.T) {
	d := newDemoCluster()
	v := d.find("web-prod", "corral-vms")
	if v == nil {
		t.Fatal("web-prod VM not found")
	}
	if v.Node != "corral-1" {
		t.Fatalf("expected web-prod to be on corral-1 initially, got %s", v.Node)
	}

	// 1. Pinned migration: patch nodeSelector, then run virtctl migrate
	patchJSON := `{"spec":{"template":{"spec":{"nodeSelector":{"kubernetes.io/hostname":"corral-2"}}}}}`
	if _, err := d.dispatch("", "kubectl", []string{"patch", "vm", "web-prod", "-n", "corral-vms", "--type", "merge", "-p", patchJSON}); err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if v.Node != "corral-2" {
		t.Fatalf("expected web-prod node to be updated to corral-2 by patch, got %s", v.Node)
	}
	if _, err := d.virtctl([]string{"migrate", "web-prod", "-n", "corral-vms"}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if v.Node != "corral-2" {
		t.Fatalf("expected web-prod node to remain corral-2 after pinned migrate, got %s", v.Node)
	}

	// 2. Unpinned migration: virtctl migrate cycles to another node
	if _, err := d.virtctl([]string{"migrate", "web-prod", "-n", "corral-vms"}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if v.Node != "corral-3" {
		t.Fatalf("expected web-prod node to cycle to corral-3 after unpinned migrate, got %s", v.Node)
	}

	// 3. Test get vmi
	out, err := d.dispatch("", "kubectl", []string{"get", "vmi", "web-prod", "-n", "corral-vms", "-o", "json"})
	if err != nil {
		t.Fatalf("get vmi failed: %v", err)
	}
	var vmi map[string]any
	if err := json.Unmarshal(out, &vmi); err != nil {
		t.Fatalf("failed to parse vmi json: %v", err)
	}
	status := vmi["status"].(map[string]any)
	if status["nodeName"] != "corral-3" {
		t.Errorf("expected vmi status.nodeName to be corral-3, got %v", status["nodeName"])
	}
}
