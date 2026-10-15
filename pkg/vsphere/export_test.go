package vsphere

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExportDiskRunningIsCrashAndCleansUp(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	dest := filepath.Join(t.TempDir(), "disk.vmdk")
	consistency, err := client.ExportDisk(ctx, names[0], dest)
	if err != nil {
		t.Fatalf("ExportDisk: %v", err)
	}
	if consistency != ExportCrash {
		t.Errorf("consistency = %q, want crash for a running VM without tools", consistency)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("no disk written: %v", err)
	}
	snaps, err := client.ListSnapshots(ctx, names[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 0 {
		t.Errorf("temporary snapshot left behind: %+v", snaps)
	}
}

func TestExportDiskPoweredOffIsOffline(t *testing.T) {
	client, names := simClient(t)
	ctx := context.Background()
	if err := client.Stop(ctx, names[0]); err != nil {
		t.Fatal(err)
	}
	consistency, err := client.ExportDisk(ctx, names[0], filepath.Join(t.TempDir(), "disk.vmdk"))
	if err != nil {
		t.Fatalf("ExportDisk: %v", err)
	}
	if consistency != ExportOffline {
		t.Errorf("consistency = %q, want offline", consistency)
	}
}
