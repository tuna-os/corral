package cmd

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

// `corral clone` reaches the source VM's own Cloner: on KubeVirt that applies
// a VirtualMachineClone in the source's namespace, and the clone is registered
// beside it (#214).
func TestCloneCLIUsesTheSourceBackendsCloner(t *testing.T) {
	fake := kubevirtVMScratch(t, "web", types.RegistryEntry{Backend: "kubevirt", Context: "prod", Namespace: "team-a"})
	old := cloneNamespace
	cloneNamespace = ""
	t.Cleanup(func() { cloneNamespace = old })

	captureStdout(t, func() {
		if err := cloneCmd.RunE(cloneCmd, []string{"web", "web-copy"}); err != nil {
			t.Errorf("clone on a KubeVirt VM: %v", err)
		}
	})
	if !ranCommandLine(fake, "kubectl apply -f -") {
		t.Errorf("clone did not apply a VirtualMachineClone; calls were %v", fake.Calls())
	}
	entry, ok := registryStore.Get("web-copy")
	if !ok || entry.Backend != "kubevirt" || entry.Context != "prod" || entry.Namespace != "team-a" {
		t.Errorf("registry entry for the clone = %+v (found %v), want kubevirt/prod/team-a", entry, ok)
	}
}

// A backend without a Cloner refuses by name instead of reaching KubeVirt.
func TestCloneCLIRefusesABackendWithoutACloner(t *testing.T) {
	fake := remoteVMScratch(t, "web", types.RegistryEntry{Backend: "incus", Context: "lab"})
	err := cloneCmd.RunE(cloneCmd, []string{"web", "web-copy"})
	if err == nil || !strings.Contains(err.Error(), "incus backend does not support clone") {
		t.Fatalf("clone on an Incus VM = %v, want a refusal naming incus", err)
	}
	if len(fake.Calls()) != 0 && ranCommandLine(fake, "kubectl") {
		t.Errorf("an Incus clone reached kubectl: %v", fake.Calls())
	}
}
