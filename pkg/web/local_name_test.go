package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/qemu"
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

// TestLocalVM_ReadRoutesRefuseTraversalName: info and snapshots for the
// local namespace must not read (or run qemu-img on) a directory outside
// the VM home. Before the check, info served victim/metadata.json.
func TestLocalVM_ReadRoutesRefuseTraversalName(t *testing.T) {
	victim := victimDir(t)
	if err := os.WriteFile(filepath.Join(victim, "metadata.json"), []byte(`{"secret":"leaked"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fx := NewTestFixture()
	defer fx.Close()

	for _, path := range []string{
		"/api/vms/local/..%2F..%2Fvictim",
		"/api/vms/local/..%2F..%2Fvictim/snapshots",
	} {
		resp, err := http.Get(fx.Server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", path, resp.StatusCode)
		}
	}
	resp, err := http.Post(fx.Server.URL+"/api/vms/local/..%2F..%2Fvictim/snapshots", "application/json",
		strings.NewReader(`{"name":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST snapshots: status %d, want 400", resp.StatusCode)
	}
}

// TestCreateLocalVM_Succeeds (#501): RecordCreating makes VMHome/<name>
// for the in-progress listing, and qemu.Create used to refuse that same
// directory as an existing VM, so every web-created local VM failed.
func TestCreateLocalVM_Succeeds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	shims := map[string]string{
		"qemu-system-x86_64": "#!/bin/sh\nexit 0\n",
		// create -f qcow2 <path> <size>: make the disk so later stats pass.
		"qemu-img": "#!/bin/sh\n[ \"$1\" = create ] && : > \"$4\"\nexit 0\n",
	}
	for name, script := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	qemu.SetSystemctl(func(args ...string) ([]byte, error) { return nil, nil })
	t.Cleanup(func() { qemu.SetSystemctl(nil) })
	iso := filepath.Join(t.TempDir(), "install.iso")
	if err := os.WriteFile(iso, []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}

	fx := NewTestFixture()
	defer fx.Close()
	resp, err := http.Post(fx.Server.URL+"/api/vms", "application/json",
		strings.NewReader(`{"name":"webvm","target":"local","iso":"`+iso+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d (%v), want 201", resp.StatusCode, body)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".local", "share", "corral", "vms", "webvm", "metadata.json"))
	if err != nil {
		t.Fatalf("created VM has no metadata: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["status"] == "Creating" || meta["iso"] != iso {
		t.Errorf("metadata is still the placeholder or lost the ISO: %s", raw)
	}
}
