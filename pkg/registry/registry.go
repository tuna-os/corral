package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/types"
)

// Store manages VM registry persistence.
type Store struct {
	path string
	mu   sync.Mutex
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

// Ping verifies the registry's storage is actually usable: its parent
// directory exists (or can be created) and is writable. It does not read or
// modify registry.json itself, so it is safe to call from a readiness probe
// on every request. A nil receiver (store never initialized) is reported as
// unready by the caller before Ping is reached.
//
// readAll/writeAll swallow their own I/O errors (see below) so neither one
// can be used to detect a stale mount or permission problem after startup;
// Ping exists specifically to catch what they hide.
func (s *Store) Ping() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	probe, err := os.CreateTemp(dir, ".ping-*.tmp")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		return err
	}
	return os.Remove(name)
}

// Get retrieves a VM's registry entry.
func (s *Store) Get(name string) (types.RegistryEntry, bool) {
	reg := s.readAll()
	e, ok := reg[name]
	return e, ok
}

// Set stores a registry entry for a VM.
func (s *Store) Set(name string, entry types.RegistryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.readAll()
	reg[name] = entry
	return s.writeAll(reg)
}

// Remove deletes a VM's registry entry.
func (s *Store) Remove(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.readAll()
	delete(reg, name)
	return s.writeAll(reg)
}

// SetRef stores an entry under its canonical scoped identity. Existing unscoped
// data is usable only when its recorded identity establishes exact ownership.
func (s *Store) SetRef(ref types.InstanceRef, entry types.RegistryEntry) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	entry.Backend, entry.Context, entry.Namespace, entry.Peer = ref.Backend, ref.Context, ref.Namespace, ref.Peer
	return s.Set(ref.String(), entry)
}

func (s *Store) GetRef(ref types.InstanceRef) (types.RegistryEntry, bool) {
	if entry, ok := s.Get(ref.String()); ok {
		return entry, true
	}
	// A legacy name is usable only when its recorded ownership matches exactly.
	// In particular, an absent context is not evidence of today's default cluster.
	entry, ok := s.Get(ref.Name)
	if !ok || !ownsRef(entry, ref) {
		return types.RegistryEntry{}, false
	}
	return entry, true
}

// RemoveRef retires scoped data and legacy data whose recorded ownership is
// exact. Unbound legacy credentials are retained, never assigned to the current
// default cluster merely because an instance there has the same name.
func (s *Store) RemoveRef(ref types.InstanceRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.readAll()
	delete(reg, ref.String())
	if entry, ok := reg[ref.Name]; ok && ownsRef(entry, ref) {
		delete(reg, ref.Name)
	}
	return s.writeAll(reg)
}

func ownsRef(entry types.RegistryEntry, ref types.InstanceRef) bool {
	if entry.Context == "" && entry.Backend != "qemu" {
		return false
	}
	return entry.Peer == ref.Peer &&
		backend.Ref(entry.Backend, entry.Context, entry.Namespace, ref.Name) ==
			backend.Ref(ref.Backend, ref.Context, ref.Namespace, ref.Name)
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
	// Write+rename keeps readers from observing truncated JSON. The registry can
	// contain cloud-init passwords, so both temporary and final files are 0600.
	tmp, err := os.CreateTemp(dir, ".registry-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}
