package web

import (
	"fmt"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/types"
)

// createWindows runs the Windows guided-install flow synchronously:
// UEFI+TPM+Hyper-V tuned VM with the installer ISO + virtio-win drivers.
func createWindows(req createRequest, ns string) error {
	if req.ISO == "" {
		return badRequest(fmt.Errorf("a Windows installer ISO URL is required"))
	}
	done := taskBegin("create windows", ns+"/"+req.Name)
	if err := kubevirt.CreateWindowsVM(req.Name, ns, req.ISO, req.Disk, req.Mem, req.CPU); err != nil {
		done(err)
		return err
	}
	done(nil)
	if store != nil {
		store.Set(req.Name, types.RegistryEntry{Backend: "kubevirt", Namespace: ns})
	}
	return nil
}
