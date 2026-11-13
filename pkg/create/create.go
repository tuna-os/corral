package create

import (
	"fmt"

	backendapi "github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/types"
)

func (r request) execute(name string) error {
	if err := r.validateGuestOptions("qemu"); err != nil {
		return err
	}
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
		if err := r.validateGuestOptions("bootc"); err != nil {
			return err
		}
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
	if err := r.validateGuestOptions(backend); err != nil {
		return err
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
		target := types.InstanceRef{Backend: "libvirt", Context: r.createContext(config.LibvirtURI), Namespace: "libvirt", Name: name}
		backendapi.ActivateContext(target)
		if err := r.LibvirtCreate(types.CreateOpts{Name: name, CPU: r.CPU, Mem: r.Mem, Disk: r.Disk, ISO: r.ISO, QCOW: r.QCOW}); err != nil {
			return err
		}
		if r.Registry != nil {
			return r.Registry.SetRef(target, types.RegistryEntry{})
		}
		return nil
	case "qemu":
		if err := r.runQemuCreate(name); err != nil {
			return err
		}
		return r.maybeStartAndWait(name)
	}
	return r.runAdapterCreate(name, backend, r.createContext(func() string {
		if def := config.DefaultContext(); def.Backend == backend {
			return def.Context
		}
		return ""
	}))
}

// Guest selection is supported only by direct local QEMU create; never silently
// drop it when another backend or the bootc builder chooses the guest.
func (r request) validateGuestOptions(backend string) error {
	if err := types.GuestArch(r.Arch).Validate(); err != nil {
		return err
	}
	switch r.Firmware {
	case "", "uefi", "bios":
	default:
		return fmt.Errorf("unsupported firmware %q: use uefi or bios", r.Firmware)
	}
	if r.Arch != "" && backend != "qemu" {
		return fmt.Errorf("--arch is not supported on backend %s; use local QEMU create with a prepared disk or ISO", backend)
	}
	if r.Loopback && backend != "qemu" {
		return fmt.Errorf("--loopback is supported only on local QEMU create")
	}
	return nil
}

// An explicit choice bypasses reachability detection, including an explicit
// false --kubevirt flag (the local bootc path).
func (r request) useKubevirtBootc() bool {
	if !r.KubevirtExplicit {
		return r.Reachable()
	}
	return r.Kubevirt
}
