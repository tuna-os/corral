package registry

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/hanthor/corral/pkg/types"
)

// Store manages VM registry persistence.
type Store struct {
	path string
}

// NewStore creates a registry at the default location (~/.local/share/corral/registry.json).
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(home, ".local", "share", "corral", "registry.json")}, nil
}

// NewStoreAt creates a registry at a custom path (useful for testing).
func NewStoreAt(path string) *Store {
	return &Store{path: path}
}

// Get retrieves a VM's registry entry.
func (s *Store) Get(name string) (types.RegistryEntry, bool) {
	reg := s.readAll()
	e, ok := reg[name]
	return e, ok
}

// Set stores a registry entry for a VM.
func (s *Store) Set(name string, entry types.RegistryEntry) error {
	reg := s.readAll()
	reg[name] = entry
	return s.writeAll(reg)
}

// Remove deletes a VM's registry entry.
func (s *Store) Remove(name string) error {
	reg := s.readAll()
	delete(reg, name)
	return s.writeAll(reg)
}

// All returns all registry entries.
func (s *Store) All() map[string]types.RegistryEntry {
	return s.readAll()
}

// Names returns all registered VM names.
func (s *Store) Names() []string {
	reg := s.readAll()
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	return names
}

func (s *Store) readAll() map[string]types.RegistryEntry {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return make(map[string]types.RegistryEntry)
	}
	var reg map[string]types.RegistryEntry
	if json.Unmarshal(data, &reg) != nil {
		return make(map[string]types.RegistryEntry)
	}
	return reg
}

func (s *Store) writeAll(reg map[string]types.RegistryEntry) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	// 0600: entries carry cloud-init passwords
	return os.WriteFile(s.path, data, 0600)
}
