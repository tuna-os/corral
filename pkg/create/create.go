package create

import (
	"fmt"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/types"
)

func (r request) execute(name string) error {
	if len(r.Kargs) > 0 && r.Bootc == "" {
		return fmt.Errorf("--karg needs --bootc: only a bootc install puts kernel arguments into the boot entry")
	}
	if err := kubevirt.ValidateKargs(r.Kargs); err != nil {
		return err
	}
	if _, err := r.createPlacement(); err != nil {
		return err
	}
	if existing := r.ResolveBackend(name); existing != "" && !r.Force {
		return fmt.Errorf("VM %q already exists (backend: %s). Use --force to overwrite", name, existing)
	}

	if r.Bootc != "" {
		// Backend auto-selection: honor an explicit KubeVirt choice, otherwise
		// use KubeVirt when a cluster is reachable and fall back to local
		// QEMU. One command, right backend, no flag juggling.
		if r.useKubevirtBootc() {
			return r.runKubevirtBootcCreate(name)
		}
		if err := r.refuseKubevirtOnlyFlags("qemu"); err != nil {
			return err
		}
		if err := r.runLocalBootcCreate(name); err != nil {
			return err
		}
		return r.maybeStartAndWait(name)
	}

	backend := config.DefaultBackend()
	if r.Backend != "" {
		backend = r.Backend
	} else if r.Context != "" {
		if target, ok := config.FindContext(r.Context); ok {
			backend = target.Backend
		}
	}
	if r.Incus {
		backend = "incus"
	}
	if r.Kubevirt {
		backend = "kubevirt"
	}
	// KubeVirt-only source flags remain an explicit signal for backwards compatibility.
	if backend == "qemu" && !r.Incus && (r.Image != "" || r.Import != "" || r.ContainerDisk != "" || r.PVC != "") {
		backend = "kubevirt"
	}
	switch backend {
	case "kubevirt":
		return r.runKubevirtCreate(name)
	}
	// Every other backend: the KubeVirt-only placement flags are refused,
	// not dropped.
	if err := r.refuseKubevirtOnlyFlags(backend); err != nil {
		return err
	}
	switch backend {
	case "incus":
		return r.runIncusCreate(name)
	case "libvirt":
		if err := r.LibvirtCreate(types.CreateOpts{Name: name, CPU: r.CPU, Mem: r.Mem, Disk: r.Disk, ISO: r.ISO, QCOW: r.QCOW}); err != nil {
			return err
		}
		if r.Registry != nil {
			return r.Registry.Set(name, types.RegistryEntry{Backend: "libvirt"})
		}
		return nil
	case "qemu":
		if err := r.runQemuCreate(name); err != nil {
			return err
		}
		return r.maybeStartAndWait(name)
	}
	return r.runAdapterCreate(name, backend, r.createContext(backend))
}

// An explicit choice bypasses reachability detection, including an explicit
// false --kubevirt flag (the local bootc path).
func (r request) useKubevirtBootc() bool {
	if !r.KubevirtExplicit {
		return r.Reachable()
	}
	return r.Kubevirt
}
