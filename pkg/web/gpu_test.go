package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const kvWithGPUs = `{"spec":{"configuration":{"permittedHostDevices":{
  "pciHostDevices":[{"pciVendorSelector":"10de:1b80","resourceName":"nvidia.com/GP104"}],
  "mediatedDevices":[{"mdevNameSelector":"GRID T4-2Q","resourceName":"nvidia.com/T4-2Q"}]}}}}`

func TestListGPUs(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	fx.Runner.AddResponseKV("kubectl", []string{"get", "kubevirt", "kubevirt", "-n", "kubevirt", "-o", "json"}, kvWithGPUs, nil)

	resp, err := http.Get(fx.Server.URL + "/api/gpus")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var devs []map[string]string
	json.NewDecoder(resp.Body).Decode(&devs)
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2: %+v", len(devs), devs)
	}
	var pci, mdev bool
	for _, d := range devs {
		if d["resourceName"] == "nvidia.com/GP104" && d["type"] == "pci" {
			pci = true
		}
		if d["resourceName"] == "nvidia.com/T4-2Q" && d["type"] == "mediated" {
			mdev = true
		}
	}
	if !pci || !mdev {
		t.Errorf("expected both pci and mediated devices, got %+v", devs)
	}
}

func TestAttachGPU(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	// VM currently has no gpus, patch succeeds.
	fx.Runner.AddPrefixResponse("kubectl get vm gpuvm -n tailvm -o jsonpath=", "", nil)
	fx.Runner.AddPrefixResponse("kubectl patch vm gpuvm -n tailvm", "patched", nil)

	resp := mustPost(t, fx.Server.URL+"/api/vms/tailvm/gpuvm/gpus", `{"device":"nvidia.com/GP104"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("got %d, want 200 — %s", resp.StatusCode, b)
	}
	// The patch must carry the device as a named gpu.
	var sawPatch bool
	for _, c := range fx.Runner.Calls() {
		j := strings.Join(c.Args, " ")
		if strings.Contains(j, "patch vm gpuvm") && strings.Contains(j, "nvidia.com/GP104") && strings.Contains(j, "gpu1") {
			sawPatch = true
		}
	}
	if !sawPatch {
		t.Error("expected a VM patch adding the GPU as gpu1")
	}
}

func TestAttachGPU_RequiresDevice(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()
	resp := mustPost(t, fx.Server.URL+"/api/vms/tailvm/gpuvm/gpus", `{}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", resp.StatusCode)
	}
}
