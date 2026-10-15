package backend

import (
	"os"

	"github.com/tuna-os/corral/pkg/types"
)

// contextEnv names the variable each backend's package-level helpers read for
// the context to talk to. Adapters bind their context explicitly; these
// helpers predate that and still find it in the environment. A backend absent
// here has no such helpers, so there is nothing to export.
var contextEnv = map[string]string{
	"kubevirt": "CORRAL_KUBE_CONTEXT",
	"incus":    "CORRAL_INCUS_REMOTE",
	"libvirt":  "CORRAL_LIBVIRT_URI",
}

// ActivateContext exports a reference's context into the variable its
// backend's package-level helpers read, so a helper that predates per-client
// contexts still talks to the instance's own cluster, remote, or hypervisor.
func ActivateContext(ref types.InstanceRef) {
	if name, ok := contextEnv[ref.Backend]; ok {
		_ = os.Setenv(name, ref.Context)
	}
}
