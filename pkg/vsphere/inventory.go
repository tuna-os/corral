package vsphere

// Inventory and identity.
//
// vCenter addresses everything by managed object reference; Corral addresses
// everything by name. The whole estate comes back from one container view over
// VirtualMachine — VMs and templates together, with power state, shape, and
// host — so the name→reference map and the fleet listing are the same
// request, and no operation needs a second round trip to find out where an
// instance lives. This is the inventory.go shape pkg/proxmoxbe uses, over
// govmomi's view manager instead of /cluster/resources.

import (
	"context"
	"fmt"
	"sort"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	govmtypes "github.com/vmware/govmomi/vim25/types"

	"github.com/tuna-os/corral/pkg/types"
)

// vmProps is everything List and name resolution need in one pass. Parent
// places clones; snapshot feeds the snapshot adapter — both must be named
// explicitly, or the collector leaves them nil and lookups silently miss.
var vmProps = []string{"name", "runtime", "config", "guest", "summary", "parent", "snapshot"}

// describeAll returns every virtual machine on the estate.
func (c *Client) describeAll(ctx context.Context) ([]mo.VirtualMachine, error) {
	if err := c.Connect(ctx); err != nil {
		return nil, err
	}
	mgr := view.NewManager(c.vim)
	cv, err := mgr.CreateContainerView(ctx, c.vim.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return nil, fmt.Errorf("vsphere: list virtual machines: %w", err)
	}
	defer func() { _ = cv.Destroy(ctx) }()
	var vms []mo.VirtualMachine
	if err := cv.Retrieve(ctx, []string{"VirtualMachine"}, vmProps, &vms); err != nil {
		return nil, fmt.Errorf("vsphere: list virtual machines: %w", err)
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
	return vms, nil
}

// describe resolves one VM by name. Both the display name and an unambiguous
// match are accepted the way PVE users think in vmids and Corral users in
// names; refusing one of them would make the backend feel foreign from
// whichever side you arrive.
func (c *Client) describe(ctx context.Context, name string) (mo.VirtualMachine, error) {
	vms, err := c.describeAll(ctx)
	if err != nil {
		return mo.VirtualMachine{}, err
	}
	for _, vm := range vms {
		if vm.Name == name {
			return vm, nil
		}
	}
	return mo.VirtualMachine{}, fmt.Errorf("vsphere: no VM named %q", name)
}

// vm returns the object handle for a described VM, for the operations that
// act rather than read.
func (c *Client) vm(described mo.VirtualMachine) *object.VirtualMachine {
	return object.NewVirtualMachine(c.vim, described.Reference())
}

// List converts the estate's virtual machines into Corral VMs. Templates are
// inventory too — they are flagged, not hidden, the way PVE templates are.
func (c *Client) List() ([]types.VM, error) {
	ctx := context.Background()
	vms, err := c.describeAll(ctx)
	if err != nil {
		return nil, err
	}
	hosts, err := c.hostNames(ctx, vms)
	if err != nil {
		return nil, err
	}
	out := make([]types.VM, 0, len(vms))
	for _, vm := range vms {
		out = append(out, c.toVM(vm, hosts))
	}
	return out, nil
}

// hostNames resolves every runtime host in one property call, so List stays
// two round trips however many VMs the estate holds.
func (c *Client) hostNames(ctx context.Context, vms []mo.VirtualMachine) (map[govmtypes.ManagedObjectReference]string, error) {
	seen := map[govmtypes.ManagedObjectReference]bool{}
	var refs []govmtypes.ManagedObjectReference
	for _, vm := range vms {
		if vm.Runtime.Host == nil {
			continue
		}
		ref := *vm.Runtime.Host
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	names := map[govmtypes.ManagedObjectReference]string{}
	if len(refs) == 0 {
		return names, nil
	}
	pc := property.DefaultCollector(c.vim)
	var hosts []mo.HostSystem
	if err := pc.Retrieve(ctx, refs, []string{"name"}, &hosts); err != nil {
		return nil, fmt.Errorf("vsphere: list hosts: %w", err)
	}
	for _, h := range hosts {
		names[h.Reference()] = h.Name
	}
	return names, nil
}

func (c *Client) toVM(vm mo.VirtualMachine, hosts map[govmtypes.ManagedObjectReference]string) types.VM {
	running := vm.Runtime.PowerState == govmtypes.VirtualMachinePowerStatePoweredOn
	out := types.VM{
		Name:    vm.Name,
		Backend: "vsphere",
		// Namespace stays empty: vSphere has folders and pools, and
		// repurposing the field for one of them would make identity mean two
		// things — the same reason PVE leaves it empty.
		Context:      c.cfg.Host,
		Status:       statusWord(vm.Runtime.PowerState),
		Ready:        running,
		Running:      running,
		CPU:          numCPU(vm),
		Mem:          memMB(vm),
		IsTemplate:   vm.Config != nil && vm.Config.Template,
		Capabilities: types.CapabilitiesForBackend("vsphere"),
	}
	if vm.Runtime.Host != nil {
		out.Node = hosts[*vm.Runtime.Host]
	}
	if vm.Guest != nil && vm.Guest.IpAddress != "" {
		out.IP = vm.Guest.IpAddress
	}
	out.SetIdentity()
	return out
}

// statusWord reports the power state the way the TUI's classification keys
// off it: Running, Stopped, Suspended.
func statusWord(state govmtypes.VirtualMachinePowerState) string {
	switch state {
	case govmtypes.VirtualMachinePowerStatePoweredOn:
		return "Running"
	case govmtypes.VirtualMachinePowerStatePoweredOff:
		return "Stopped"
	case govmtypes.VirtualMachinePowerStateSuspended:
		return "Suspended"
	default:
		return string(state)
	}
}

func numCPU(vm mo.VirtualMachine) int {
	if vm.Config == nil || vm.Config.Hardware.NumCPU == 0 {
		return 0
	}
	return int(vm.Config.Hardware.NumCPU)
}

func memMB(vm mo.VirtualMachine) string {
	if vm.Config == nil || vm.Config.Hardware.MemoryMB == 0 {
		return ""
	}
	return fmt.Sprintf("%d MB", vm.Config.Hardware.MemoryMB)
}
