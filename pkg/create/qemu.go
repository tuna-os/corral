package create

import (
	"runtime"

	"github.com/tuna-os/corral/pkg/types"
)

// qemuUsesUEFI resolves --firmware for a local QEMU guest. Unset firmware
// means UEFI for an arm64 guest, which has no BIOS, and keeps BIOS for amd64.
// An empty arch is the host architecture, as QEMU create resolves it.
func qemuUsesUEFI(firmware, arch, hostArch string) bool {
	if firmware != "" {
		return firmware == "uefi"
	}
	if arch == "" {
		arch = hostArch
	}
	return types.GuestArch(arch) == types.ArchARM64
}

func (r request) runQemuCreate(name string) error {
	if err := r.validateGuestOptions("qemu"); err != nil {
		return err
	}
	useUEFI := qemuUsesUEFI(r.Firmware, r.Arch, runtime.GOARCH)
	vsock := r.Vsock || r.VsockCID != 0
	if r.VsockCID != 0 {
		vsock = true
	}
	if err := r.QEMUCreate(types.CreateOpts{
		Name:     name,
		Arch:     types.GuestArch(r.Arch),
		Loopback: r.Loopback,
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
		return r.Registry.SetRef(types.InstanceRef{Backend: "qemu", Namespace: "local", Name: name}, types.RegistryEntry{})
	}
	return nil
}
