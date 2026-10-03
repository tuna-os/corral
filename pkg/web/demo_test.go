package web

// End-to-end smoke test of demo mode: EnableDemo + the real mux, exercising
// the same request flow the dashboard uses. This is the CI safety net for
// the whole demo seam — if a handler's kubectl usage drifts away from what
// pkg/demo answers, it fails here instead of on someone's first
// `corral web --demo`.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/registry"
	"golang.org/x/net/websocket"
)

func newDemoServer(t *testing.T) *httptest.Server {
	t.Helper()
	// demo.Enable sets CORRAL_INCUS_REMOTE for the whole process, which would
	// otherwise leave every later test in this package with an extra Incus
	// context that nothing stubs. Claiming the variable first makes the test
	// framework put it back afterwards.
	t.Setenv("CORRAL_INCUS_REMOTE", "")
	previousDemo := demoMode
	previousFolderStore := folderStore
	previousTheme := activeTheme
	previousCLITheme := cliTheme
	resetActivity()
	EnableDemo()
	tmpDir := t.TempDir()
	store = registry.NewStoreAt(tmpDir + "/registry.json")
	t.Cleanup(func() {
		// Restore the seams so later tests in this package start clean.
		demoMode = previousDemo
		folderStore = previousFolderStore
		activeTheme = previousTheme
		cliTheme = previousCLITheme
		config.SetForceKubevirtContext(false)
		qemu.SetStateDirs("", "")
		f := NewTestFixture()
		f.Close()
	})
	mux, err := newMux()
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, srv *httptest.Server, path string, out any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
}

func TestDemoVNCCompletesRFBHandshake(t *testing.T) {
	srv := newDemoServer(t)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/vnc/corral-vms/web-prod"
	ws, err := websocket.Dial(wsURL, "", srv.URL)
	if err != nil {
		t.Fatalf("dial demo console: %v", err)
	}
	defer func() { _ = ws.Close() }()

	version := make([]byte, 12)
	if _, err := io.ReadFull(ws, version); err != nil || string(version) != "RFB 003.008\n" {
		t.Fatalf("server version = %q, err=%v", version, err)
	}
	if _, err := ws.Write(version); err != nil {
		t.Fatalf("write client version: %v", err)
	}
	security := make([]byte, 2)
	if _, err := io.ReadFull(ws, security); err != nil || !bytes.Equal(security, []byte{1, 1}) {
		t.Fatalf("security types = %v, err=%v", security, err)
	}
	if _, err := ws.Write([]byte{1}); err != nil {
		t.Fatalf("choose security: %v", err)
	}
	result := make([]byte, 4)
	if _, err := io.ReadFull(ws, result); err != nil || !bytes.Equal(result, make([]byte, 4)) {
		t.Fatalf("security result = %v, err=%v", result, err)
	}
	if _, err := ws.Write([]byte{1}); err != nil {
		t.Fatalf("write ClientInit: %v", err)
	}
	serverInit := make([]byte, 24)
	if _, err := io.ReadFull(ws, serverInit); err != nil {
		t.Fatalf("read ServerInit: %v", err)
	}
	if width, height := int(serverInit[0])<<8|int(serverInit[1]), int(serverInit[2])<<8|int(serverInit[3]); width != 800 || height != 600 {
		t.Fatalf("demo framebuffer = %dx%d, want 800x600", width, height)
	}
}

func TestDemoMode_EndToEnd(t *testing.T) {
	srv := newDemoServer(t)

	// The fleet lists with real derived state.
	var vms []map[string]any
	getJSON(t, srv, "/api/vms", &vms)
	if len(vms) < 8 {
		t.Fatalf("demo fleet has %d VMs, want >= 8", len(vms))
	}
	byName := map[string]map[string]any{}
	for _, v := range vms {
		byName[v["name"].(string)] = v
	}
	if v := byName["web-prod"]; v == nil || v["running"] != true || v["ip"] != "10.42.1.20" {
		t.Errorf("web-prod not running with its IP: %+v", byName["web-prod"])
	}
	if v := byName["win11-desktop"]; v == nil || !strings.Contains(v["status"].(string), "Paused") {
		t.Errorf("win11-desktop should be paused: %+v", byName["win11-desktop"])
	}

	// CTs and nodes populate. Three CTs: two pet pods plus the demo remote's
	// Incus container, which only started appearing once pkg/ct's Incus path
	// went through the runner seam instead of exec.Command — before that it was
	// invisible in demo mode, and the Incus *virtual machine* on the same
	// remote was wrongly counted here as a CT.
	var cts []map[string]any
	getJSON(t, srv, "/api/cts", &cts)
	if len(cts) != 3 {
		t.Errorf("demo has %d CTs, want 3", len(cts))
	}
	// Local backend fixture (#91 Phase 4): a fake qemu VM under a "local" node.
	if v := byName["laptop-dev"]; v == nil || v["backend"] != "qemu" || v["namespace"] != "local" {
		t.Errorf("demo local VM missing or misshaped: %+v", byName["laptop-dev"])
	}
	var nodes []map[string]any
	getJSON(t, srv, "/api/nodes", &nodes)
	if len(nodes) != 4 { // 3 cluster nodes + the synthetic local node
		t.Errorf("demo has %d nodes, want 4: %+v", len(nodes), nodes)
	}

	// Cluster checks are all green (local checks depend on the CI host —
	// /dev/kvm and installed CLIs — so only their presence is asserted).
	var checks []map[string]any
	getJSON(t, srv, "/api/doctor", &checks)
	names := map[string]bool{}
	for _, c := range checks {
		names[c["name"].(string)] = true
		if local := map[string]bool{
			"QEMU (local backend)": true, "KVM acceleration": true,
			"Tailscale CLI": true, "virtctl CLI": true,
			// VSOCK support depends on the host's socat build, not our
			// code: hosted runners and minimal containers lack it.
			"VSOCK host support":    true,
			"OVMF firmware":         true,
			"swtpm (TPM emulation)": true,
			"socat":                 true,
			"qemu-img":              true,
		}; local[c["name"].(string)] {
			continue
		}
		if c["ok"] != true {
			t.Errorf("doctor check %q not OK in demo: %v", c["name"], c["detail"])
		}
	}
	for _, want := range []string{"KubeVirt installed", "Default StorageClass", "QEMU (local backend)"} {
		if !names[want] {
			t.Errorf("doctor check %q missing", want)
		}
	}

	// Stop is stateful: the VM's derived state flips.
	resp, err := http.Post(srv.URL+"/api/vms/corral-vms/web-prod/stop", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("stop web-prod: %v status=%v", err, resp.StatusCode)
	}
	resp.Body.Close()
	getJSON(t, srv, "/api/vms", &vms)
	for _, v := range vms {
		if v["name"] == "web-prod" && v["running"] != false {
			t.Errorf("web-prod still running after stop: %+v", v)
		}
	}

	// Per-VM live metrics flow through the real metrics-server code path.
	var m map[string]string
	getJSON(t, srv, "/api/vms/corral-vms/db-prod/metrics", &m)
	if m["cpu"] == "" {
		t.Errorf("db-prod live cpu empty: %+v", m)
	}

	// Theme API in demo mode: PUT /api/theme updates in-memory theme and does not write config.yaml
	cfgPath := config.DefaultPath()
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatalf("expected config file %q not to exist before theme update", cfgPath)
	}
	putBody := `{"accent":"#22c55e","brand_title":"SmokeTest"}`
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/theme", strings.NewReader(putBody))
	if err != nil {
		t.Fatalf("NewRequest PUT /api/theme: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	respTheme, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/theme: %v", err)
	}
	_ = respTheme.Body.Close()
	if respTheme.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/theme: status=%v", respTheme.StatusCode)
	}
	var theme ThemeConfig
	getJSON(t, srv, "/api/theme", &theme)
	if theme.Accent != "#22c55e" || theme.BrandTitle != "SmokeTest" {
		t.Errorf("demo theme not updated in memory: %+v", theme)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("demo PUT /api/theme created config file at %s", cfgPath)
	}
}
