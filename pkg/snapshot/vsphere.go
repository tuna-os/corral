package snapshot

// ── vSphere ─────────────────────────────────────────────────────────
//
// Create/Remove/RevertSnapshot behind the shared contract. A snapshot taken
// while the VM runs includes its RAM, but nothing is quiesced unless the
// tools are there to honor it — the guest filesystem is exactly as
// consistent as after a power cut. So a stopped capture reports Offline, a
// quiesced one Filesystem, and a running one Crash, the same three labels
// every other adapter reports.

import (
	"context"
	"fmt"
	govmtypes "github.com/vmware/govmomi/vim25/types"

	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/vsphere"
)

// VsphereClientFor resolves a client for a reference. It is a package
// variable so the context configuration (host, user, TLS trust) can be
// supplied by whatever owns it, and so tests can point it at the simulator.
var VsphereClientFor = func(ref types.InstanceRef) (*vsphere.Client, error) {
	client, err := vsphere.ClientForContext(ref.Context)
	if err != nil {
		return nil, fmt.Errorf("%w — add the estate with "+
			"`corral context add <name> --backend vsphere --context <host>`", err)
	}
	return client, nil
}

// Vsphere snapshots a vCenter guest.
type Vsphere struct{}

func (v Vsphere) client(ref types.InstanceRef) (*vsphere.Client, error) {
	return VsphereClientFor(ref)
}

func (v Vsphere) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), vsphere.DefaultTimeout)
}

func (v Vsphere) Create(ref types.InstanceRef, name string) (Snapshot, error) {
	client, err := v.client(ref)
	if err != nil {
		return Snapshot{}, err
	}
	ctx, cancel := v.ctx()
	defer cancel()

	state, err := client.PowerState(ctx, ref.Name)
	if err != nil {
		return Snapshot{}, err
	}
	running := state == govmtypes.VirtualMachinePowerStatePoweredOn

	// A running capture carries RAM; quiesce joins it only when the tools
	// are there to honor the request — asking without tools fails the task.
	memory := running
	quiesce := false
	if running {
		if tools, err := client.ToolsRunning(ctx, ref.Name); err == nil && tools {
			quiesce = true
		}
	}
	if err := client.CreateSnapshot(ctx, ref.Name, name, memory, quiesce); err != nil {
		return Snapshot{}, err
	}

	// Read back rather than assume: the caller is told what vCenter actually
	// recorded, the way the Proxmox adapter reads back vmstate.
	snaps, err := client.ListSnapshots(ctx, ref.Name)
	if err != nil {
		return Snapshot{}, err
	}
	for _, s := range snaps {
		if s.Name == name {
			return v.toSnapshot(ref, s), nil
		}
	}
	return Snapshot{Name: name, Source: ref.Name, Consistency: v.consistency(running, false)}, nil
}

func (v Vsphere) List(ref types.InstanceRef) ([]Snapshot, error) {
	client, err := v.client(ref)
	if err != nil {
		return nil, err
	}
	ctx, cancel := v.ctx()
	defer cancel()

	snaps, err := client.ListSnapshots(ctx, ref.Name)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, v.toSnapshot(ref, s))
	}
	sortNewestFirst(out)
	return out, nil
}

func (v Vsphere) Restore(ref types.InstanceRef, name string) error {
	client, err := v.client(ref)
	if err != nil {
		return err
	}
	ctx, cancel := v.ctx()
	defer cancel()
	return client.RevertSnapshot(ctx, ref.Name, name, false)
}

func (v Vsphere) Delete(ref types.InstanceRef, name string) error {
	client, err := v.client(ref)
	if err != nil {
		return err
	}
	ctx, cancel := v.ctx()
	defer cancel()
	return client.RemoveSnapshot(ctx, ref.Name, name)
}

func (v Vsphere) toSnapshot(ref types.InstanceRef, s vsphere.SnapshotInfo) Snapshot {
	return Snapshot{
		Name:    s.Name,
		Source:  ref.Name,
		Created: s.Created,
		// vCenter snapshots are usable as soon as the task completes; there
		// is no separate readiness condition to wait on.
		Ready:       true,
		Consistency: v.consistency(s.PoweredOn, s.Quiesced),
	}
}

// consistency reports what the capture actually caught: a stopped guest is
// offline-consistent, a quiesced one filesystem-consistent, and anything
// else is a power-cut capture.
func (v Vsphere) consistency(poweredOn, quiesced bool) Consistency {
	switch {
	case !poweredOn:
		return Offline
	case quiesced:
		return Filesystem
	default:
		return Crash
	}
}
