package web

import (
	"net/http"
	"strings"
	"testing"
)

// Characterization tests for the route table (#503) beyond the per-route
// registration check in server_test.go.

// Cluster-only operations answer the reserved local namespace with one
// uniform 400 before any handler runs.
func TestRoutes_ClusterOnlyOperationsRejectLocal(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	for _, rt := range []struct{ method, path string }{
		{"POST", "/api/vms/local/vm/migrate"},
		{"POST", "/api/vms/local/vm/scale"},
		{"POST", "/api/vms/local/vm/expand"},
		{"POST", "/api/vms/local/vm/clone"},
		{"POST", "/api/vms/local/vm/template"},
		{"POST", "/api/vms/local/vm/tags"},
		{"POST", "/api/vms/local/vm/options"},
		{"POST", "/api/vms/local/vm/bootc/rebuild"},
		{"GET", "/api/vms/local/vm/export"},
		{"POST", "/api/vms/local/vm/volumes"},
		{"DELETE", "/api/vms/local/vm/volumes/data"},
		{"POST", "/api/vms/local/vm/snapschedule"},
		{"DELETE", "/api/vms/local/vm/snapschedule"},
		{"POST", "/api/vms/local/vm/powerschedule"},
		{"DELETE", "/api/vms/local/vm/powerschedule"},
	} {
		req, _ := http.NewRequest(rt.method, fx.Server.URL+rt.path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusBadRequest ||
			body != "{\"error\":\"this operation is not supported for local QEMU VMs\"}\n" {
			t.Errorf("%s %s: %d %q", rt.method, rt.path, resp.StatusCode, body)
		}
	}
	if n := len(fx.Runner.Calls()); n != 0 {
		t.Errorf("rejected local operations shelled out %d times", n)
	}
}

// The index is the only dynamic page: it is served with the active theme
// injected, at both / and /index.html.
func TestRoutes_IndexIsThemed(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	raw, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	want := injectTheme(string(raw))
	for _, path := range []string{"/", "/index.html"} {
		resp := mustGet(t, fx.Server.URL+path)
		body := readBody(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("GET %s: Content-Type %q", path, ct)
		}
		if body != want {
			t.Errorf("GET %s: body is not the theme-injected index", path)
		}
	}

	resp := mustGet(t, fx.Server.URL+"/no-such-asset.js")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown asset: status %d, want 404", resp.StatusCode)
	}
}

// Routes outside /api: the Proxmox compatibility API, and the console
// websockets (which refuse a plain GET without an upgrade).
func TestRoutes_NonRESTSurfacesAreMounted(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	for _, path := range []string{
		"/api2/json/version",
		"/api/vnc/ns/vm",
		"/api/tty/ns/vm",
		"/api/rdp/ns/vm",
	} {
		resp := mustGet(t, fx.Server.URL+path)
		body := readBody(t, resp)
		if strings.Contains(body, "404 page not found") || resp.StatusCode == http.StatusMethodNotAllowed {
			t.Errorf("GET %s not mounted: %d %q", path, resp.StatusCode, body)
		}
	}
}

func TestHandleCreateVM_UnknownTarget(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	resp := mustPost(t, fx.Server.URL+"/api/vms", `{"name":"vm","target":"nope"}`)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || body != "{\"error\":\"unknown context \\\"nope\\\"\"}\n" {
		t.Errorf("unknown target: %d %q", resp.StatusCode, body)
	}
}
