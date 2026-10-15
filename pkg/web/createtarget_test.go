package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tuna-os/corral/pkg/config"
)

// Characterization tests for create-target resolution (#503). The routing
// cases for "", "cluster" and "local" are in backend_selection_test.go.

// withContexts gives the test a private config holding exactly ctxs (plus the
// built-in local context) and no kubeconfig-derived legacy cluster.
func withContexts(t *testing.T, ctxs ...config.ContextConfig) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KUBECONFIG", "")
	t.Setenv("CORRAL_KUBE_CONTEXT", "")
	t.Setenv("CORRAL_INCUS_REMOTE", "")
	t.Setenv("CORRAL_LIBVIRT_URI", "")
	t.Setenv("CORRAL_DEFAULT_BACKEND", "")
	for _, c := range ctxs {
		if err := config.AddContext(c); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNeedsCluster(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  createRequest
		want bool
	}{
		{"empty", createRequest{}, false},
		{"iso", createRequest{ISO: "https://example/x.iso"}, false},
		{"import", createRequest{Import: "https://example/x.qcow2"}, false},
		{"sizing only", createRequest{CPU: 4, Mem: "8Gi", Disk: "40Gi", Firmware: "uefi", UEFI: true}, false},
		{"cloud-init and ssh", createRequest{CloudInit: "#cloud-config", SSHKey: "ssh-ed25519 AAAA"}, false},
		{"instancetype", createRequest{InstanceType: "u1.small", Preference: "fedora"}, false},
		{"storage class and node", createRequest{StorageClass: "fast", Node: "n1"}, false},
		{"containerDisk", createRequest{ContainerDisk: "quay.io/x"}, true},
		{"pvc", createRequest{PVC: "golden"}, true},
		{"bootc", createRequest{Bootc: "ghcr.io/x"}, true},
		{"image", createRequest{Image: "fedora"}, true},
		{"windows", createRequest{Windows: true}, true},
	} {
		if got := needsCluster(tc.req); got != tc.want {
			t.Errorf("%s: needsCluster = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveCreateTarget_NamedContexts(t *testing.T) {
	lab := config.ContextConfig{Name: "lab", Backend: "kubevirt", Context: "lab-ctx"}
	hv := config.ContextConfig{Name: "hv", Backend: "libvirt", Context: "qemu+ssh://hv/system"}
	withContexts(t, lab, hv)

	for name, want := range map[string]config.ContextConfig{"lab": lab, "hv": hv} {
		got, err := resolveCreateTarget(name, createRequest{Name: "vm", ContainerDisk: "quay.io/x"})
		if err != nil {
			t.Fatalf("resolveCreateTarget(%q): %v", name, err)
		}
		if got.Name != want.Name || got.Backend != want.Backend || got.Context != want.Context {
			t.Errorf("resolveCreateTarget(%q) = %+v, want %+v", name, got, want)
		}
	}

	// "local" never consults the config, whatever the request carries.
	got, err := resolveCreateTarget("local", createRequest{Bootc: "ghcr.io/x"})
	if err != nil || got != (config.ContextConfig{Name: "local", Backend: "qemu"}) {
		t.Errorf("resolveCreateTarget(local) = %+v, %v", got, err)
	}

	if _, err := resolveCreateTarget("nope", createRequest{}); err == nil || err.Error() != `unknown context "nope"` {
		t.Errorf("unknown context error = %v", err)
	}
}

func TestClusterContext(t *testing.T) {
	t.Run("first kubevirt context", func(t *testing.T) {
		withContexts(t,
			config.ContextConfig{Name: "hv", Backend: "libvirt", Context: "qemu:///system"},
			config.ContextConfig{Name: "lab", Backend: "kubevirt", Context: "lab-ctx"},
			config.ContextConfig{Name: "prod", Backend: "kubevirt", Context: "prod-ctx"},
		)
		got, err := clusterContext()
		if err != nil || got.Name != "lab" {
			t.Errorf("clusterContext = %+v, %v; want lab", got, err)
		}
	})
	t.Run("prefers the context named kubevirt", func(t *testing.T) {
		withContexts(t,
			config.ContextConfig{Name: "lab", Backend: "kubevirt", Context: "lab-ctx"},
			config.ContextConfig{Name: "kubevirt", Backend: "kubevirt", Context: "main-ctx"},
		)
		got, err := clusterContext()
		if err != nil || got.Name != "kubevirt" {
			t.Errorf("clusterContext = %+v, %v; want kubevirt", got, err)
		}
	})
	t.Run("a context named kubevirt on another backend is skipped", func(t *testing.T) {
		withContexts(t,
			config.ContextConfig{Name: "kubevirt", Backend: "incus", Context: "remote"},
			config.ContextConfig{Name: "lab", Backend: "kubevirt", Context: "lab-ctx"},
		)
		got, err := clusterContext()
		if err != nil || got.Name != "lab" {
			t.Errorf("clusterContext = %+v, %v; want lab", got, err)
		}
	})
	t.Run("none configured", func(t *testing.T) {
		withContexts(t, config.ContextConfig{Name: "hv", Backend: "libvirt", Context: "qemu:///system"})
		if got, err := clusterContext(); err == nil {
			t.Errorf("clusterContext = %+v, want an error", got)
		}
	})
}

func TestHandleContexts(t *testing.T) {
	withContexts(t, config.ContextConfig{Name: "lab", Backend: "kubevirt", Context: "lab-ctx"})
	fx := NewTestFixture()
	defer fx.Close()

	resp := mustGet(t, fx.Server.URL+"/api/contexts")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Default  string                 `json:"default"`
		Contexts []config.ContextConfig `json:"contexts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Default != "local" {
		t.Errorf("default = %q, want local", body.Default)
	}
	if len(body.Contexts) != 2 || body.Contexts[0].Name != "local" || body.Contexts[1].Name != "lab" {
		t.Errorf("contexts = %+v, want [local lab]", body.Contexts)
	}
}
