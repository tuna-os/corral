package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The web create-failure cleanup must never remove what the failed create
// could not have owned: with a reserved name ("locks", the home-level
// lock namespace) Create refuses, so the cleanup must skip removal. Old
// code RemoveAll'd unconditionally, deleting every live flock on the
// failed create.
func TestCreateLocalVM_LocksCleanupPreservesLocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	vms := filepath.Join(home, ".local", "share", "corral", "vms")
	locks := filepath.Join(vms, "locks")
	if err := os.MkdirAll(locks, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(locks, "gate.lock")
	if err := os.WriteFile(sentinel, []byte("held"), 0o644); err != nil {
		t.Fatal(err)
	}

	fx := NewTestFixture()
	defer fx.Close()
	resp, err := http.Post(fx.Server.URL+"/api/vms", "application/json",
		strings.NewReader(`{"name":"locks","target":"local","import":"/nonexistent/disk.qcow2"}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body["error"], "reserved") {
		t.Errorf("want 500 naming the reservation, got %d %q", resp.StatusCode, body["error"])
	}
	if got, rerr := os.ReadFile(sentinel); rerr != nil || string(got) != "held" {
		t.Errorf("failed web create must preserve the locks namespace (read %q, %v)", got, rerr)
	}
}
