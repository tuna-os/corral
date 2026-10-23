package create

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/proxmoxbe"
	"github.com/tuna-os/corral/pkg/types"
)

type mockStore struct {
	mu      sync.Mutex
	entries map[string]types.RegistryEntry
}

func (m *mockStore) Set(name string, entry types.RegistryEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]types.RegistryEntry)
	}
	m.entries[name] = entry
	return nil
}

func TestLocalBootcScript_UsesTempDirMounts(t *testing.T) {
	script := LocalBootcScript("/dev/loop7", "", nil)

	if !strings.Contains(script, "DISK=/dev/loop7") {
		t.Errorf("script missing DISK=/dev/loop7:\n%s", script)
	}
	if !strings.Contains(script, "MNT_ESP=$(mktemp -d)") {
		t.Errorf("script should use mktemp -d for ESP mount:\n%s", script)
	}
	if !strings.Contains(script, "MNT_ROOT=$(mktemp -d)") {
		t.Errorf("script should use mktemp -d for composefs root mount:\n%s", script)
	}
	if !strings.Contains(script, "MNT=$(mktemp -d)") {
		t.Errorf("script should use mktemp -d for post-install mount check:\n%s", script)
	}

	for _, forbidden := range []string{"/mnt/esp", "/mnt/root", "mkdir -p /mnt", "mount \"${DISK}p3\" /mnt ", "chroot /mnt"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("script contains forbidden hardcoded /mnt path %q (#389):\n%s", forbidden, script)
		}
	}
}

func TestLocalBootcScript_WithProvisionScript(t *testing.T) {
	script := LocalBootcScript("/dev/loop7", "echo test-provision", nil)

	wantChroot := `cat /output/provision.sh | chroot "$MNT" /bin/bash`
	if !strings.Contains(script, wantChroot) {
		t.Errorf("script missing chroot into temp mount %q:\n%s", wantChroot, script)
	}
	if strings.Contains(script, "chroot /mnt") {
		t.Errorf("script contains forbidden chroot /mnt:\n%s", script)
	}
}

func TestLocalBootcScript_Kargs(t *testing.T) {
	script := LocalBootcScript("/dev/loop7", "", []string{"tunaos.countme=0", "console=ttyS0,115200n8", "it's=1"})
	want := `--wipe --karg 'tunaos.countme=0' --karg 'console=ttyS0,115200n8' --karg 'it'\''s=1' --root-ssh-authorized-keys /output/id_rsa.pub "$DISK"`
	if !strings.Contains(script, want) {
		t.Errorf("script missing %q:\n%s", want, script)
	}

	plain := LocalBootcScript("/dev/loop7", "", nil)
	if strings.Contains(plain, "--karg") || !strings.Contains(plain, `--wipe --root-ssh-authorized-keys`) {
		t.Errorf("script without kargs must keep the plain install line:\n%s", plain)
	}
}

func TestService_KargsValidation(t *testing.T) {
	svc := NewService(nil)

	err := svc.Execute(Options{
		Name:  "vm",
		Kargs: []string{"tunaos.countme=0"},
		Bootc: "",
	})
	if err == nil || !strings.Contains(err.Error(), "--karg needs --bootc") {
		t.Errorf("--karg without --bootc: err = %v", err)
	}

	err = svc.Execute(Options{
		Name:  "vm",
		Kargs: []string{"two words"},
		Bootc: "ghcr.io/tuna-os/yellowfin:gnome",
	})
	if err == nil || !strings.Contains(err.Error(), "not one kernel argument") {
		t.Errorf("whitespace karg: err = %v", err)
	}
}

func TestService_RefusesBackendWithoutCreator(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CORRAL_DEFAULT_BACKEND", "")
	if err := config.AddContext(config.ContextConfig{Name: "vc", Backend: "vsphere", Context: "vc.invalid"}); err != nil {
		t.Fatal(err)
	}

	svc := NewService(nil)
	err := svc.Execute(Options{
		Name:    "vm1",
		Context: "vc",
		Force:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "not supported on backend vsphere") {
		t.Fatalf("expected refusal naming vsphere, got: %v", err)
	}
}

func TestService_ProxmoxCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CORRAL_DEFAULT_BACKEND", "")

	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api2/json")
		mu.Lock()
		requests = append(requests, r.Method+" "+path)
		mu.Unlock()
		var data any
		switch {
		case path == "/cluster/resources":
			data = []any{}
		case path == "/cluster/nextid":
			data = "101"
		case path == "/nodes":
			data = []map[string]any{{"node": "pve1", "status": "online"}}
		case strings.HasSuffix(path, "/status"):
			data = map[string]any{"status": "stopped", "exitstatus": "OK"}
		default:
			data = "UPID:pve1:00001234:0000ABCD:65000000:qmcreate:101:corral@pve!ci:"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()

	if err := config.AddContext(config.ContextConfig{Name: "pve", Backend: "proxmox", Context: server.URL}); err != nil {
		t.Fatal(err)
	}
	client, err := proxmoxbe.New(proxmoxbe.Config{Host: server.URL, Token: "corral@pve!ci=00000000-0000-0000-0000-000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	proxmoxbe.SetClientForContext("pve", client)
	defer proxmoxbe.ResetClients()

	svc := NewService(&mockStore{})
	err = svc.Execute(Options{
		Name:    "vm1",
		Context: "pve",
		ISO:     "local:iso/debian.iso",
		Force:   true,
	})
	if err != nil {
		t.Fatalf("proxmox create failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	posted := false
	for _, r := range requests {
		if r == "POST /nodes/pve1/qemu" {
			posted = true
		}
	}
	if !posted {
		t.Errorf("create never reached PVE; requests were %v", requests)
	}
}

func TestService_AlreadyExists(t *testing.T) {
	svc := NewService(nil)
	svc.ResolveBackendFunc = func(name string) string {
		if name == "existing-vm" {
			return "qemu"
		}
		return ""
	}

	err := svc.Execute(Options{
		Name: "existing-vm",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists (backend: qemu)") {
		t.Errorf("expected already exists error, got: %v", err)
	}

	// With Force: true, should proceed past check
	err = svc.Execute(Options{
		Name:    "existing-vm",
		Force:   true,
		Backend: "nonexistent-backend",
	})
	if err != nil && strings.Contains(err.Error(), "already exists") {
		t.Errorf("with force, should not complain about existing vm, got: %v", err)
	}
}
