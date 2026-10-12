package proxmoxbe

// The content API reports storage-prefixed volume ids
// ("backup-nfs:backup/vzdump-qemu-100-....vma.zst"), not bare file names.
// Matching on the whole id found nothing on a real cluster, so every export
// failed with "no backup found" — match on the file name, return the full id
// the download endpoint wants.
import "testing"

func TestFindLatestBackupMatchesStoragePrefixedIDs(t *testing.T) {
	f := newFakePVE(t)
	f.on("GET /storage/backup-nfs/content", []map[string]any{
		{"volid": "backup-nfs:backup/vzdump-qemu-100-2025_06_01-00_00_00.vma.zst", "content": "backup"},
		{"volid": "backup-nfs:backup/vzdump-qemu-100-2025_09_12-04_00_00.vma.zst", "content": "backup"},
		{"volid": "backup-nfs:backup/vzdump-qemu-101-2025_09_12-04_00_00.vma.zst", "content": "backup"},
		{"volid": "backup-nfs:iso/debian-12.iso", "content": "iso"},
	})

	got, err := f.client().findLatestBackup("backup-nfs", 100)
	if err != nil {
		t.Fatalf("findLatestBackup: %v", err)
	}
	// The newest backup for the guest, with the storage prefix intact: the
	// download endpoint wants the complete id, not the file name.
	if want := "backup-nfs:backup/vzdump-qemu-100-2025_09_12-04_00_00.vma.zst"; got != want {
		t.Errorf("latest = %q, want %q", got, want)
	}
}

func TestFindLatestBackupMatchesBareIDs(t *testing.T) {
	f := newFakePVE(t)
	f.on("GET /storage/local/content", []map[string]any{
		{"volid": "vzdump-lxc-200-2025_09_12-04_00_00.tar.zst", "content": "backup"},
	})

	got, err := f.client().findLatestBackup("local", 200)
	if err != nil {
		t.Fatalf("findLatestBackup: %v", err)
	}
	if want := "vzdump-lxc-200-2025_09_12-04_00_00.tar.zst"; got != want {
		t.Errorf("latest = %q, want %q", got, want)
	}
}
