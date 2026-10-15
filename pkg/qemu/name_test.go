package qemu

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

func TestValidateVMName(t *testing.T) {
	for _, ok := range []string{"vm1", "a", "Tuna-v1.2_x", strings.Repeat("n", 64)} {
		if err := ValidateVMName(ok); err != nil {
			t.Errorf("ValidateVMName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", ".", "..", "../escape", "a/b", "/abs", `a\b`, "nul\x00x", ".hidden",
		"-flag", "has space", "semi;colon", strings.Repeat("n", 65), "cache",
	} {
		if err := ValidateVMName(bad); !errors.Is(err, ErrInvalidVMName) {
			t.Errorf("ValidateVMName(%q) = %v, want ErrInvalidVMName", bad, err)
		}
	}
}

// TestCreate_RefusesTraversalName: neither creation entry point may make
// or write a directory outside VMHome.
func TestCreate_RefusesTraversalName(t *testing.T) {
	home := t.TempDir()
	vms := filepath.Join(home, "vms")
	SetStateDirs(vms, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })

	if err := RecordCreating("../escape", types.CreateOpts{}); !errors.Is(err, ErrInvalidVMName) {
		t.Errorf("RecordCreating = %v, want ErrInvalidVMName", err)
	}
	if err := Create(types.CreateOpts{Name: "../escape"}); !errors.Is(err, ErrInvalidVMName) {
		t.Errorf("Create = %v, want ErrInvalidVMName", err)
	}
	if _, err := os.Stat(filepath.Join(home, "escape")); !os.IsNotExist(err) {
		t.Errorf("a directory was created outside VMHome: %v", err)
	}
}

// TestLifecycle_RefusesTraversalName: Delete must never remove VMHome's
// parent or a sibling of it, and Start/Stop refuse the same names.
func TestLifecycle_RefusesTraversalName(t *testing.T) {
	home := t.TempDir()
	vms := filepath.Join(home, "vms")
	victim := filepath.Join(home, "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	SetStateDirs(vms, t.TempDir())
	t.Cleanup(func() { SetStateDirs("", "") })

	for _, name := range []string{"..", "../victim", "cache", ""} {
		if err := Delete(name); !errors.Is(err, ErrInvalidVMName) {
			t.Errorf("Delete(%q) = %v, want ErrInvalidVMName", name, err)
		}
		if err := Start(name); !errors.Is(err, ErrInvalidVMName) {
			t.Errorf("Start(%q) = %v, want ErrInvalidVMName", name, err)
		}
		if err := Stop(name); !errors.Is(err, ErrInvalidVMName) {
			t.Errorf("Stop(%q) = %v, want ErrInvalidVMName", name, err)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("Delete removed a directory outside VMHome: %v", err)
	}
}
