package create

import (
	"fmt"

	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/types"
)

func (r request) runIncusCreate(name string) error {
	if r.ISO != "" || r.QCOW != "" || r.Import != "" || r.PVC != "" || r.ContainerDisk != "" {
		return fmt.Errorf("the Incus backend creates from an Incus image; use --image (for example images:ubuntu/24.04)")
	}
	image := r.Image
	if image == "" {
		image = "images:ubuntu/24.04"
	}
	if err := r.IncusCreate(incus.CreateOpts{Name: name, Image: image, VM: true, CPU: r.CPU, Memory: r.Mem}); err != nil {
		return err
	}
	if r.Registry != nil {
		return r.Registry.Set(name, types.RegistryEntry{Backend: "incus"})
	}
	return nil
}
