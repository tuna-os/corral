package kubevirt

import (
	"encoding/json"
	"testing"

	"github.com/hanthor/corral/pkg/shell"
)

func newFakeClient() (*Client, *shell.Fake) {
	r := shell.NewFake()
	// Always succeed at looking up virtctl
	r.AddResponseKV("virtctl", nil, "", nil)
	// Also set package-level runners for functions that shell out directly
	SetDefaultRunner(r)
	SetPackageRunner(r)
	return NewClientWithRunner("tailvm", r), r
}

// ── VM lifecycle ──────────────────────────────────────────────────

func TestClient_VMExists_True(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vm", "testvm", "-n", "tailvm", "-o", "name"}, "vm.kubevirt.io/testvm", nil)

	if !c.VMExists("testvm") {
		t.Error("VMExists should return true")
	}
}

func TestClient_VMExists_False(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vm", "testvm", "-n", "tailvm", "-o", "name"}, "", errSimulated)

	if c.VMExists("testvm") {
		t.Error("VMExists should return false")
	}
}

func TestClient_StartVM(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"start", "testvm", "-n", "tailvm"}, "", nil)

	if err := c.StartVM("testvm"); err != nil {
		t.Fatalf("StartVM: %v", err)
	}
}

func TestClient_StartVM_Error(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"start", "testvm", "-n", "tailvm"}, "", errSimulated)

	if err := c.StartVM("testvm"); err == nil {
		t.Error("StartVM should return error")
	}
}

func TestClient_StopVM(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"stop", "testvm", "-n", "tailvm"}, "", nil)

	if err := c.StopVM("testvm"); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
}

func TestClient_DeleteVM(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"stop", "testvm", "-n", "tailvm"}, "", nil)
	r.AddResponseKV("kubectl", []string{"delete", "vm", "testvm", "-n", "tailvm", "--ignore-not-found"}, "", nil)
	r.AddPrefixResponse("kubectl delete pvc testvm-", "", nil)
	r.AddPrefixResponse("kubectl delete datavolume testvm-", "", nil)
	r.AddResponseKV("kubectl", []string{"delete", "pvc", "-n", "tailvm", "-l", "corral.dev/vm=testvm", "--ignore-not-found"}, "", nil)
	r.AddResponseKV("kubectl", []string{"delete", "vmsnapshot", "-n", "tailvm", "-l", "corral.dev/vm=testvm", "--ignore-not-found"}, "", nil)

	if err := c.DeleteVM("testvm"); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
}

func TestClient_VMInfo(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vm", "testvm", "-n", "tailvm", "-o", "json"}, `{"metadata":{"name":"testvm"}}`, nil)

	data, err := c.VMInfo("testvm")
	if err != nil {
		t.Fatalf("VMInfo: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}

func TestClient_ListVMs(t *testing.T) {
	c, r := newFakeClient()
	SetPackageRunner(r)
	r.AddResponseKV("kubectl", []string{"get", "vms", "-A", "-o", "json"}, `{"items":[]}`, nil)
	r.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"}, `{"items":[]}`, nil)

	vms, err := c.ListVMs()
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if vms == nil {
		t.Log("ListVMs returned nil for empty cluster (acceptable — callers normalize)")
		return
	}
}

func TestClient_ListVMs_WithVMs(t *testing.T) {
	c, r := newFakeClient()
	SetPackageRunner(r)
	r.AddResponseKV("kubectl", []string{"get", "vms", "-A", "-o", "json"}, `{"items":[
		{"metadata":{"name":"vm1","namespace":"tailvm"},"spec":{"template":{"spec":{"domain":{"cpu":{"sockets":2},"resources":{"requests":{"memory":"4Gi"}}}}}},"status":{"printableStatus":"Running","ready":true}}
	]}`, nil)
	r.AddResponseKV("kubectl", []string{"get", "vmis", "-A", "-o", "json"}, `{"items":[
		{"metadata":{"name":"vm1","namespace":"tailvm"},"status":{"nodeName":"karnataka","interfaces":[{"ipAddress":"10.0.0.1"}]}}
	]}`, nil)
	// nodeVendors uses runPkg which uses the package runner
	r.AddResponseKV("kubectl", []string{"get", "nodes", "-o", "json"}, `{"items":[{"metadata":{"name":"karnataka","labels":{}},"status":{"nodeInfo":{"architecture":"amd64"}}}]}`, nil)

	vms, err := c.ListVMs()
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("expected 1 VM, got %d", len(vms))
	}
	if vms[0].Name != "vm1" {
		t.Errorf("name = %q, want vm1", vms[0].Name)
	}
}

// ── KubeVirt actions ─────────────────────────────────────────────

func TestClient_RestartVM(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"restart", "testvm", "-n", "tailvm"}, "", nil)

	if err := c.RestartVM("testvm"); err != nil {
		t.Fatalf("RestartVM: %v", err)
	}
}

func TestClient_PauseVM(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"pause", "vm", "testvm", "-n", "tailvm"}, "", nil)

	if err := c.PauseVM("testvm"); err != nil {
		t.Fatalf("PauseVM: %v", err)
	}
}

func TestClient_UnpauseVM(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"unpause", "vm", "testvm", "-n", "tailvm"}, "", nil)

	if err := c.UnpauseVM("testvm"); err != nil {
		t.Fatalf("UnpauseVM: %v", err)
	}
}

// ── Scale ─────────────────────────────────────────────────────────

func TestClient_Scale_Stopped(t *testing.T) {
	c, r := newFakeClient()
	// VM domain — stopped, no live update needed
	r.AddResponseKV("kubectl", []string{"get", "vm", "testvm", "-n", "tailvm", "-o", "json"}, `{
		"metadata":{"name":"testvm"},
		"spec":{"template":{"spec":{"domain":{"cpu":{"sockets":1,"cores":1,"threads":1},"resources":{"requests":{"memory":"2Gi"}}}}}},
		"status":{"printableStatus":"Stopped"}
	}`, nil)
	r.AddPrefixResponse("kubectl patch vm testvm -n tailvm --type merge -p", "", nil)

	if err := c.Scale("testvm", 4, "8G"); err != nil {
		t.Fatalf("Scale: %v", err)
	}
}

// ── Volumes ──────────────────────────────────────────────────────

func TestClient_RemoveVolume(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"removevolume", "testvm", "--volume-name=disk-2", "-n", "tailvm"}, "", nil)

	if err := c.RemoveVolume("testvm", "disk-2"); err != nil {
		t.Fatalf("RemoveVolume: %v", err)
	}
}

func TestClient_ExpandDisk(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"patch", "pvc", "testvm-disk", "-n", "tailvm", "--type", "merge", "-p", `{"spec":{"resources":{"requests":{"storage":"40Gi"}}}}`}, "", nil)

	if err := c.ExpandDisk("testvm-disk", "40Gi"); err != nil {
		t.Fatalf("ExpandDisk: %v", err)
	}
}

// ── Snapshots ────────────────────────────────────────────────────

func TestClient_ListSnapshots(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "vmsnapshot", "-n", "tailvm", "-o", "json"}, `{"items":[]}`, nil)

	snaps, err := c.ListSnapshots("testvm")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if snaps == nil {
		t.Error("ListSnapshots returned nil")
	}
}

func TestClient_DeleteSnapshot(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"delete", "vmsnapshot", "snap1", "-n", "tailvm", "--ignore-not-found"}, "", nil)

	if err := c.DeleteSnapshot("snap1"); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
}

// ── Guest info ───────────────────────────────────────────────────

func TestClient_GuestInfo(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("/fake/bin/virtctl", []string{"guestosinfo", "testvm", "-n", "tailvm"}, `{"name":"fedora","version":"42"}`, nil)
	r.AddResponseKV("/fake/bin/virtctl", []string{"fslist", "testvm", "-n", "tailvm"}, `{"items":[]}`, nil)
	r.AddResponseKV("/fake/bin/virtctl", []string{"userlist", "testvm", "-n", "tailvm"}, `{"items":[]}`, nil)

	info, err := c.GuestInfo("testvm")
	if err != nil {
		t.Fatalf("GuestInfo: %v", err)
	}
	if info["os"] == nil {
		t.Error("missing os field")
	}
}

// ── Events ───────────────────────────────────────────────────────

func TestClient_Events(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"get", "events", "-n", "tailvm", "-o", "json"}, `{"items":[]}`, nil)

	evs, err := c.Events("testvm")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if evs == nil {
		t.Error("Events returned nil")
	}
}

// ── Template ─────────────────────────────────────────────────────

func TestClient_MarkTemplate(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"label", "vm", "testvm", "-n", "tailvm", "corral.dev/template=true", "--overwrite"}, "", nil)

	if err := c.MarkTemplate("testvm", true); err != nil {
		t.Fatalf("MarkTemplate(true): %v", err)
	}
}

func TestClient_MarkTemplate_Off(t *testing.T) {
	c, r := newFakeClient()
	r.AddResponseKV("kubectl", []string{"label", "vm", "testvm", "-n", "tailvm", "corral.dev/template-", "--overwrite"}, "", nil)

	if err := c.MarkTemplate("testvm", false); err != nil {
		t.Fatalf("MarkTemplate(false): %v", err)
	}
}

// ── NIC ──────────────────────────────────────────────────────────

func TestClient_AddNIC(t *testing.T) {
	c, r := newFakeClient()
	r.AddPrefixResponse("kubectl patch vm testvm -n tailvm --type json -p", "", nil)

	if err := c.AddNIC("testvm", "default/lan", "eth1"); err != nil {
		t.Fatalf("AddNIC: %v", err)
	}
}

var errSimulated = errTestSentinel

var errTestSentinel = &testError{"simulated"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
