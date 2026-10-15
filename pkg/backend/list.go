package backend

import (
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// Lister inventories every guest in the adapter's context. Every registered
// backend has one — a backend Corral cannot list is not one it can manage —
// so it is not a Family: the "list" row of the matrix is shipped everywhere.
// The caller (pkg/fleet) stamps backend and context onto the results.
type Lister interface {
	List() ([]types.VM, error)
}

func (a kubevirtAdapter) List() ([]types.VM, error) { return a.client.ListVMs() }

func (qemuAdapter) List() ([]types.VM, error) { return qemu.List() }

func (a incusAdapter) List() ([]types.VM, error) { return a.client.List() }

func (a libvirtAdapter) List() ([]types.VM, error) { return a.client.List() }

func (a virtualboxAdapter) List() ([]types.VM, error) { return a.client.List() }

func (a proxmoxAdapter) List() ([]types.VM, error) {
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	return client.List()
}

// vSphere, Xen, and Nutanix bound a listing with their clients' own 20s
// request timeouts, the way proxmoxbe.Timeout bounds PVE: a wedged endpoint
// fails one context, not the whole fleet aggregation.

func (a vsphereAdapter) List() ([]types.VM, error) {
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	return client.List()
}

func (a xenAdapter) List() ([]types.VM, error) {
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	return client.List()
}

func (a nutanixAdapter) List() ([]types.VM, error) {
	client, err := a.client()
	if err != nil {
		return nil, err
	}
	return client.List()
}
