package create

import (
	"github.com/tuna-os/corral/pkg/types"
)

func (r request) runQemuCreate(name string) error {
	useUEFI := r.Firmware == "uefi"
	vsock := r.Vsock || r.VsockCID != 0
	if r.VsockCID != 0 {
		vsock = true
	}
	if err := r.QEMUCreate(types.CreateOpts{
		Name:     name,
		Mem:      r.Mem,
		CPU:      r.CPU,
		Disk:     r.Disk,
		ISO:      r.ISO,
		QCOW:     r.QCOW,
		Force:    r.Force,
		UEFI:     useUEFI,
		Vsock:    vsock,
		VsockCID: r.VsockCID,
		TPM:      r.TPM,
	}); err != nil {
		return err
	}
	if r.Registry != nil {
		_ = r.Registry.Set(name, types.RegistryEntry{Backend: "qemu"})
	}
	return nil
}
