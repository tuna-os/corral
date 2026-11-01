package create

import (
	"fmt"

	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/types"
)

// createContext names the context a create on backend lands in: the --context
// flag, else the default context when it belongs to that backend.
func (r request) createContext(backend string) string {
	if r.Context != "" {
		return r.Context
	}
	if def := config.DefaultContext(); def.Backend == backend {
		return def.Name
	}
	return ""
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
		if err := r.Registry.Set(name, types.RegistryEntry{Backend: backendName, Context: context}); err != nil {
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
