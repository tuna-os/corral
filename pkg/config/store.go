package config

import (
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// store caches the on-disk bytes of the default config file so the many
// read helpers in this package (Contexts, Peers, DefaultContext, ...) don't
// each pay a fresh os.ReadFile when called back-to-back for the same
// logical operation. Every read still unmarshals its own *Config from the
// cached bytes, so callers get an independent value exactly as they did
// before caching existed — no *Config is ever shared or mutated by two
// callers.
//
// store.mu additionally serializes the load-mutate-save span of every
// mutating helper (see mutate below), so two concurrent mutations can't
// both load the same on-disk state and have one silently overwrite the
// other's Save.
//
// Explicit-path calls to Load bypass this cache entirely: tests and callers
// that pass their own path expect an always-fresh read of that file, not
// the cached "default config file" bytes.
//
// The zero value is ready to use.
var store configStore

type configStore struct {
	mu    sync.Mutex
	path  string // resolved DefaultPath() these bytes were read for
	bytes []byte // nil means "file does not exist" once valid is true
	valid bool
}

// loadDefault returns a freshly unmarshalled *Config for DefaultPath(),
// reading the file only if it isn't already cached for the current
// DefaultPath() (which can change between calls in tests that alter HOME).
func (s *configStore) loadDefault() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// loadLocked is loadDefault's body. Callers must already hold s.mu.
func (s *configStore) loadLocked() (*Config, error) {
	path := DefaultPath()
	if !s.valid || s.path != path {
		data, notFound, err := readFileTolerant(path)
		if err != nil {
			return nil, err
		}
		if notFound {
			data = nil
		}
		s.bytes, s.path, s.valid = data, path, true
	}
	return unmarshalConfig(s.bytes)
}

// saveLocked marshals and writes cfg to DefaultPath() and updates the
// cache to match. Callers must already hold s.mu.
func (s *configStore) saveLocked(cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	path := DefaultPath()
	if err := writeFileAtomic(path, data); err != nil {
		return err
	}
	s.bytes, s.path, s.valid = data, path, true
	return nil
}

// mutate runs fn under the store's lock: it loads the current default
// config, passes it to fn to modify in place (or fn may ignore it and
// build its own), and saves whatever fn returns. The whole load-mutate-save
// span is one critical section, so two concurrent mutate calls (from two
// CLI invocations, or two web request handlers) serialize instead of one
// silently discarding the other's change.
func mutate(fn func(cfg *Config) (*Config, error)) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	cfg, err := store.loadLocked()
	if err != nil {
		return err
	}
	next, err := fn(cfg)
	if err != nil {
		return err
	}
	return store.saveLocked(next)
}

// invalidate drops the cached bytes so the next read reloads from disk.
// Exported for tests that write config.yaml out from under this package.
func (s *configStore) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.valid = false
	s.bytes, s.path = nil, ""
}

func readFileTolerant(path string) (data []byte, notFound bool, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return data, false, nil
}

func unmarshalConfig(data []byte) (*Config, error) {
	if data == nil {
		return &Config{}, nil
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// writeFileAtomic writes data to path via a temp file plus rename, so a
// crash or concurrent read never observes a partially written config.yaml.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(ConfigDir(), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(ConfigDir(), ".config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
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
	return os.Rename(name, path)
}
