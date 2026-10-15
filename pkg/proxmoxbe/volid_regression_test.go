package proxmoxbe

// The content API reports storage-prefixed volume ids, and main already pins
// that shape (TestFindLatestBackup_MatchesStoragePrefixedVolumeIDs). This
// pins the other shape the matcher must keep working on: a bare file name
// with no storage prefix (LXC tarball here), matched the same way.
import "testing"

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
