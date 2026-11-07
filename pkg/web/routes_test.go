package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Characterization tests for the route table (#503) beyond the per-route
// registration check in server_test.go.

// TestRoutes_PatternTable pins the complete route table: every pattern below
// must be registered and must be the one a matching request resolves to, so a
// dropped route or a new pattern shadowing an existing one fails here.
func TestRoutes_PatternTable(t *testing.T) {
	mux, err := routes(false, nil)
	if err != nil {
		t.Fatal(err)
	}
	patterns := []string{
		"/",
		"/api2/json/",
		"GET /metrics",
		"GET /healthz",
		"GET /readyz",
		"GET /api/whoami",
		"GET /api/theme",
		"PUT /api/theme",
		"GET /api/vms",
		"GET /api/v1/inventory",
		"GET /api/v1/meta",
		"GET /api/contexts",
		"POST /api/vms",
		"GET /api/cts",
		"POST /api/cts",
		"POST /api/cts/{ns}/{name}/{action}",
		"DELETE /api/cts/{ns}/{name}",
		"PUT /api/cts/{ns}/{name}/scale",
		"GET /api/nodes",
		"GET /api/nodes/{name}/metrics/history",
		"GET /api/metrics/history",
		"GET /api/metrics/top",
		"GET /api/hostpower",
		"POST /api/hostpower/{plugin}/{action}",
		"GET /api/capabilities",
		"GET /api/images",
		"GET /api/sources",
		"POST /api/sources",
		"DELETE /api/sources/{name}",
		"GET /api/instancetypes",
		"GET /api/nads",
		"GET /api/gpus",
		"GET /api/vms/{ns}/{name}/gpus",
		"POST /api/vms/{ns}/{name}/gpus",
		"DELETE /api/vms/{ns}/{name}/gpus/{gpu}",
		"GET /api/doctor",
		"POST /api/doctor/fix",
		"GET /api/plugins",
		"POST /api/plugins/{name}/install",
		"DELETE /api/plugins/{name}",
		"GET /api/plugins/ui",
		"GET /api/plugins/{plugin}/ui/{id}",
		"POST /api/vms/{ns}/{name}/nics",
		"GET /api/datavolumes",
		"POST /api/datavolumes",
		"POST /api/datavolumes/upload",
		"DELETE /api/datavolumes/{ns}/{name}",
		"GET /api/tasks/{id}",
		"GET /api/tasklog",
		"GET /api/folders",
		"POST /api/folders",
		"DELETE /api/folders",
		"POST /api/folders/move",
		"POST /api/folders/members",
		"DELETE /api/folders/members",
		"POST /api/folders/action",
		"POST /api/move/preflight",
		"POST /api/move",
		"GET /api/move/destinations",
		"GET /api/vms/{ns}/{name}",
		"POST /api/vms/{ns}/{name}/{action}",
		"DELETE /api/vms/{ns}/{name}",
		"POST /api/vms/{ns}/{name}/migrate",
		"POST /api/vms/{ns}/{name}/scale",
		"POST /api/vms/{ns}/{name}/expand",
		"POST /api/vms/{ns}/{name}/clone",
		"POST /api/vms/{ns}/{name}/template",
		"POST /api/vms/{ns}/{name}/tags",
		"POST /api/vms/{ns}/{name}/options",
		"GET /api/vms/{ns}/{name}/guestinfo",
		"GET /api/vms/{ns}/{name}/rdp",
		"POST /api/vms/{ns}/{name}/bootc/rebuild",
		"GET /api/vms/{ns}/{name}/events",
		"GET /api/vms/{ns}/{name}/metrics",
		"GET /api/vms/{ns}/{name}/metrics/history",
		"GET /api/vms/{ns}/{name}/export",
		"POST /api/vms/{ns}/{name}/volumes",
		"DELETE /api/vms/{ns}/{name}/volumes/{vol}",
		"GET /api/vms/{ns}/{name}/snapschedule",
		"POST /api/vms/{ns}/{name}/snapschedule",
		"DELETE /api/vms/{ns}/{name}/snapschedule",
		"GET /api/vms/{ns}/{name}/powerschedule",
		"POST /api/vms/{ns}/{name}/powerschedule",
		"DELETE /api/vms/{ns}/{name}/powerschedule",
		"GET /api/vms/{ns}/{name}/snapshots",
		"POST /api/vms/{ns}/{name}/snapshots",
		"DELETE /api/vms/{ns}/{name}/snapshots/{snap}",
		"POST /api/vms/{ns}/{name}/snapshots/{snap}/restore",
		"GET /api/vnc/{ns}/{name}",
		"GET /api/tty/{ns}/{name}",
		"GET /api/rdp/{ns}/{name}",
	}
	wildcard := regexp.MustCompile(`\{[a-z]+\}`)
	for _, pattern := range patterns {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			method, path = http.MethodGet, pattern
		}
		req := httptest.NewRequest(method, wildcard.ReplaceAllString(path, "x"), nil)
		if _, got := mux.Handler(req); got != pattern {
			t.Errorf("%s %s resolves to %q, want %q", method, req.URL.Path, got, pattern)
		}
	}
}

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
