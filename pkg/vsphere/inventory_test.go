package vsphere

import (
	"context"
	"testing"
)

func TestListReturnsSimulatorInventory(t *testing.T) {
	client, names := simClient(t)

	vms, err := client.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vms) != len(names) {
		t.Fatalf("len(vms) = %d, want %d", len(vms), len(names))
	}
	byName := map[string]bool{}
	for _, vm := range vms {
		byName[vm.Name] = true
		if vm.Backend != "vsphere" {
			t.Fatalf("backend = %q", vm.Backend)
		}
		if vm.Context != "simulator" {
			t.Fatalf("context = %q, want the configured host", vm.Context)
		}
		if vm.Namespace != "" {
			t.Fatalf("namespace = %q, want empty", vm.Namespace)
		}
		if vm.CPU <= 0 || vm.Mem == "" {
			t.Fatalf("bad shape: %+v", vm)
		}
		if vm.Node == "" {
			t.Fatalf("node is empty: %+v", vm)
		}
		if vm.Status != "Running" || !vm.Running || !vm.Ready {
			t.Fatalf("simulator VMs boot powered on: %+v", vm)
		}
		if vm.ID == "" {
			t.Fatal("SetIdentity was not applied: empty ID")
		}
		if !vm.Capabilities.Start || !vm.Capabilities.Snapshots || !vm.Capabilities.Migrate {
			t.Fatalf("capabilities disagree with the matrix: %+v", vm.Capabilities)
		}
	}
	for _, want := range names {
		if !byName[want] {
			t.Fatalf("missing VM %q", want)
		}
	}
}

func TestDescribeRejectsUnknownNames(t *testing.T) {
	client, _ := simClient(t)
	if _, err := client.describe(context.Background(), "ghost"); err == nil {
		t.Fatal("expected an error for an unknown VM")
	}
}
