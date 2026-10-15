package snapshot

import (
	"errors"
	"testing"

	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// TestQEMU_RefusesTraversalName: the disk path is VMHome/<name>/disk.qcow2,
// and qemu-img writes snapshots into it — a "../" name must never reach it.
func TestQEMU_RefusesTraversalName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ref := types.InstanceRef{Backend: "qemu", Name: "../victim"}
	q := QEMU{}
	if _, err := q.List(ref); !errors.Is(err, qemu.ErrInvalidVMName) {
		t.Errorf("List = %v, want ErrInvalidVMName", err)
	}
	if _, err := q.Create(ref, "s1"); !errors.Is(err, qemu.ErrInvalidVMName) {
		t.Errorf("Create = %v, want ErrInvalidVMName", err)
	}
	if err := q.Restore(ref, "s1"); !errors.Is(err, qemu.ErrInvalidVMName) {
		t.Errorf("Restore = %v, want ErrInvalidVMName", err)
	}
	if err := q.Delete(ref, "s1"); !errors.Is(err, qemu.ErrInvalidVMName) {
		t.Errorf("Delete = %v, want ErrInvalidVMName", err)
	}
}
