package main

import (
	"testing"

	"github.com/tuna-os/corral/pkg/config"
)

// A namespace scopes a KubeVirt VM inside its cluster and means nothing on any
// other backend. The stored reference keeps it only where it applies, so two
// references to the same libvirt domain never compare unequal (#214).
func TestResolveRefKeepsTheNamespaceOnlyWhereItScopes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, c := range []config.ContextConfig{
		{Name: "prod", Backend: "kubevirt", Context: "prod-cluster"},
		{Name: "hv", Backend: "libvirt", Context: "qemu+ssh://hv/system"},
		{Name: "lab", Backend: "incus", Context: "lab"},
	} {
		if err := config.AddContext(c); err != nil {
			t.Fatal(err)
		}
	}

	kube, err := resolveRef("web-1", "prod", "corral-vms")
	if err != nil {
		t.Fatalf("resolveRef on a KubeVirt context: %v", err)
	}
	if kube.Backend != "kubevirt" || kube.Context != "prod-cluster" || kube.Namespace != "corral-vms" || kube.Name != "web-1" {
		t.Errorf("KubeVirt ref = %+v", kube)
	}
	for _, name := range []string{"hv", "lab"} {
		ref, err := resolveRef("web-1", name, "corral-vms")
		if err != nil {
			t.Fatalf("resolveRef on %s: %v", name, err)
		}
		if ref.Namespace != "" {
			t.Errorf("%s ref carries a namespace it has no concept of: %+v", ref.Backend, ref)
		}
	}
}
