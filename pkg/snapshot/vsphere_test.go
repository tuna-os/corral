package snapshot

import (
	"context"
	"testing"

	govmomi "github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"

	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/vsphere"
)

// simVsphere points the Vsphere adapter at a VPX simulator and returns a
// reference to its first VM.
func simVsphere(t *testing.T) types.InstanceRef {
	t.Helper()
	ctx := context.Background()

	model := simulator.VPX()
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
	client, err := vsphere.New(vsphere.Config{Host: "sim", Username: "user", Password: "pass"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.WithVimClient(gov.Client)

	vms, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) == 0 {
		t.Fatal("simulator has no VMs")
	}
	vsphere.SetClientForContext("sim", client)
	t.Cleanup(vsphere.ResetClients)
	old := VsphereClientFor
	VsphereClientFor = func(ref types.InstanceRef) (*vsphere.Client, error) {
		return vsphere.ClientForContext("sim")
	}
	t.Cleanup(func() { VsphereClientFor = old })
	return types.InstanceRef{Backend: "vsphere", Context: "sim", Name: vms[0].Name}
}

func TestVsphereSnapshotsEndToEnd(t *testing.T) {
	ref := simVsphere(t)
	adapter := Vsphere{}

	// Stopped capture first: the model boots its VMs powered on.
	client, err := VsphereClientFor(ref)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx := context.Background()
	if err := client.Stop(ctx, ref.Name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	created, err := adapter.Create(ref, "off")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "off" || created.Source != ref.Name || !created.Ready {
		t.Fatalf("bad snapshot: %+v", created)
	}
	if created.Consistency != Offline {
		t.Fatalf("stopped capture consistency = %q, want offline", created.Consistency)
	}

	// Running capture without tools: RAM, nothing quiesced, crash.
	if err := client.Start(ctx, ref.Name); err != nil {
		t.Fatalf("Start: %v", err)
	}
	created, err = adapter.Create(ref, "run")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Consistency != Crash {
		t.Fatalf("running capture consistency = %q, want crash", created.Consistency)
	}

	snaps, err := adapter.List(ref)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %v, want two", snaps)
	}

	if err := adapter.Restore(ref, "run"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := adapter.Delete(ref, "off"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	snaps, err = adapter.List(ref)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 1 || snaps[0].Name != "run" {
		t.Fatalf("snapshots after deletion = %v, want only run", snaps)
	}
	if err := adapter.Restore(ref, "ghost"); err == nil {
		t.Fatal("restore of an unknown snapshot succeeded")
	}
}

func TestVsphereConsistencyMapping(t *testing.T) {
	adapter := Vsphere{}
	for _, tc := range []struct {
		poweredOn, quiesced bool
		want                Consistency
	}{
		{false, false, Offline},
		{false, true, Offline},
		{true, false, Crash},
		{true, true, Filesystem},
	} {
		if got := adapter.consistency(tc.poweredOn, tc.quiesced); got != tc.want {
			t.Fatalf("consistency(%v, %v) = %q, want %q",
				tc.poweredOn, tc.quiesced, got, tc.want)
		}
	}
}

func TestVsphereAdapterIsRegistered(t *testing.T) {
	if !Supported("vsphere") {
		t.Fatal("Supported(vsphere) is false")
	}
	adapter, err := For(types.InstanceRef{Backend: "vsphere", Name: "probe"})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if _, ok := adapter.(Vsphere); !ok {
		t.Fatalf("For returned %T, want Vsphere", adapter)
	}
}
