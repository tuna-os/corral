package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// victimDir makes a directory outside the local VM home that a traversal
// name could reach, and returns it.
func victimDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	victim := filepath.Join(home, ".local", "share", "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return victim
}

// TestCreateLocalVM_RefusesTraversalName: a failed local create removes
// VMHome/<name>, so a name with "../" used to delete a directory outside the
// VM home. The request must fail with 400 and touch nothing.
func TestCreateLocalVM_RefusesTraversalName(t *testing.T) {
	victim := victimDir(t)
	fx := NewTestFixture()
	defer fx.Close()

	for _, body := range []string{
		`{"name":"../../victim","target":"local","iso":"/nonexistent.iso"}`,
		`{"name":"../../victim","target":"local","iso":"http://127.0.0.1:1/never.iso"}`,
	} {
		resp, err := http.Post(fx.Server.URL+"/api/vms", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, resp.StatusCode)
		}
	}
	if _, err := os.Stat(filepath.Join(victim, "keep")); err != nil {
		t.Fatalf("directory outside the VM home was removed: %v", err)
	}
}

// TestCreateLocalVM_RefusesExistingName: the failure cleanup must not
// remove a VM that this request did not create.
func TestCreateLocalVM_RefusesExistingName(t *testing.T) {
	fakeLocalVM(t, "existingvm")
	fx := NewTestFixture()
	defer fx.Close()

	resp, err := http.Post(fx.Server.URL+"/api/vms", "application/json",
		strings.NewReader(`{"name":"existingvm","target":"local","iso":"/nonexistent.iso"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status %d, want 409", resp.StatusCode)
	}
	home, _ := os.UserHomeDir()
	meta := filepath.Join(home, ".local", "share", "corral", "vms", "existingvm", "metadata.json")
	if _, err := os.Stat(meta); err != nil {
		t.Fatalf("existing VM was removed by a failed create: %v", err)
	}
}

// TestLocalVM_DeleteAndActionsRefuseTraversalName: an encoded "../" in the
// {name} path segment must not reach qemu.Delete's RemoveAll.
func TestLocalVM_DeleteAndActionsRefuseTraversalName(t *testing.T) {
	victim := victimDir(t)
	fx := NewTestFixture()
	defer fx.Close()

	req, err := http.NewRequest(http.MethodDelete, fx.Server.URL+"/api/vms/local/..%2F..%2Fvictim", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("delete: status %d, want 400", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(victim, "keep")); err != nil {
		t.Fatalf("delete removed a directory outside the VM home: %v", err)
	}

	for _, action := range []string{"start", "stop", "restart"} {
		resp, err := http.Post(fx.Server.URL+"/api/vms/local/..%2F..%2Fvictim/"+action, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", action, resp.StatusCode)
		}
	}
}
