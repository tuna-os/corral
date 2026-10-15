package backend

import (
	"os"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

func TestActivateContextSetsOnlyTheBackendsVariable(t *testing.T) {
	vars := []string{"CORRAL_KUBE_CONTEXT", "CORRAL_INCUS_REMOTE", "CORRAL_LIBVIRT_URI"}
	cases := []struct {
		ref  types.InstanceRef
		want string // the variable that changes, or "" for none
	}{
		{types.InstanceRef{Backend: "kubevirt", Context: "prod"}, "CORRAL_KUBE_CONTEXT"},
		{types.InstanceRef{Backend: "incus", Context: "prod"}, "CORRAL_INCUS_REMOTE"},
		{types.InstanceRef{Backend: "libvirt", Context: "prod"}, "CORRAL_LIBVIRT_URI"},
		{types.InstanceRef{Backend: "qemu", Context: "prod"}, ""},
		{types.InstanceRef{Backend: "vsphere", Context: "prod"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.ref.Backend, func(t *testing.T) {
			for _, v := range vars {
				t.Setenv(v, "unchanged")
			}
			ActivateContext(tc.ref)
			for _, v := range vars {
				want := "unchanged"
				if v == tc.want {
					want = "prod"
				}
				if got := os.Getenv(v); got != want {
					t.Errorf("%s = %q, want %q", v, got, want)
				}
			}
		})
	}
}
