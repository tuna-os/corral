package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/proxmoxbe"
	"github.com/tuna-os/corral/pkg/qemu"
)

// createScratch isolates config and VM state, and restores the package-level
// flag variables executeCreate reads.
func createScratch(t *testing.T, contexts ...config.ContextConfig) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CORRAL_DEFAULT_BACKEND", "")
	for _, c := range contexts {
		if err := config.AddContext(c); err != nil {
			t.Fatal(err)
		}
	}
	savedContext, savedBackend, savedForce, savedISO, savedStore := rootContext, rootBackend, createForce, createISO, registryStore
	t.Cleanup(func() {
		rootContext, rootBackend, createForce, createISO, registryStore = savedContext, savedBackend, savedForce, savedISO, savedStore
	})
	// --force skips the "already exists" probe, which would list every
	// configured context over the network.
	createForce, registryStore, rootBackend = true, nil, ""
}

func assertNoLocalVM(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(qemu.VMHome(), name)); err == nil {
		t.Errorf("create made a local QEMU VM %q on the workstation instead of using the requested backend", name)
	}
}

// A backend with no CLI creator must be refused by name. It used to fall
// through to runQemuCreate and report success for a VM on the wrong host (#493).
func TestCreateOnABackendWithoutACreatorIsRefused(t *testing.T) {
	createScratch(t, config.ContextConfig{Name: "vc", Backend: "vsphere", Context: "vc.invalid"})
	rootContext = "vc"

	err := executeCreate("vm1", false)
	if err == nil || !strings.Contains(err.Error(), "not supported on backend vsphere") {
		t.Fatalf("executeCreate on a vsphere context = %v, want a refusal naming vsphere", err)
	}
	assertNoLocalVM(t, "vm1")
}

// `corral create --context <pve>` must create on PVE, through the proxmox
// adapter's Creator (#493).
func TestCreateOnAProxmoxContextCreatesOnPVE(t *testing.T) {
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
	t.Cleanup(server.Close)

	createScratch(t, config.ContextConfig{Name: "pve", Backend: "proxmox", Context: server.URL})
	client, err := proxmoxbe.New(proxmoxbe.Config{Host: server.URL, Token: "corral@pve!ci=00000000-0000-0000-0000-000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	proxmoxbe.SetClientForContext("pve", client)
	t.Cleanup(proxmoxbe.ResetClients)
	rootContext = "pve"
	createISO = "local:iso/debian.iso"

	if err := executeCreate("vm1", false); err != nil {
		t.Fatalf("executeCreate on a proxmox context: %v", err)
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
	assertNoLocalVM(t, "vm1")
}
