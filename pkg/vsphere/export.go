package vsphere

// Disk export over an NFC lease.
//
// ExportVm needs the VM powered off, and the simulator has no handler for it.
// A snapshot export does neither: Corral takes a temporary snapshot, asks
// vCenter for an NFC lease on it (ExportSnapshot serves the disks as of that
// snapshot), downloads the first disk as a stream-optimized VMDK, and removes
// the snapshot. A running guest keeps running, and the snapshot is quiesced
// when VMware Tools is there to honor it.

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/vmware/govmomi/vim25/soap"
	govmtypes "github.com/vmware/govmomi/vim25/types"
)

// ExportConsistency is how consistent an exported disk is.
type ExportConsistency string

const (
	// ExportOffline is a disk read from a powered-off VM.
	ExportOffline ExportConsistency = "offline"
	// ExportQuiesced is a disk from a snapshot that VMware Tools quiesced.
	ExportQuiesced ExportConsistency = "quiesced"
	// ExportCrash is a disk from a running VM without a quiesced snapshot.
	ExportCrash ExportConsistency = "crash"
)

// ExportDisk writes the VM's first disk, as a stream-optimized VMDK, to dest.
// The temporary snapshot is removed whether the download succeeds or not.
func (c *Client) ExportDisk(ctx context.Context, name, dest string) (ExportConsistency, error) {
	described, err := c.describe(ctx, name)
	if err != nil {
		return "", err
	}
	vm := c.vm(described)
	consistency := ExportOffline
	quiesce := false
	if described.Runtime.PowerState == govmtypes.VirtualMachinePowerStatePoweredOn {
		consistency = ExportCrash
		if running, err := vm.IsToolsRunning(ctx); err == nil && running {
			quiesce, consistency = true, ExportQuiesced
		}
	}

	snap := fmt.Sprintf("corral-export-%d", time.Now().UnixNano())
	if err := c.CreateSnapshot(ctx, name, snap, false, quiesce); err != nil {
		return "", err
	}
	defer func() {
		// The caller's ctx may be what ended the export; the cleanup must
		// still run, bounded on its own.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), Timeout)
		defer cancel()
		_ = c.RemoveSnapshot(cleanup, name, snap)
	}()

	ref, err := vm.FindSnapshot(ctx, snap)
	if err != nil {
		return "", fmt.Errorf("vsphere: export %q: %w", name, err)
	}
	lease, err := vm.ExportSnapshot(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("vsphere: export %q: %w", name, err)
	}
	info, err := lease.Wait(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("vsphere: export %q: lease: %w", name, err)
	}
	updater := lease.StartUpdater(ctx, info)
	defer updater.Done()

	for _, item := range info.Items {
		if !strings.EqualFold(path.Ext(item.Path), ".vmdk") {
			continue
		}
		if err := lease.DownloadFile(ctx, dest, item, soap.DefaultDownload); err != nil {
			_ = lease.Abort(ctx, nil)
			return "", fmt.Errorf("vsphere: export %q: download %s: %w", name, item.Path, err)
		}
		if err := lease.Complete(ctx); err != nil {
			return "", fmt.Errorf("vsphere: export %q: %w", name, err)
		}
		return consistency, nil
	}
	_ = lease.Abort(ctx, nil)
	return "", fmt.Errorf("vsphere: export %q: the VM has no disk to export", name)
}
