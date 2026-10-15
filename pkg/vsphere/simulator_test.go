package vsphere

// Fixture harness: every test in this package runs against the
// govmomi/simulator, a real vCenter-shaped SOAP endpoint in-process. The
// client under test gets its session injected through WithVimClient, so no
// test ever dials anything — the same role Proxmox's recorded-payload
// httptest fixtures play.
//
// TestSimulatorCoversOpsPaths is the VS-0 spike proof, kept: it drives the
// raw govmomi surface Corral needs (power, snapshot, clone, relocate,
// reconfig) against the simulator and fails first if a govmomi upgrade drops
// one of those paths. Everything else in this package exercises the same
// paths through Client methods.

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	govmtypes "github.com/vmware/govmomi/vim25/types"
)

// simClient returns a Client whose session points at a VPX simulator with two
// VMs, plus their names. The model is removed when the test ends.
func simClient(t *testing.T) (*Client, []string) {
	t.Helper()
	client := simClientFor(t, simulator.VPX())
	vms, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) == 0 {
		t.Fatal("simulator has no VMs")
	}
	names := make([]string, 0, len(vms))
	for _, vm := range vms {
		names = append(names, vm.Name)
	}
	return client, names
}

// simVM returns the object handle for a simulator VM by name.
func simVM(t *testing.T, client *Client, name string) *object.VirtualMachine {
	t.Helper()
	described, err := client.describe(context.Background(), name)
	if err != nil {
		t.Fatalf("describe %q: %v", name, err)
	}
	return client.vm(described)
}

func TestSimulatorCoversOpsPaths(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	vm := simVM(t, client, names[0])

	powerState := func() govmtypes.VirtualMachinePowerState {
		t.Helper()
		var o mo.VirtualMachine
		pc := property.DefaultCollector(client.vim)
		if err := pc.RetrieveOne(ctx, vm.Reference(), []string{"runtime.powerState"}, &o); err != nil {
			t.Fatalf("power state: %v", err)
		}
		return o.Runtime.PowerState
	}

	// Power: off then on. The model boots its VMs powered on; proving both
	// directions matters because Stop and Start take different paths.
	if task, err := vm.PowerOff(ctx); err != nil {
		t.Fatalf("PowerOff: %v", err)
	} else if err := task.Wait(ctx); err != nil {
		t.Fatalf("PowerOff wait: %v", err)
	}
	if got := powerState(); got != govmtypes.VirtualMachinePowerStatePoweredOff {
		t.Fatalf("power state = %s, want poweredOff", got)
	}
	if task, err := vm.PowerOn(ctx); err != nil {
		t.Fatalf("PowerOn: %v", err)
	} else if err := task.Wait(ctx); err != nil {
		t.Fatalf("PowerOn wait: %v", err)
	}

	// Snapshot: create with memory and quiesce, then remove.
	snapTask, err := vm.CreateSnapshot(ctx, "spike", "spike proof", true, false)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if err := snapTask.Wait(ctx); err != nil {
		t.Fatalf("CreateSnapshot wait: %v", err)
	}
	rmTask, err := vm.RemoveSnapshot(ctx, "spike", false, nil)
	if err != nil {
		t.Fatalf("RemoveSnapshot: %v", err)
	}
	if err := rmTask.Wait(ctx); err != nil {
		t.Fatalf("RemoveSnapshot wait: %v", err)
	}

	// Reconfig: change the CPU count.
	reTask, err := vm.Reconfigure(ctx, govmtypes.VirtualMachineConfigSpec{NumCPUs: 4})
	if err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	if err := reTask.Wait(ctx); err != nil {
		t.Fatalf("Reconfigure wait: %v", err)
	}

	// Relocate: move to any host that is not the current one.
	pool, err := vm.ResourcePool(ctx)
	if err != nil {
		t.Fatalf("ResourcePool: %v", err)
	}
	pc := property.DefaultCollector(client.vim)
	var described mo.VirtualMachine
	if err := pc.RetrieveOne(ctx, vm.Reference(), []string{"runtime.host", "parent"}, &described); err != nil {
		t.Fatalf("runtime.host: %v", err)
	}
	mgr := view.NewManager(client.vim)
	hcv, err := mgr.CreateContainerView(ctx, client.vim.ServiceContent.RootFolder, []string{"HostSystem"}, true)
	if err != nil {
		t.Fatalf("host view: %v", err)
	}
	defer func() { _ = hcv.Destroy(ctx) }()
	var hosts []mo.HostSystem
	if err := hcv.Retrieve(ctx, []string{"HostSystem"}, []string{"name"}, &hosts); err != nil {
		t.Fatalf("hosts: %v", err)
	}
	if len(hosts) == 0 {
		t.Fatal("simulator has no hosts")
	}
	target := hosts[0]
	for _, h := range hosts {
		if described.Runtime.Host == nil || h.Reference() != *described.Runtime.Host {
			target = h
			break
		}
	}
	hostRef := target.Reference()
	poolRef := pool.Reference()
	relTask, err := vm.Relocate(ctx, govmtypes.VirtualMachineRelocateSpec{
		Host: &hostRef,
		Pool: &poolRef,
	}, govmtypes.VirtualMachineMovePriorityDefaultPriority)
	if err != nil {
		t.Fatalf("Relocate: %v", err)
	}
	if err := relTask.Wait(ctx); err != nil {
		t.Fatalf("Relocate wait: %v", err)
	}

	// Clone: a full clone into the source's folder and pool.
	if described.Parent == nil {
		t.Fatal("simulator VM has no parent folder")
	}
	folder := object.NewFolder(client.vim, *described.Parent)
	cloneTask, err := vm.Clone(ctx, folder, "spike-clone", govmtypes.VirtualMachineCloneSpec{
		Location: govmtypes.VirtualMachineRelocateSpec{Pool: &poolRef},
		PowerOn:  false,
	})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if err := cloneTask.Wait(ctx); err != nil {
		t.Fatalf("Clone wait: %v", err)
	}
	if _, err := client.describe(ctx, "spike-clone"); err != nil {
		t.Fatalf("clone not listed: %v", err)
	}
}
