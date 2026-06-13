//go:build integration

package kubevirt

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanthor/corral/pkg/catalog"
	"github.com/hanthor/corral/pkg/shell"
	"github.com/hanthor/corral/pkg/types"
)

const testNS = "corral-e2e"

// TestMain sets up and tears down the test namespace.
func TestMain(m *testing.M) {
	// Create the test namespace
	shell.Real{}.Run("kubectl", "create", "ns", testNS)
	shell.Real{}.Run("kubectl", "label", "ns", testNS,
		"pod-security.kubernetes.io/enforce=privileged", "--overwrite")

	code := m.Run()

	// Cleanup: delete everything in the namespace, then the namespace
	shell.Real{}.Run("kubectl", "delete", "vm", "--all", "-n", testNS, "--ignore-not-found")
	shell.Real{}.Run("kubectl", "delete", "pvc", "--all", "-n", testNS, "--ignore-not-found")
	shell.Real{}.Run("kubectl", "delete", "datavolume", "--all", "-n", testNS, "--ignore-not-found")
	shell.Real{}.Run("kubectl", "delete", "vmsnapshot", "--all", "-n", testNS, "--ignore-not-found")
	shell.Real{}.Run("kubectl", "delete", "ns", testNS, "--ignore-not-found")

	os.Exit(code)
}

// randomSuffix returns a short random string for unique names.
func randomSuffix() string {
	return fmt.Sprintf("%x", time.Now().UnixNano()%0xFFFF)
}

// ── Smoke tests ───────────────────────────────────────────────────

func TestIntegration_CreateCatalogVM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running integration test in short mode")
	}
	timeout := 5 * time.Minute
	if deadline, ok := t.Deadline(); ok {
		timeout = time.Until(deadline) - 10*time.Second
	}
	name := "test-catalog-" + randomSuffix()
	client := NewClient(testNS)

	opts := types.CreateOpts{
		Name:          name,
		Namespace:     testNS,
		ContainerDisk: "quay.io/containerdisks/fedora:42",
		CPU:           1,
		Mem:           "2G",
		Disk:          "5Gi",
	}
	if err := CreateVM(opts); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	// Verify VM exists in list
	vms, err := client.ListVMs()
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	found := false
	for _, v := range vms {
		if v.Name == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("VM %q not found in list after creation", name)
	}

	// Start it
	if err := client.StartVM(name); err != nil {
		t.Fatalf("StartVM: %v", err)
	}

	// Wait for Running state
	t.Logf("Waiting for %q to reach Running state...", name)
	var ready bool
	start := time.Now()
	deadline := start.Add(timeout)
	for time.Now().Before(deadline) {
		vms, err := client.ListVMs()
		if err != nil {
			t.Fatalf("ListVMs: %v", err)
		}
		for _, v := range vms {
			if v.Name == name && v.Ready {
				ready = true
				t.Logf("VM %q is Ready after %s", name, time.Since(start).Round(time.Second))
				break
			}
		}
		if ready {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if !ready {
		// Clean up and fail
		client.StopVM(name)
		t.Fatalf("VM %q did not become Ready within %v", name, timeout)
	}

	// Verify IP is assigned
	vms, _ = client.ListVMs()
	var ip string
	for _, v := range vms {
		if v.Name == name {
			ip = v.IP
			break
		}
	}
	if ip == "" {
		t.Logf("VM %q has no IP (guest agent may not be running yet)", name)
	}

	// Stop and clean up
	t.Logf("Stopping %q...", name)
	if err := client.StopVM(name); err != nil {
		t.Logf("StopVM (best-effort): %v", err)
	}
	time.Sleep(2 * time.Second)

	if err := client.DeleteVM(name); err != nil {
		t.Errorf("DeleteVM: %v", err)
	}
}

func TestIntegration_ListVMs(t *testing.T) {
	client := NewClient(testNS)
	vms, err := client.ListVMs()
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	t.Logf("Found %d VMs in namespace %s", len(vms), testNS)
}

func TestIntegration_ImageCatalog(t *testing.T) {
	// Verify the catalog has expected images
	fromCatalog := catalog.Find("fedora")
	if fromCatalog == nil {
		t.Fatal("fedora not found in catalog")
	}
	if !strings.Contains(fromCatalog.ContainerDisk, "fedora") {
		t.Errorf("expected fedora containerDisk, got %q", fromCatalog.ContainerDisk)
	}

	fromCatalog = catalog.Find("ubuntu")
	if fromCatalog == nil {
		t.Fatal("ubuntu not found in catalog")
	}
}

func TestIntegration_Capabilities(t *testing.T) {
	caps := ClusterCapabilities()
	t.Logf("StorageClass: %s, CanExpand: %v, CanSnapshot: %v",
		caps.StorageClass, caps.CanExpand, caps.CanSnapshot)

	if caps.StorageClass == "" {
		t.Error("no storage class found")
	}
}

func TestIntegration_DataVolumeOps(t *testing.T) {
	name := "test-dv-" + randomSuffix()

	// List existing (should be empty or have previous leftovers)
	dvs, err := ListDataVolumes()
	if err != nil {
		t.Fatalf("ListDataVolumes: %v", err)
	}
	initial := len(dvs)
	t.Logf("Initial DataVolumes: %d", initial)

	// Create a small import
	if err := ImportDataVolume(name, testNS, "https://cloud-images.ubuntu.com/minimal/releases/jammy/release/ubuntu-22.04-minimal-cloudimg-amd64.img", "5Gi"); err != nil {
		t.Fatalf("ImportDataVolume: %v", err)
	}

	// Verify it appears in the list
	dvs, err = ListDataVolumes()
	if err != nil {
		t.Fatalf("ListDataVolumes: %v", err)
	}
	found := false
	for _, dv := range dvs {
		if dv.Name == name {
			found = true
			t.Logf("DataVolume %q phase: %s", name, dv.Phase)
			break
		}
	}
	if !found {
		t.Errorf("DataVolume %q not found after creation", name)
	}

	// Clean up
	if err := DeleteDataVolume(testNS, name); err != nil {
		t.Errorf("DeleteDataVolume: %v", err)
	}
}

func TestIntegration_SnapshotOps(t *testing.T) {
	caps := ClusterCapabilities()
	if !caps.CanSnapshot {
		t.Skip("snapshots not available on this cluster (no VolumeSnapshotClass)")
	}

	name := "test-snap-" + randomSuffix()
	client := NewClient(testNS)

	// Create a VM
	opts := types.CreateOpts{
		Name:          name,
		Namespace:     testNS,
		ContainerDisk: "quay.io/containerdisks/fedora:42",
		CPU:           1,
		Mem:           "2G",
		Disk:          "5Gi",
	}
	if err := CreateVM(opts); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	// Snapshot it (VM can be stopped)
	snapName := name + "-snap"
	snap, err := client.Snapshot(name, snapName)
	if err != nil {
		client.DeleteVM(name)
		t.Fatalf("Snapshot: %v", err)
	}
	t.Logf("Created snapshot: %s", snap)

	// List snapshots
	snaps, err := client.ListSnapshots(name)
	if err != nil {
		client.DeleteVM(name)
		t.Fatalf("ListSnapshots: %v", err)
	}
	found := false
	for _, s := range snaps {
		if s.Name == snapName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("snapshot %q not found in list", snapName)
	}

	// Delete snapshot
	if err := client.DeleteSnapshot(snap); err != nil {
		t.Errorf("DeleteSnapshot: %v", err)
	}

	// Clean up VM
	client.DeleteVM(name)
}
