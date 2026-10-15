package vsphere

import (
	"context"
	"strings"
	"testing"

	govmomi "github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"
	govmtypes "github.com/vmware/govmomi/vim25/types"
)

// simClientFor builds a simulator-backed client from an explicit model, so
// tests can use topologies VPX does not offer (a single-host estate).
func simClientFor(t *testing.T, model *simulator.Model) *Client {
	t.Helper()
	ctx := context.Background()

	if err := model.Create(); err != nil {
		t.Fatalf("simulator create: %v", err)
	}
	t.Cleanup(model.Remove)

	server := model.Service.NewServer()
	t.Cleanup(server.Close)

	gov, err := govmomi.NewClient(ctx, server.URL, true)
	if err != nil {
		t.Fatalf("simulator login: %v", err)
	}
	client, err := New(Config{Host: "simulator", Username: "user", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client.WithVimClient(gov.Client)
}

func powerOf(t *testing.T, client *Client, name string) govmtypes.VirtualMachinePowerState {
	t.Helper()
	state, err := client.PowerState(context.Background(), name)
	if err != nil {
		t.Fatalf("PowerState: %v", err)
	}
	return state
}

func TestPowerCycle(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	name := names[0]

	if err := client.Stop(ctx, name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := powerOf(t, client, name); got != govmtypes.VirtualMachinePowerStatePoweredOff {
		t.Fatalf("state = %s, want poweredOff", got)
	}
	if err := client.Start(ctx, name); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := powerOf(t, client, name); got != govmtypes.VirtualMachinePowerStatePoweredOn {
		t.Fatalf("state = %s, want poweredOn", got)
	}
}

func TestStopOfAStoppedGuestReportsIt(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	name := names[0]

	if err := client.Stop(ctx, name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Both the graceful request and the hard fallback refuse a guest that is
	// already off; the refusal must name the operation, not vanish.
	if err := client.Stop(ctx, name); err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("second Stop = %v, want a stop error", err)
	}
}

func TestSuspendResumeRestart(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	name := names[0]

	if err := client.Suspend(ctx, name); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if got := powerOf(t, client, name); got != govmtypes.VirtualMachinePowerStateSuspended {
		t.Fatalf("state = %s, want suspended", got)
	}
	if err := client.Resume(ctx, name); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := powerOf(t, client, name); got != govmtypes.VirtualMachinePowerStatePoweredOn {
		t.Fatalf("state = %s, want poweredOn", got)
	}
	if err := client.Restart(ctx, name); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := powerOf(t, client, name); got != govmtypes.VirtualMachinePowerStatePoweredOn {
		t.Fatalf("state = %s, want poweredOn", got)
	}
}

func TestDelete(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	name := names[0]

	if err := client.Delete(ctx, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := client.describe(ctx, name); err == nil {
		t.Fatal("deleted VM still resolves")
	}
	if err := client.Delete(ctx, name); err == nil {
		t.Fatal("second Delete succeeded, want an error")
	}
}

func TestScaleOfflineAndHotAdd(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	name := names[0]

	// A running guest without hot-add refuses with the remedy.
	if err := client.Scale(ctx, name, 4, "8Gi"); err == nil ||
		!strings.Contains(err.Error(), "power it off") {
		t.Fatalf("running Scale = %v, want the power-off remedy", err)
	}

	if err := client.Stop(ctx, name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := client.Scale(ctx, name, 4, "8Gi"); err != nil {
		t.Fatalf("offline Scale: %v", err)
	}
	described, err := client.describe(ctx, name)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if described.Config.Hardware.NumCPU != 4 || described.Config.Hardware.MemoryMB != 8192 {
		t.Fatalf("shape = %d CPUs %d MB, want 4/8192",
			described.Config.Hardware.NumCPU, described.Config.Hardware.MemoryMB)
	}

	// Enabling hot-add makes the same edit work live.
	hot := true
	vm := client.vm(described)
	reTask, err := vm.Reconfigure(ctx, govmtypes.VirtualMachineConfigSpec{
		CpuHotAddEnabled:    &hot,
		MemoryHotAddEnabled: &hot,
	})
	if err != nil {
		t.Fatalf("enable hot-add: %v", err)
	}
	if err := reTask.Wait(ctx); err != nil {
		t.Fatalf("enable hot-add wait: %v", err)
	}
	if live, err := client.HotplugsLive(ctx, name); err != nil || !live {
		t.Fatalf("HotplugsLive = %v, %v; want true", live, err)
	}
	if err := client.Start(ctx, name); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := client.Scale(ctx, name, 8, ""); err != nil {
		t.Fatalf("live Scale: %v", err)
	}
}

func TestScaleRejectsGarbage(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	if err := client.Scale(ctx, names[0], 0, "lots"); err == nil {
		t.Fatal("bad memory size succeeded")
	}
	if err := client.Scale(ctx, "ghost", 2, ""); err == nil {
		t.Fatal("scale of an unknown VM succeeded")
	}
}

func TestMemoryMiB(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"2048", 2048}, {"2048Mi", 2048}, {"2048M", 2048}, {"2048MB", 2048},
		{"4Gi", 4096}, {"4G", 4096}, {"4GB", 4096}, {"1.5Gi", 1536},
	} {
		if got, err := memoryMiB(tc.in); err != nil || got != tc.want {
			t.Fatalf("memoryMiB(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "lots", "0", "-4Gi"} {
		if _, err := memoryMiB(in); err == nil {
			t.Fatalf("memoryMiB(%q) succeeded", in)
		}
	}
}

func TestMigrate(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	name := names[0]

	hosts, err := client.Hosts(ctx)
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	if len(hosts) < 2 {
		t.Fatalf("VPX simulator has %d hosts, want at least 2", len(hosts))
	}

	// Empty target: anywhere but here.
	if err := client.Migrate(ctx, name, ""); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if ok, reason := client.CanMigrate(ctx, name); !ok {
		t.Fatalf("CanMigrate = false (%q) after a migration", reason)
	}
	// Named target: exactly there.
	if err := client.Migrate(ctx, name, hosts[0].Name); err != nil {
		t.Fatalf("Migrate to %q: %v", hosts[0].Name, err)
	}
	described, err := client.describe(ctx, name)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if described.Runtime.Host == nil || *described.Runtime.Host != hosts[0].Ref {
		t.Fatalf("host = %+v, want %s", described.Runtime.Host, hosts[0].Name)
	}
	if err := client.Migrate(ctx, name, "ghost-host"); err == nil {
		t.Fatal("migrate to an unknown host succeeded")
	}
}

func TestCanMigrateRefusesSingleHostAndTemplates(t *testing.T) {
	single := simClientFor(t, simulator.ESX())
	ctx := context.Background()
	vms, err := single.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if ok, reason := single.CanMigrate(ctx, vms[0].Name); ok || !strings.Contains(reason, "single host") {
		t.Fatalf("CanMigrate = %v (%q), want the single-host refusal", ok, reason)
	}

	client, names := simClient(t)
	if err := client.Stop(ctx, names[0]); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := client.MarkTemplate(ctx, names[0], true); err != nil {
		t.Fatalf("MarkTemplate: %v", err)
	}
	if ok, reason := client.CanMigrate(ctx, names[0]); ok || !strings.Contains(reason, "template") {
		t.Fatalf("CanMigrate = %v (%q), want the template refusal", ok, reason)
	}
}

func TestClone(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()

	if err := client.Clone(ctx, names[0], "clone-target"); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	described, err := client.describe(ctx, "clone-target")
	if err != nil {
		t.Fatalf("clone not listed: %v", err)
	}
	if described.Config.Template {
		t.Fatal("a clone must be a VM, not a template")
	}
	if err := client.Clone(ctx, "ghost", "clone-target-2"); err == nil {
		t.Fatal("clone of an unknown VM succeeded")
	}
}

func TestMarkTemplate(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	name := names[0]

	// A running guest is refused rather than stopped behind the operator's
	// back; marking needs a stopped VM.
	if err := client.MarkTemplate(ctx, name, true); err == nil ||
		!strings.Contains(err.Error(), "power") {
		t.Fatalf("running MarkTemplate = %v, want the power-off remedy", err)
	}
	if err := client.Stop(ctx, name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := client.MarkTemplate(ctx, name, true); err != nil {
		t.Fatalf("MarkTemplate: %v", err)
	}
	described, err := client.describe(ctx, name)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !described.Config.Template {
		t.Fatal("VM is not flagged as a template after marking")
	}
	vms, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, vm := range vms {
		if vm.Name == name && !vm.IsTemplate {
			t.Fatal("List does not flag the template")
		}
	}
	// Unmarking is one-way: vSphere needs placement to make a VM again.
	if err := client.MarkTemplate(ctx, name, false); err == nil ||
		!strings.Contains(err.Error(), "cannot be converted back") {
		t.Fatalf("unmark = %v, want the one-way refusal", err)
	}
}

func TestSnapshots(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	name := names[0]

	if snaps, err := client.ListSnapshots(ctx, name); err != nil || len(snaps) != 0 {
		t.Fatalf("fresh VM has %v, %v; want none", snaps, err)
	}

	// Stopped capture: no memory, offline.
	if err := client.Stop(ctx, name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := client.CreateSnapshot(ctx, name, "off", false, false); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	// Running capture with memory but nothing quiesced: crash.
	if err := client.Start(ctx, name); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := client.CreateSnapshot(ctx, name, "run", true, false); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	// Running capture with quiesce: filesystem.
	if err := client.CreateSnapshot(ctx, name, "quiet", true, true); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}

	snaps, err := client.ListSnapshots(ctx, name)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	byName := map[string]SnapshotInfo{}
	for _, s := range snaps {
		byName[s.Name] = s
		if s.Created == "" {
			t.Fatalf("snapshot %q has no timestamp", s.Name)
		}
	}
	if len(byName) != 3 {
		t.Fatalf("snapshots = %v, want all three captures", snaps)
	}
	if byName["off"].PoweredOn || byName["off"].Quiesced {
		t.Fatalf("stopped capture flags: %+v", byName["off"])
	}
	if !byName["run"].PoweredOn || byName["run"].Quiesced {
		t.Fatalf("running capture flags: %+v", byName["run"])
	}
	if !byName["quiet"].PoweredOn || !byName["quiet"].Quiesced {
		t.Fatalf("quiesced capture flags: %+v", byName["quiet"])
	}

	if err := client.RevertSnapshot(ctx, name, "run", true); err != nil {
		t.Fatalf("RevertSnapshot: %v", err)
	}
	if err := client.RemoveSnapshot(ctx, name, "off"); err != nil {
		t.Fatalf("RemoveSnapshot: %v", err)
	}
	snaps, err = client.ListSnapshots(ctx, name)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("snapshots after removal = %v, want two", snaps)
	}
	if err := client.RemoveSnapshot(ctx, name, "ghost"); err == nil {
		t.Fatal("removal of an unknown snapshot succeeded")
	}
	if err := client.RevertSnapshot(ctx, name, "ghost", true); err == nil {
		t.Fatal("revert to an unknown snapshot succeeded")
	}
}

func TestToolsRunning(t *testing.T) {
	client, names := simClient(t)
	// Simulator guests run no tools; the point is the query works, not the
	// answer — the snapshot adapter treats false as "do not quiesce".
	if running, err := client.ToolsRunning(context.Background(), names[0]); err != nil || running {
		t.Fatalf("ToolsRunning = %v, %v; want false", running, err)
	}
}
