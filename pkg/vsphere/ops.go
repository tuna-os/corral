package vsphere

// The operations, one per row of the parity matrix.
//
// Every one of them resolves a name to a VM first, which is what lets a
// caller pass the name a human uses while vCenter gets the reference it
// needs — the same shape as pkg/proxmoxbe/ops.go. govmomi tasks are waited on
// inside each mutation, so callers get completion, not a task handle: a
// surface that fired and forgot would report success for a migration that
// failed thirty seconds later.
//
// What the simulator cannot cover is not here: ExportVm has no simulator
// handler (the simulator implements HttpNfcLease only for resource-pool
// import and snapshot export), so export stays a Possible matrix cell with no
// client method until a fixture-backed slice lands it.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	govmtypes "github.com/vmware/govmomi/vim25/types"
)

// ── power ─────────────────────────────────────────────────────────

// PowerState reports the VM's current power state.
func (c *Client) PowerState(ctx context.Context, name string) (govmtypes.VirtualMachinePowerState, error) {
	described, err := c.describe(ctx, name)
	if err != nil {
		return "", err
	}
	return described.Runtime.PowerState, nil
}

// Start boots a VM.
func (c *Client) Start(ctx context.Context, name string) error {
	described, err := c.describe(ctx, name)
	if err != nil {
		return err
	}
	task, err := c.vm(described).PowerOn(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: start %q: %w", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: start %q: %w", name, err)
	}
	return nil
}

// Stop shuts a VM down gracefully via the guest first and falls back to a
// hard power-off when the guest does not accept the request (no tools, or an
// already-stopped guest). Graceful first gives the guest its shutdown
// ordering; hard only when graceful fails.
func (c *Client) Stop(ctx context.Context, name string) error {
	described, err := c.describe(ctx, name)
	if err != nil {
		return err
	}
	vm := c.vm(described)
	if err := vm.ShutdownGuest(ctx); err == nil {
		return nil
	}
	task, err := vm.PowerOff(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: stop %q: %w", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: stop %q: %w", name, err)
	}
	return nil
}

// Delete destroys the VM. vCenter refuses to destroy a running guest, so a
// running VM is powered off first — hard, not graceful: Delete is already
// destructive, and a graceful shutdown would only delay the inevitable while
// pretending otherwise.
func (c *Client) Delete(ctx context.Context, name string) error {
	described, err := c.describe(ctx, name)
	if err != nil {
		return err
	}
	vm := c.vm(described)
	if described.Runtime.PowerState == govmtypes.VirtualMachinePowerStatePoweredOn {
		task, err := vm.PowerOff(ctx)
		if err != nil {
			return fmt.Errorf("vsphere: delete %q: %w", name, err)
		}
		if err := task.Wait(ctx); err != nil {
			return fmt.Errorf("vsphere: delete %q: %w", name, err)
		}
	}
	task, err := vm.Destroy(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: delete %q: %w", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: delete %q: %w", name, err)
	}
	return nil
}

// Restart resets the VM through vCenter rather than stop-then-start — the
// backend's own reset, not a power cycle.
func (c *Client) Restart(ctx context.Context, name string) error {
	described, err := c.describe(ctx, name)
	if err != nil {
		return err
	}
	task, err := c.vm(described).Reset(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: restart %q: %w", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: restart %q: %w", name, err)
	}
	return nil
}

// Suspend freezes the guest to memory. Resume powers it back on from that
// exact point — the same verb vCenter uses, since a suspended VM resumes with
// PowerOn.
func (c *Client) Suspend(ctx context.Context, name string) error {
	described, err := c.describe(ctx, name)
	if err != nil {
		return err
	}
	task, err := c.vm(described).Suspend(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: suspend %q: %w", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: suspend %q: %w", name, err)
	}
	return nil
}

// Resume continues a suspended VM from where it froze.
func (c *Client) Resume(ctx context.Context, name string) error {
	return c.Start(ctx, name)
}

// ── size ──────────────────────────────────────────────────────────

// HotplugsLive asks the guest's own config rather than guessing: vCenter
// reports which devices are hotpluggable, the same shape as PVE's hotplug
// field.
func (c *Client) HotplugsLive(ctx context.Context, name string) (bool, error) {
	described, err := c.describe(ctx, name)
	if err != nil {
		return false, err
	}
	if described.Config == nil {
		return false, nil
	}
	return described.Config.CpuHotAddEnabled != nil && *described.Config.CpuHotAddEnabled &&
		described.Config.MemoryHotAddEnabled != nil && *described.Config.MemoryHotAddEnabled, nil
}

// Scale edits CPU and memory through ReconfigVM. A powered-off VM always
// accepts it; a running one only when the guest allows hot-add — otherwise
// the reconfig is refused with the remedy instead of failing inside vCenter.
func (c *Client) Scale(ctx context.Context, name string, cores int, mem string) error {
	described, err := c.describe(ctx, name)
	if err != nil {
		return err
	}
	spec := govmtypes.VirtualMachineConfigSpec{}
	if cores > 0 {
		spec.NumCPUs = int32(cores)
	}
	if mem != "" {
		mib, err := memoryMiB(mem)
		if err != nil {
			return err
		}
		spec.MemoryMB = int64(mib)
	}
	if spec.NumCPUs == 0 && spec.MemoryMB == 0 {
		return nil
	}
	if described.Runtime.PowerState == govmtypes.VirtualMachinePowerStatePoweredOn {
		hot, err := c.HotplugsLive(ctx, name)
		if err != nil {
			return err
		}
		if !hot {
			return fmt.Errorf("vsphere: %q is running without CPU/memory hot-add; power it off first or enable hot-add", name)
		}
	}
	task, err := c.vm(described).Reconfigure(ctx, spec)
	if err != nil {
		return fmt.Errorf("vsphere: scale %q: %w", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: scale %q: %w", name, err)
	}
	return nil
}

// memoryMiB converts Corral's memory strings to the MiB integer vCenter
// wants, mirroring proxmoxbe's parser so Scale means the same everywhere.
func memoryMiB(mem string) (int, error) {
	lower := strings.ToLower(strings.TrimSpace(mem))
	var multiplier int
	switch {
	case strings.HasSuffix(lower, "gi"), strings.HasSuffix(lower, "g"), strings.HasSuffix(lower, "gb"):
		multiplier = 1024
	case strings.HasSuffix(lower, "mi"), strings.HasSuffix(lower, "m"), strings.HasSuffix(lower, "mb"):
		multiplier = 1
	case isAllDigits(lower):
		// A bare number is MiB.
		multiplier = 1
	default:
		return 0, fmt.Errorf("vsphere: cannot read memory %q; use e.g. 4Gi, 2048Mi, or 2048", mem)
	}
	digits := strings.TrimRight(lower, "gimb")
	value, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, fmt.Errorf("vsphere: cannot read memory %q: %w", mem, err)
	}
	mib := int(value * float64(multiplier))
	if mib <= 0 {
		return 0, fmt.Errorf("vsphere: memory %q is not a positive size", mem)
	}
	return mib, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ── movement ──────────────────────────────────────────────────────

// HostInfo is one estate host a VM can migrate to.
type HostInfo struct {
	Name string
	Ref  govmtypes.ManagedObjectReference
}

// Hosts lists the estate's hosts.
func (c *Client) Hosts(ctx context.Context) ([]HostInfo, error) {
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	mgr := view.NewManager(c.vim)
	cv, err := mgr.CreateContainerView(ctx, c.vim.ServiceContent.RootFolder, []string{"HostSystem"}, true)
	if err != nil {
		return nil, fmt.Errorf("vsphere: list hosts: %w", err)
	}
	defer func() { _ = cv.Destroy(ctx) }()
	var hosts []mo.HostSystem
	if err := cv.Retrieve(ctx, []string{"HostSystem"}, []string{"name"}, &hosts); err != nil {
		return nil, fmt.Errorf("vsphere: list hosts: %w", err)
	}
	out := make([]HostInfo, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, HostInfo{Name: h.Name, Ref: h.Reference()})
	}
	return out, nil
}

// Migrate vMotions a VM to another host. An empty target takes the first
// host that is not the current one, so "migrate" with no argument behaves
// the way it does on other backends. The pool and datastore stay put:
// same-cluster vMotion is the routine case, and a cross-cluster move is a
// different operation with its own preconditions.
func (c *Client) Migrate(ctx context.Context, name, target string) error {
	described, err := c.describe(ctx, name)
	if err != nil {
		return err
	}
	vm := c.vm(described)
	hostRef, err := c.migrationTarget(ctx, described, target)
	if err != nil {
		return err
	}
	pool, err := vm.ResourcePool(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: migrate %q: %w", name, err)
	}
	poolRef := pool.Reference()
	task, err := vm.Relocate(ctx, govmtypes.VirtualMachineRelocateSpec{
		Host: &hostRef,
		Pool: &poolRef,
	}, govmtypes.VirtualMachineMovePriorityDefaultPriority)
	if err != nil {
		return fmt.Errorf("vsphere: migrate %q: %w", name, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: migrate %q: %w", name, err)
	}
	return nil
}

func (c *Client) migrationTarget(ctx context.Context, described mo.VirtualMachine, target string) (govmtypes.ManagedObjectReference, error) {
	hosts, err := c.Hosts(ctx)
	if err != nil {
		return govmtypes.ManagedObjectReference{}, err
	}
	if target != "" {
		for _, h := range hosts {
			if h.Name == target {
				return h.Ref, nil
			}
		}
		return govmtypes.ManagedObjectReference{}, fmt.Errorf("vsphere: no host named %q", target)
	}
	for _, h := range hosts {
		if described.Runtime.Host == nil || h.Ref != *described.Runtime.Host {
			return h.Ref, nil
		}
	}
	return govmtypes.ManagedObjectReference{}, fmt.Errorf("vsphere: no migration target: the estate has a single host")
}

// CanMigrate reports whether a migration has anywhere to go. Templates
// cannot move (clone them instead) and a single-host estate has no
// destination — both are refused with the reason rather than failing inside
// vCenter.
func (c *Client) CanMigrate(ctx context.Context, name string) (bool, string) {
	described, err := c.describe(ctx, name)
	if err != nil {
		return false, err.Error()
	}
	if described.Config != nil && described.Config.Template {
		return false, "templates cannot be migrated; clone the template instead"
	}
	hosts, err := c.Hosts(ctx)
	if err != nil {
		return false, err.Error()
	}
	if len(hosts) < 2 {
		return false, "the estate has a single host, so there is nowhere to migrate to"
	}
	return true, ""
}

// ── clone and template ────────────────────────────────────────────

// Clone makes a full clone of the source into its folder and pool. Full, not
// linked: without a snapshot reference CloneVM copies everything, so a clone
// never depends on its parent surviving.
func (c *Client) Clone(ctx context.Context, source, target string) error {
	described, err := c.describe(ctx, source)
	if err != nil {
		return err
	}
	vm := c.vm(described)
	if described.Parent == nil {
		return fmt.Errorf("vsphere: clone %q: source has no folder", source)
	}
	pool, err := vm.ResourcePool(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: clone %q: %w", source, err)
	}
	poolRef := pool.Reference()
	task, err := vm.Clone(ctx, object.NewFolder(c.vim, *described.Parent), target,
		govmtypes.VirtualMachineCloneSpec{
			Location: govmtypes.VirtualMachineRelocateSpec{Pool: &poolRef},
			PowerOn:  false,
		})
	if err != nil {
		return fmt.Errorf("vsphere: clone %q: %w", source, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: clone %q: %w", source, err)
	}
	return nil
}

// MarkTemplate marks a VM as a template. One-way, like PVE: vSphere can mark
// back only with a pool and host to place the result on, so unmarking is
// refused with the remedy instead of guessing placement.
func (c *Client) MarkTemplate(ctx context.Context, name string, on bool) error {
	described, err := c.describe(ctx, name)
	if err != nil {
		return err
	}
	if !on {
		return fmt.Errorf("vsphere: a template cannot be converted back into a guest; " +
			"clone it and delete the template instead")
	}
	// Marking powers the VM down into a template; vCenter refuses a running
	// guest rather than stopping it here, which would silently destroy the
	// running state the operator did not ask to lose.
	if described.Runtime.PowerState == govmtypes.VirtualMachinePowerStatePoweredOn {
		return fmt.Errorf("vsphere: power %q off before marking it as a template", name)
	}
	if err := c.vm(described).MarkAsTemplate(ctx); err != nil {
		return fmt.Errorf("vsphere: mark %q as template: %w", name, err)
	}
	return nil
}

// ── snapshots ─────────────────────────────────────────────────────

// SnapshotInfo is one capture as vCenter records it.
type SnapshotInfo struct {
	Name      string
	Created   string
	PoweredOn bool
	Quiesced  bool
}

// ListSnapshots returns every snapshot of the VM, walking the whole tree —
// children included, not just the roots.
func (c *Client) ListSnapshots(ctx context.Context, name string) ([]SnapshotInfo, error) {
	described, err := c.describe(ctx, name)
	if err != nil {
		return nil, err
	}
	if described.Snapshot == nil {
		return nil, nil
	}
	out := []SnapshotInfo{}
	var walk func(tree []govmtypes.VirtualMachineSnapshotTree)
	walk = func(tree []govmtypes.VirtualMachineSnapshotTree) {
		for _, item := range tree {
			out = append(out, SnapshotInfo{
				Name:      item.Name,
				Created:   item.CreateTime.Format("2006-01-02T15:04:05Z07:00"),
				PoweredOn: item.State == govmtypes.VirtualMachinePowerStatePoweredOn,
				Quiesced:  item.Quiesced,
			})
			walk(item.ChildSnapshotList)
		}
	}
	walk(described.Snapshot.RootSnapshotList)
	return out, nil
}

// CreateSnapshot captures the VM. Memory and quiesce are the caller's choice
// — the snapshot adapter asks for memory on a running guest and quiesce when
// the tools are there to honor it.
func (c *Client) CreateSnapshot(ctx context.Context, vmName, snap string, memory, quiesce bool) error {
	described, err := c.describe(ctx, vmName)
	if err != nil {
		return err
	}
	task, err := c.vm(described).CreateSnapshot(ctx, snap, "", memory, quiesce)
	if err != nil {
		return fmt.Errorf("vsphere: snapshot %q of %q: %w", snap, vmName, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: snapshot %q of %q: %w", snap, vmName, err)
	}
	return nil
}

// RemoveSnapshot deletes a snapshot, leaving its children on the VM.
func (c *Client) RemoveSnapshot(ctx context.Context, vmName, snap string) error {
	described, err := c.describe(ctx, vmName)
	if err != nil {
		return err
	}
	task, err := c.vm(described).RemoveSnapshot(ctx, snap, false, nil)
	if err != nil {
		return fmt.Errorf("vsphere: delete snapshot %q of %q: %w", snap, vmName, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: delete snapshot %q of %q: %w", snap, vmName, err)
	}
	return nil
}

// RevertSnapshot rolls the VM back to a snapshot. Unlike VirtualBox, vSphere
// can revert a running guest, so there is no power-state refusal here.
func (c *Client) RevertSnapshot(ctx context.Context, vmName, snap string, suppressPowerOn bool) error {
	described, err := c.describe(ctx, vmName)
	if err != nil {
		return err
	}
	task, err := c.vm(described).RevertToSnapshot(ctx, snap, suppressPowerOn)
	if err != nil {
		return fmt.Errorf("vsphere: revert %q to %q: %w", snap, vmName, err)
	}
	if err := task.Wait(ctx); err != nil {
		return fmt.Errorf("vsphere: revert %q to %q: %w", snap, vmName, err)
	}
	return nil
}

// ToolsRunning reports whether VMware Tools is running in the guest — the
// precondition for a quiesced snapshot.
func (c *Client) ToolsRunning(ctx context.Context, name string) (bool, error) {
	described, err := c.describe(ctx, name)
	if err != nil {
		return false, err
	}
	running, err := c.vm(described).IsToolsRunning(ctx)
	if err != nil {
		return false, fmt.Errorf("vsphere: tools state of %q: %w", name, err)
	}
	return running, nil
}
