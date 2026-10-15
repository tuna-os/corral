package cmd

import (
	"os"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

// activateVMContext exports an instance's context into the variable its
// backend's package-level helpers read, so a helper that predates per-client
// contexts still talks to the right cluster, remote, or hypervisor (#214).
func TestActivateVMContextExportsTheBackendsVariable(t *testing.T) {
	vars := []string{"CORRAL_KUBE_CONTEXT", "CORRAL_INCUS_REMOTE", "CORRAL_LIBVIRT_URI"}
	cases := []struct {
		vm   types.VM
		want map[string]string
	}{
		{types.VM{Backend: "kubevirt", Context: "prod"}, map[string]string{"CORRAL_KUBE_CONTEXT": "prod"}},
		{types.VM{Backend: "incus", Context: "lab"}, map[string]string{"CORRAL_INCUS_REMOTE": "lab"}},
		{types.VM{Backend: "libvirt", Context: "qemu+ssh://hv/system"}, map[string]string{"CORRAL_LIBVIRT_URI": "qemu+ssh://hv/system"}},
		{types.VM{Backend: "qemu", Context: "local"}, map[string]string{}},
		{types.VM{Backend: "proxmox", Context: "pve"}, map[string]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.vm.Backend, func(t *testing.T) {
			for _, v := range vars {
				t.Setenv(v, "unchanged")
			}
			activateVMContext(tc.vm)
			for _, v := range vars {
				want, set := tc.want[v]
				if !set {
					want = "unchanged"
				}
				if got := os.Getenv(v); got != want {
					t.Errorf("%s = %q, want %q", v, got, want)
				}
			}
		})
	}
}
