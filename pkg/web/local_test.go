package web

// Local (QEMU) backend in the dashboard — issue #91 Phase 1 coverage.
// qemu.List reads $HOME/.local/share/corral/vms, so a temp HOME with a
// metadata.json is a complete fake local backend.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeLocalVM(t *testing.T, name string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local", "share", "corral", "vms", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"name":"` + name + `","cpu":2,"memory":"4G","disk_size":"20G","vnc_port":5901,"tailscale_ip":"100.64.0.9"}`
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocalVMs_MergedIntoList(t *testing.T) {
	fakeLocalVM(t, "laptopvm")
	fx := NewTestFixture()
	defer fx.Close()
	fx.Runner.AddResponse("kubectl get vms -A -o json", `{"items":[]}`, nil)
	fx.Runner.AddResponse("kubectl get vmis -A -o json", `{"items":[]}`, nil)
	fx.Runner.AddPrefixResponse("kubectl get pods -A -l kubevirt.io=virt-launcher", `{"items":[]}`, nil)
	fx.Runner.AddPrefixResponse("kubectl get nodes -o json", `{"items":[]}`, nil)
	fx.Runner.AddPrefixResponse("kubectl get pvc -A -l corral.dev/ct=true", `{"items":[]}`, nil)

	resp, err := http.Get(fx.Server.URL + "/api/vms")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var vms []map[string]any
	json.NewDecoder(resp.Body).Decode(&vms)
	found := false
	for _, v := range vms {
		if v["name"] == "laptopvm" {
			found = true
			if v["namespace"] != "local" || v["node"] != "local" || v["backend"] != "qemu" {
				t.Errorf("local VM not shaped for the dashboard: %+v", v)
			}
		}
	}
	if !found {
		t.Fatalf("local qemu VM missing from /api/vms: %+v", vms)
	}
}

func TestLocalVMs_ListWorksWithoutCluster(t *testing.T) {
	// No kubectl responses registered → kubevirt list fails. With a local VM
	// present the dashboard must still work, local-only — not 502.
	fakeLocalVM(t, "onlylocal")
	fx := NewTestFixture()
	defer fx.Close()

	resp, err := http.Get(fx.Server.URL + "/api/vms")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/vms status %d with local VMs present, want 200", resp.StatusCode)
	}
	var vms []map[string]any
	json.NewDecoder(resp.Body).Decode(&vms)
	if len(vms) != 1 || vms[0]["name"] != "onlylocal" {
		t.Fatalf("want the local VM alone, got %+v", vms)
	}

	nresp, err := http.Get(fx.Server.URL + "/api/nodes")
	if err != nil {
		t.Fatal(err)
	}
	defer nresp.Body.Close()
	var nodes []map[string]any
	json.NewDecoder(nresp.Body).Decode(&nodes)
	if nresp.StatusCode != http.StatusOK || len(nodes) != 1 || nodes[0]["name"] != "local" {
		t.Fatalf("/api/nodes should serve the synthetic local node: %d %+v", nresp.StatusCode, nodes)
	}
}

func TestLocalVMs_ClusterOnlyActionsRejected(t *testing.T) {
	fakeLocalVM(t, "laptopvm")
	fx := NewTestFixture()
	defer fx.Close()

	for _, action := range []string{"pause", "unpause", "migrate"} {
		resp, err := http.Post(fx.Server.URL+"/api/vms/local/laptopvm/"+action, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		body := make(map[string]string)
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s on a local VM: status %d, want 400", action, resp.StatusCode)
		}
		if !strings.Contains(body["error"], "not supported") {
			t.Errorf("%s error should say not supported: %q", action, body["error"])
		}
	}
}
