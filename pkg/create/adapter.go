package create

import (
	"fmt"

	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/types"
)

// createContext returns the backend target rather than its configuration alias.
// Evaluate the backend default only when no explicit context was selected.
func (r request) createContext(defaultContext func() string) string {
	if r.Context != "" {
		if target, ok := config.FindContext(r.Context); ok {
			return target.Context
		}
		return r.Context
	}
	return defaultContext()
}

// runAdapterCreate creates on a backend the dedicated paths above do not
// cover, through its pkg/backend Creator. A backend without one is refused by
// name: falling through to local QEMU made a VM on the workstation, not where
// the operator asked, and reported success (#493).
func (r request) runAdapterCreate(name, backendName, context string) error {
	adapter, err := r.BackendFor(types.InstanceRef{Backend: backendName, Context: context, Name: name})
	if err != nil {
		return fmt.Errorf("create is not supported on backend %q from the CLI: %w", backendName, err)
	}
	creator, ok := adapter.(backend.Creator)
	if !ok {
		return fmt.Errorf("create is not supported on backend %s from the CLI (see docs/backend-support.md)", backendName)
	}
	if r.Image != "" {
		return fmt.Errorf("--image names a KubeVirt or Incus image; it is not supported on backend %s", backendName)
	}
	if r.WaitSSH {
		return fmt.Errorf("--wait-ssh is supported for QEMU and KubeVirt VMs, not backend %s", backendName)
	}
	if err := creator.Create(types.CreateOpts{
		Name: name, Backend: backendName,
		CPU: r.CPU, Mem: r.Mem, Disk: r.Disk,
		ISO: r.ISO, QCOW: r.QCOW, ContainerDisk: r.ContainerDisk,
		ImportURL: r.Import, PVC: r.PVC,
		UEFI: r.Firmware == "uefi",
	}); err != nil {
		return err
	}
	if r.Registry != nil {
		if err := r.Registry.SetRef(types.InstanceRef{Backend: backendName, Context: context, Name: name}, types.RegistryEntry{}); err != nil {
			return err
		}
	}
	if !r.Start {
		return nil
	}
	power, ok := adapter.(backend.Power)
	if !ok {
		return fmt.Errorf("created %s, but backend %s cannot start it", name, backendName)
	}
	return power.Start(name)
}
