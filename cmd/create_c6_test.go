package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/qemu"
)

// The CLI bootc-create path joins and owns the VM dir directly (with a
// RemoveAll failure cleanup), so it must refuse invalid and reserved
// names before touching state. Old code accepted "locks": success
// scattered VM state into the home-level lock namespace, and any failure
// removed every live flock.
func TestRunLocalBootcCreate_LocksRefused(t *testing.T) {
	home := t.TempDir()
	vms := filepath.Join(home, "vms")
	qemu.SetStateDirs(vms, filepath.Join(home, "units"))
	t.Cleanup(func() { qemu.SetStateDirs("", "") })
	locks := filepath.Join(vms, "locks")
	if err := os.MkdirAll(locks, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(locks, "gate.lock")
	if err := os.WriteFile(sentinel, []byte("held"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runLocalBootcCreate("locks"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("bootc create of locks must refuse naming the reservation, got %v", err)
	}
	entries, rerr := os.ReadDir(locks)
	if rerr != nil || len(entries) != 1 || entries[0].Name() != "gate.lock" {
		t.Errorf("refused bootc create must leave the locks namespace untouched (read %v, %v)", entries, rerr)
	}
}
