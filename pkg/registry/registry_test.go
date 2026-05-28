package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanthor/tailvm-go/pkg/types"
)

func TestNewStoreAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	s := NewStoreAt(path)

	_, ok := s.Get("nonexistent")
	if ok {
		t.Error("expected false for nonexistent key")
	}
}

func TestSetAndGet(t *testing.T) {
	s := NewStoreAt(filepath.Join(t.TempDir(), "registry.json"))

	entry := types.RegistryEntry{Backend: "kubevirt", Namespace: "default"}
	if err := s.Set("testvm", entry); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, ok := s.Get("testvm")
	if !ok {
		t.Fatal("expected to find testvm")
	}
	if got.Backend != "kubevirt" {
		t.Errorf("expected backend kubevirt, got %s", got.Backend)
	}
	if got.Namespace != "default" {
		t.Errorf("expected namespace default, got %s", got.Namespace)
	}
}

func TestRemove(t *testing.T) {
	s := NewStoreAt(filepath.Join(t.TempDir(), "registry.json"))

	s.Set("vm1", types.RegistryEntry{Backend: "qemu"})
	s.Set("vm2", types.RegistryEntry{Backend: "kubevirt"})

	if err := s.Remove("vm1"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	_, ok := s.Get("vm1")
	if ok {
		t.Error("vm1 should be removed")
	}
	_, ok = s.Get("vm2")
	if !ok {
		t.Error("vm2 should still exist")
	}
}

func TestNames(t *testing.T) {
	s := NewStoreAt(filepath.Join(t.TempDir(), "registry.json"))

	s.Set("a", types.RegistryEntry{Backend: "qemu"})
	s.Set("b", types.RegistryEntry{Backend: "kubevirt"})
	s.Set("c", types.RegistryEntry{Backend: "qemu"})

	names := s.Names()
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
}

func TestAll(t *testing.T) {
	s := NewStoreAt(filepath.Join(t.TempDir(), "registry.json"))

	s.Set("vm1", types.RegistryEntry{Backend: "qemu"})
	s.Set("vm2", types.RegistryEntry{Backend: "kubevirt", Namespace: "ns1"})

	all := s.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
}

func TestPersistsToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	s := NewStoreAt(path)

	s.Set("vm1", types.RegistryEntry{Backend: "kubevirt"})

	// Read file directly
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read registry file: %v", err)
	}
	var reg map[string]types.RegistryEntry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if reg["vm1"].Backend != "kubevirt" {
		t.Error("backend not persisted")
	}
}
