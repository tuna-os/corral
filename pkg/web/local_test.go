package web

// Local (QEMU) backend in the dashboard — issue #91 Phase 1 coverage.
// qemu.List reads $HOME/.local/share/corral/vms, so a temp HOME with a
// metadata.json is a complete fake local backend.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
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

func TestLocalVNCBridge_DialsLocalPort(t *testing.T) {
	// Real TCP listener plays the QEMU VNC server; metadata points at it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local", "share", "corral", "vms", "laptopvm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"name":"laptopvm","cpu":1,"memory":"1G","vnc_port":%d,"tailscale_ip":"127.0.0.1"}`, port)
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		got <- string(buf[:n])
	}()

	fx := NewTestFixture()
	defer fx.Close()
	ws, err := websocket.Dial(wsURL(fx.Server.URL)+"/api/vnc/local/laptopvm", "", "http://localhost")
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer ws.Close()
	if _, err := ws.Write([]byte("rfb-hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != "rfb-hello" {
			t.Errorf("VNC listener got %q, want rfb-hello", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bytes never reached the local VNC listener")
	}
}

func TestCreateLocalVM_Validation(t *testing.T) {
	fakeLocalVM(t, "existingvm")
	fx := NewTestFixture()
	defer fx.Close()

	// No source → clear 400, not a kubectl error.
	resp, err := http.Post(fx.Server.URL+"/api/vms", "application/json",
		strings.NewReader(`{"name":"newlocal","target":"local"}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body["error"], "ISO or a qcow2") {
		t.Fatalf("want 400 with source guidance, got %d %q", resp.StatusCode, body["error"])
	}

	// A URL source is accepted async (202) and lands in the task log.
	resp, err = http.Post(fx.Server.URL+"/api/vms", "application/json",
		strings.NewReader(`{"name":"newlocal","target":"local","iso":"http://127.0.0.1:1/never.iso"}`))
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || body["status"] != "downloading" {
		t.Fatalf("want 202 downloading, got %d %+v", resp.StatusCode, body)
	}
}
