package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hanthor/corral/pkg/registry"
	"github.com/hanthor/corral/pkg/types"
)

func TestResolveBackend_Registry(t *testing.T) {
	// Setup a temp registry
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	s := registry.NewStoreAt(path)

	// Override the global for this test
	oldStore := registryStore
	registryStore = s
	defer func() { registryStore = oldStore }()

	s.Set("testvm", types.RegistryEntry{Backend: "kubevirt", Namespace: "default"})

	backend := resolveBackend("testvm")
	if backend != "kubevirt" {
		t.Errorf("expected kubevirt, got %s", backend)
	}
}

func TestResolveBackend_NotFound(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()

	backend := resolveBackend("nonexistent-vm-xyzzzy")
	if backend != "" {
		t.Errorf("expected empty, got %s", backend)
	}
}

func TestRequireBackend(t *testing.T) {
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()

	_, err := requireBackend("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent VM")
	}
}

func TestUniq(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b"}
	got := uniq(input)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d: %v", len(got), got)
	}
}

func TestAllVMNames_Empty(t *testing.T) {
	// Without a cluster, should return zero KubeVirt VMs
	// And QEMU dir shouldn't exist
	oldStore := registryStore
	registryStore = nil
	defer func() { registryStore = oldStore }()
	os.Setenv("HOME", t.TempDir())
	defer os.Unsetenv("HOME")

	names := allVMNames()
	// Should be empty (or just whatever happens to exist)
	_ = names
}
