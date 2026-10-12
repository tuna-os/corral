package web

// Supervisor probe contract for deployment mode 4 (24/7 vs on-demand).
//
// A supervisor (systemd, Kubernetes, a container runtime) may only trust two
// facts about corral web: GET /healthz answers 200 with "ok" when the process
// is alive, and GET /readyz answers 200 with {"status":"ready"} when the
// registry store is writable. Anything else is not a signal. This test polls
// both endpoints the way a supervisor does instead of assuming boot order.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/registry"
)

func pollFor200(t *testing.T, url, wantBody string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for {
		resp, err := http.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && string(body) == wantBody {
				return
			}
		}
		if time.Now().After(end) {
			t.Fatalf("GET %s never returned 200 %q before deadline", url, wantBody)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSupervisorProbeContract(t *testing.T) {
	origStore := store
	defer func() { store = origStore }()

	// Ready case: a writable store behind the mux.
	store = registry.NewStoreAt(filepath.Join(t.TempDir(), "registry.json"))
	mux, err := newMux()
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pollFor200(t, srv.URL+"/healthz", "ok\n", 10*time.Second)

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200", resp.StatusCode)
	}
	var ready struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ready); err != nil {
		t.Fatalf("decode /readyz: %v", err)
	}
	if ready.Status != "ready" {
		t.Errorf("/readyz status = %q, want ready", ready.Status)
	}

	// Unready case: no store yet. The supervisor must see 503 with an
	// error field, not a hang or a 200.
	store = nil
	resp2, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz (nil store): %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz (nil store) = %d, want 503", resp2.StatusCode)
	}
	var unready map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&unready); err != nil {
		t.Fatalf("decode /readyz (nil store): %v", err)
	}
	if _, ok := unready["error"]; !ok {
		t.Errorf("/readyz (nil store) has no error field: %v", unready)
	}
}
