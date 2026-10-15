package proxmoxbe

// Tests for the proxmox cells the parity matrix ships without a covering
// test: volumes (PUT …/config scsiN / delete=), GPU passthrough (PUT …/config
// hostpciN), and export (vzdump in snapshot mode, latest-backup selection,
// and the API download — pkg/proxmoxbe.ExportDisk behind the matrix note).

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddDiskDefaultsSlotAndStripsGigabytes(t *testing.T) {
	f := newFakePVE(t)
	f.on("PUT /nodes/pve1/qemu/100/config", nil)

	// No slot: the first free SCSI slot is the only sane default, and "10G"
	// is gigabytes — PVE wants the bare number.
	if err := f.client().AddDisk("web-prod", "local", "", "10G"); err != nil {
		t.Fatalf("AddDisk: %v", err)
	}
	call := f.lastCall("PUT", "/nodes/pve1/qemu/100/config")
	if got := call.Form.Get("scsi1"); got != "local:10" {
		t.Errorf("scsi1 = %q, want local:10", got)
	}
}

func TestAddDiskHonoursAnExplicitSlot(t *testing.T) {
	f := newFakePVE(t)
	f.on("PUT /nodes/pve1/qemu/100/config", nil)

	if err := f.client().AddDisk("web-prod", "fast-nvme", "scsi2", "50G"); err != nil {
		t.Fatalf("AddDisk: %v", err)
	}
	call := f.lastCall("PUT", "/nodes/pve1/qemu/100/config")
	if got := call.Form.Get("scsi2"); got != "fast-nvme:50" {
		t.Errorf("scsi2 = %q, want fast-nvme:50", got)
	}
	if got := call.Form.Get("scsi1"); got != "" {
		t.Errorf("scsi1 = %q, want untouched", got)
	}
}

func TestRemoveDiskSendsDelete(t *testing.T) {
	f := newFakePVE(t)
	f.on("PUT /nodes/pve1/qemu/100/config", nil)

	if err := f.client().RemoveDisk("web-prod", "scsi1"); err != nil {
		t.Fatalf("RemoveDisk: %v", err)
	}
	// Detach is a config delete, not a DELETE endpoint: the disk entry is a
	// config key, and removing the key removes the disk.
	call := f.lastCall("PUT", "/nodes/pve1/qemu/100/config")
	if got := call.Form.Get("delete"); got != "scsi1" {
		t.Errorf("delete = %q, want scsi1", got)
	}
}

func TestAttachGPUSendsHostpci(t *testing.T) {
	f := newFakePVE(t)
	f.on("PUT /nodes/pve1/qemu/100/config", nil)

	if err := f.client().AttachGPU("web-prod", "0000:01:00.0", ""); err != nil {
		t.Fatalf("AttachGPU: %v", err)
	}
	call := f.lastCall("PUT", "/nodes/pve1/qemu/100/config")
	if got := call.Form.Get("hostpci0"); got != "0000:01:00.0,pcie=1" {
		t.Errorf("hostpci0 = %q, want the PCI ID with pcie=1", got)
	}
}

func TestDetachGPURemovesTheSlot(t *testing.T) {
	f := newFakePVE(t)
	f.on("PUT /nodes/pve1/qemu/100/config", nil)

	if err := f.client().DetachGPU("web-prod", ""); err != nil {
		t.Fatalf("DetachGPU: %v", err)
	}
	call := f.lastCall("PUT", "/nodes/pve1/qemu/100/config")
	if got := call.Form.Get("delete"); got != "hostpci0" {
		t.Errorf("delete = %q, want hostpci0", got)
	}
}

// ── export ────────────────────────────────────────────────────────

// exportCluster scripts the whole ExportDisk pipeline: a shared NFS backup
// storage plus a local dir, a vzdump task that succeeds at once, two backups
// where the newer sorts last, and a download serving raw bytes.
func exportCluster(t *testing.T, archive []byte) *fakePVE {
	t.Helper()
	f := newFakePVE(t)
	f.on("GET /storage", []map[string]any{
		{"storage": "backup-nfs", "type": "nfs", "content": "backup,images", "shared": 1},
		{"storage": "local", "type": "dir", "content": "backup,iso", "shared": 0},
	})
	upid := "UPID:pve1:0:0:0:vzdump:100:corral@pve:"
	f.on("POST /nodes/pve1/vzdump", upid)
	f.on("GET /nodes/pve1/tasks/"+upid+"/status",
		map[string]any{"status": "stopped", "exitstatus": "OK", "type": "vzdump"})
	f.on("GET /storage/backup-nfs/content", []map[string]any{
		{"volid": "backup-nfs:backup/vzdump-qemu-100-2025_06_01-00_00_00.vma.zst", "content": "backup"},
		{"volid": "backup-nfs:backup/vzdump-qemu-100-2025_09_12-04_00_00.vma.zst", "content": "backup"},
		{"volid": "backup-nfs:iso/debian-12.iso", "content": "iso"},
		{"volid": "backup-nfs:backup/vzdump-qemu-101-2025_09_12-04_00_00.vma.zst", "content": "backup"},
	})
	f.onRaw("GET /nodes/pve1/storage/backup-nfs/download", archive)
	return f
}

func TestExportDiskRunsVzdumpAndDownloadsTheLatest(t *testing.T) {
	archive := []byte("fake-vma-bytes")
	f := exportCluster(t, archive)
	dest := filepath.Join(t.TempDir(), "web-prod.vma.zst")

	if err := f.client().ExportDisk("web-prod", dest); err != nil {
		t.Fatalf("ExportDisk: %v", err)
	}
	// vzdump runs in snapshot mode: the only mode that costs no downtime.
	call := f.lastCall("POST", "/nodes/pve1/vzdump")
	if call.Form.Get("mode") != "snapshot" {
		t.Errorf("mode = %q, want snapshot", call.Form.Get("mode"))
	}
	if call.Form.Get("storage") != "backup-nfs" {
		t.Errorf("storage = %q, want the shared backup storage", call.Form.Get("storage"))
	}
	if call.Form.Get("vmid") != "100" {
		t.Errorf("vmid = %q, want 100", call.Form.Get("vmid"))
	}
	// The newest backup for vmid 100 wins; the ISO, the other guest, and the
	// older backup must not.
	dl := f.lastCall("GET", "/nodes/pve1/storage/backup-nfs/download")
	if got := dl.Query.Get("volume"); got != "backup-nfs:backup/vzdump-qemu-100-2025_09_12-04_00_00.vma.zst" {
		t.Errorf("volume = %q, want the latest backup for vmid 100", got)
	}
	if got := dl.Query.Get("content"); got != "backup" {
		t.Errorf("content = %q, want backup", got)
	}
	// The archive is streamed to disk, not buffered: multi-gigabyte images.
	if data, err := os.ReadFile(dest); err != nil || string(data) != string(archive) {
		t.Errorf("dest = %q, %v — want the served bytes", data, err)
	}
}

func TestExportDiskPrefersSharedStorage(t *testing.T) {
	f := newFakePVE(t)
	f.on("GET /storage", []map[string]any{
		{"storage": "local", "type": "dir", "content": "backup", "shared": 0},
		{"storage": "backup-nfs", "type": "nfs", "content": "backup", "shared": 1},
		{"storage": "rbd", "type": "rbd", "content": "backup,images", "shared": 1},
	})

	// Order in the answer must not matter: the shared file-based storage wins
	// over the local dir, and the block storage is never eligible.
	storage, err := f.client().findBackupStorage()
	if err != nil {
		t.Fatalf("findBackupStorage: %v", err)
	}
	if storage.Storage != "backup-nfs" {
		t.Errorf("storage = %q, want backup-nfs", storage.Storage)
	}
}

func TestExportDiskFallsBackToLocalDir(t *testing.T) {
	f := newFakePVE(t)
	f.on("GET /storage", []map[string]any{
		{"storage": "local", "type": "dir", "content": "backup", "shared": 0},
	})

	storage, err := f.client().findBackupStorage()
	if err != nil {
		t.Fatalf("findBackupStorage: %v", err)
	}
	if storage.Storage != "local" {
		t.Errorf("storage = %q, want the local fallback", storage.Storage)
	}
}

func TestExportDiskRefusesWithoutFileStorage(t *testing.T) {
	f := newFakePVE(t)
	f.on("GET /storage", []map[string]any{
		{"storage": "rbd", "type": "rbd", "content": "backup,images", "shared": 1},
		{"storage": "local-lvm", "type": "lvm", "content": "images", "shared": 0},
	})

	_, err := f.client().findBackupStorage()
	if err == nil {
		t.Fatal("expected a refusal: block storages cannot serve downloads")
	}
	if !strings.Contains(err.Error(), "file-based") {
		t.Errorf("refusal %q should say what kind of storage is needed", err)
	}
}

func TestExportDiskFailsWhenVzdumpFails(t *testing.T) {
	f := newFakePVE(t)
	f.on("GET /storage", []map[string]any{
		{"storage": "backup-nfs", "type": "nfs", "content": "backup", "shared": 1},
	})
	upid := "UPID:pve1:0:0:0:vzdump:100:corral@pve:"
	f.on("POST /nodes/pve1/vzdump", upid)
	f.on("GET /nodes/pve1/tasks/"+upid+"/status",
		map[string]any{"status": "stopped", "exitstatus": "boom", "type": "vzdump"})
	f.on("GET /nodes/pve1/tasks/"+upid+"/log", []map[string]any{{"n": 1, "t": "no space left"}})

	err := f.client().ExportDisk("web-prod", filepath.Join(t.TempDir(), "x.vma.zst"))
	if err == nil {
		t.Fatal("expected the vzdump failure")
	}
	if !strings.Contains(err.Error(), "vzdump task failed") {
		t.Errorf("error %q should say the vzdump task failed", err)
	}
}

func TestExportDiskRefusesWhenNoBackupFound(t *testing.T) {
	f := newFakePVE(t)
	f.on("GET /storage", []map[string]any{
		{"storage": "backup-nfs", "type": "nfs", "content": "backup", "shared": 1},
	})
	upid := "UPID:pve1:0:0:0:vzdump:100:corral@pve:"
	f.on("POST /nodes/pve1/vzdump", upid)
	f.on("GET /nodes/pve1/tasks/"+upid+"/status",
		map[string]any{"status": "stopped", "exitstatus": "OK", "type": "vzdump"})
	f.on("GET /storage/backup-nfs/content", []map[string]any{
		{"volid": "backup-nfs:backup/vzdump-qemu-101-2025_09_12-04_00_00.vma.zst", "content": "backup"},
	})

	err := f.client().ExportDisk("web-prod", filepath.Join(t.TempDir(), "x.vma.zst"))
	if err == nil {
		t.Fatal("expected a refusal when no backup matches the guest")
	}
	if !strings.Contains(err.Error(), "no backup found") {
		t.Errorf("error %q should say no backup was found", err)
	}
}

func TestExportDiskFollowsARedirect(t *testing.T) {
	archive := []byte("redirected-bytes")
	f := newFakePVE(t)
	f.on("GET /storage", []map[string]any{
		{"storage": "backup-nfs", "type": "nfs", "content": "backup", "shared": 1},
	})
	upid := "UPID:pve1:0:0:0:vzdump:100:corral@pve:"
	f.on("POST /nodes/pve1/vzdump", upid)
	f.on("GET /nodes/pve1/tasks/"+upid+"/status",
		map[string]any{"status": "stopped", "exitstatus": "OK", "type": "vzdump"})
	f.on("GET /storage/backup-nfs/content", []map[string]any{
		{"volid": "backup-nfs:backup/vzdump-qemu-100-2025_09_12-04_00_00.vma.zst", "content": "backup"},
	})
	direct := f.server.URL + "/api2/json/nodes/pve1/storage/backup-nfs/download-direct"
	f.onRawStatus("GET /nodes/pve1/storage/backup-nfs/download", http.StatusFound, direct, nil)
	f.onRaw("GET /nodes/pve1/storage/backup-nfs/download-direct", archive)
	dest := filepath.Join(t.TempDir(), "web-prod.vma.zst")

	if err := f.client().ExportDisk("web-prod", dest); err != nil {
		t.Fatalf("ExportDisk: %v", err)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != string(archive) {
		t.Errorf("dest = %q, %v — want the bytes from behind the redirect", data, err)
	}

}
