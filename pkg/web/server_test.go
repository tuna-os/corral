package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStaticServed verifies the embedded SPA (index.html + assets) is served.
func TestStaticServed(t *testing.T) {
	mux, err := newMux()
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/", "/app.js", "/icons.js", "/style.css"} {
		r, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, r.StatusCode)
		}
	}
}

// TestAllRoutesRegistered hits every API route with its method and asserts the
// route is wired (no 404 / 405). Handlers that shell out to kubectl will fail
// without a cluster (5xx), which is fine — we're verifying the surface exists,
// so the kind of "feature silently missing" regression can't slip through.
func TestAllRoutesRegistered(t *testing.T) {
	mux, err := newMux()
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	routes := []struct{ method, path string }{
		{"GET", "/api/vms"},
		{"POST", "/api/vms"},
		{"GET", "/api/nodes"},
		{"GET", "/api/capabilities"},
		{"GET", "/api/instancetypes"},
		{"GET", "/api/nads"},
		{"GET", "/api/datavolumes"},
		{"POST", "/api/datavolumes"},
		{"DELETE", "/api/datavolumes/ns/name"},
		{"GET", "/api/tasks/abc"},
		{"GET", "/api/vms/ns/name"},
		{"DELETE", "/api/vms/ns/name"},
		{"POST", "/api/vms/ns/name/start"},
		{"POST", "/api/vms/ns/name/scale"},
		{"POST", "/api/vms/ns/name/expand"},
		{"POST", "/api/vms/ns/name/clone"},
		{"POST", "/api/vms/ns/name/template"},
		{"POST", "/api/vms/ns/name/nics"},
		{"GET", "/api/vms/ns/name/guestinfo"},
		{"GET", "/api/vms/ns/name/events"},
		{"GET", "/api/vms/ns/name/metrics"},
		{"GET", "/api/vms/ns/name/export"},
		{"POST", "/api/vms/ns/name/volumes"},
		{"DELETE", "/api/vms/ns/name/volumes/vol"},
		{"GET", "/api/vms/ns/name/snapshots"},
		{"POST", "/api/vms/ns/name/snapshots"},
		{"DELETE", "/api/vms/ns/name/snapshots/snap"},
		{"POST", "/api/vms/ns/name/snapshots/snap/restore"},
	}
	client := &http.Client{}
	for _, rt := range routes {
		req, _ := http.NewRequest(rt.method, srv.URL+rt.path, strings.NewReader("{}"))
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("%s %s: %v", rt.method, rt.path, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// The Go mux returns a plain-text "404 page not found" for unregistered
		// paths and 405 for unregistered methods; our handlers return JSON. So a
		// JSON 404 (e.g. "VM not found") still means the route IS wired.
		if resp.StatusCode == http.StatusMethodNotAllowed ||
			strings.Contains(string(body), "404 page not found") {
			t.Errorf("%s %s not registered (got %d: %s)", rt.method, rt.path, resp.StatusCode, strings.TrimSpace(string(body)))
		}
	}
}
