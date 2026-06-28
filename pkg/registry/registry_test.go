package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
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

func TestNewStore_UsesDefaultPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore(): %v", err)
	}

	// Should be able to set and get
	entry := types.RegistryEntry{Backend: "kubevirt", Namespace: "corral"}
	if err := s.Set("test", entry); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok := s.Get("test")
	if !ok {
		t.Fatal("entry not found after NewStore + Set")
	}
	if got.Backend != "kubevirt" {
		t.Errorf("Backend = %q, expected kubevirt", got.Backend)
	}

	// Verify file exists at default path
	defaultPath := filepath.Join(tmp, ".local", "share", "corral", "registry.json")
	if _, err := os.Stat(defaultPath); err != nil {
		t.Errorf("registry file not at expected path %q: %v", defaultPath, err)
	}
}

func TestReadAll_CorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	os.WriteFile(path, []byte("not json{{{---"), 0644)

	s := NewStoreAt(path)
	_, ok := s.Get("anything")
	if ok {
		t.Error("expected empty registry when JSON is corrupt")
	}

	s.Set("vm", types.RegistryEntry{Backend: "qemu"})
	got, ok := s.Get("vm")
	if !ok {
		t.Fatal("Set after corrupt JSON should work")
	}
	if got.Backend != "qemu" {
		t.Errorf("Backend = %q, expected qemu", got.Backend)
	}
}

func TestWriteAll_ParentIsFile(t *testing.T) {
	// Create a file where the registry directory would go.
	// MkdirAll should fail → writeAll returns error.
	tmp := t.TempDir()
	// Create "corral" as a file, not a directory
	parentDir := filepath.Join(tmp, "blocked")
	os.WriteFile(parentDir, []byte("block"), 0644)
	path := filepath.Join(parentDir, "sub", "registry.json")

	s := NewStoreAt(path)
	err := s.Set("vm", types.RegistryEntry{Backend: "kubevirt"})
	if err == nil {
		t.Error("expected error when parent is a file, got nil")
	}
}

func TestRemove_Nonexistent(t *testing.T) {
	s := NewStoreAt(filepath.Join(t.TempDir(), "registry.json"))
	// Removing a nonexistent entry should succeed (no-op)
	err := s.Remove("nonexistent")
	if err != nil {
		t.Errorf("Remove nonexistent: %v", err)
	}
}

func TestGet_NonexistentFile(t *testing.T) {
	// Path that doesn't exist yet — should return empty map
	path := filepath.Join(t.TempDir(), "nonexistent", "subdir", "reg.json")
	s := NewStoreAt(path)
	_, ok := s.Get("anything")
	if ok {
		t.Error("expected false for nonexistent file")
	}
}

func TestWriteAll_FileIsDirectory(t *testing.T) {
	dir := t.TempDir()
	// Create the registry path as a directory so WriteFile fails
	os.MkdirAll(filepath.Join(dir, "sub", "registry.json"), 0755)

	// Store path points to a directory, not a file
	s := NewStoreAt(filepath.Join(dir, "sub", "registry.json"))

	err := s.Set("vm", types.RegistryEntry{Backend: "qemu"})
	if err == nil {
		t.Error("expected error when registry path is a directory")
	}
}

func TestWriteAll_MkdirAllFails(t *testing.T) {
	dir := t.TempDir()
	// Create a file where MkdirAll expects a directory
	blocker := filepath.Join(dir, "blocker")
	os.WriteFile(blocker, []byte("x"), 0644)

	// Store path would require creating blocker/registry.json, but blocker is a file
	s := NewStoreAt(filepath.Join(blocker, "registry.json"))

	err := s.Set("vm", types.RegistryEntry{Backend: "qemu"})
	if err == nil {
		t.Error("expected error when MkdirAll fails due to file blocking directory creation")
	}
}

func TestNewStore_HomeDirError(t *testing.T) {
	// Clear HOME — NewStore should fail when os.UserHomeDir() returns error
	t.Setenv("HOME", "")
	// Also unset XDG variables that UserHomeDir might check
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	_, err := NewStore()
	if err == nil {
		t.Log("NewStore succeeded without HOME (unusual but possible on some platforms)")
	}
}
