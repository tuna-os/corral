package main

import (
	"testing"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/kubevirt"
)

// backupRef keeps the namespace only on KubeVirt, where it scopes the VM, and
// falls back to Corral's default namespace when --namespace is not given (#214).
func TestBackupRefKeepsTheNamespaceOnlyWhereItScopes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	for _, c := range []config.ContextConfig{
		{Name: "prod", Backend: "kubevirt", Context: "prod-cluster"},
		{Name: "lab", Backend: "incus", Context: "lab"},
	} {
		if err := config.AddContext(c); err != nil {
			t.Fatal(err)
		}
	}
	oldContext, oldNamespace := flagContext, flagNamespace
	t.Cleanup(func() { flagContext, flagNamespace = oldContext, oldNamespace })

	flagContext, flagNamespace = "prod", ""
	ref, err := backupRef("web-1")
	if err != nil {
		t.Fatalf("backupRef on a KubeVirt context: %v", err)
	}
	if ref.Backend != "kubevirt" || ref.Context != "prod-cluster" || ref.Namespace != kubevirt.DefaultNamespace {
		t.Errorf("KubeVirt ref = %+v, want namespace %q", ref, kubevirt.DefaultNamespace)
	}

	flagNamespace = "team-a"
	if ref, _ := backupRef("web-1"); ref.Namespace != "team-a" {
		t.Errorf("--namespace was not carried onto the KubeVirt ref: %+v", ref)
	}

	flagContext = "lab"
	ref, err = backupRef("web-1")
	if err != nil {
		t.Fatalf("backupRef on an Incus context: %v", err)
	}
	if ref.Backend != "incus" || ref.Namespace != "" {
		t.Errorf("Incus ref = %+v, want no namespace", ref)
	}

	flagContext = "nope"
	if _, err := backupRef("web-1"); err == nil {
		t.Error("an unknown context must be an error")
	}
}
