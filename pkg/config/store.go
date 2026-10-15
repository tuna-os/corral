package config

import (
	"errors"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// store caches the on-disk bytes of the default config file so the many
// read helpers in this package (Contexts, Peers, DefaultContext, ...) don't
// each pay a fresh os.ReadFile + YAML parse when called back-to-back for the
// same logical operation. Every read still unmarshals its own *Config from
// the cached bytes, so callers get an independent value exactly as they did
// before caching existed — no *Config is ever shared or mutated by two
// callers.
//
// The cache is revalidated with an os.Stat on every read: if the file was
// replaced, rewritten, created or deleted since it was cached (another
// corral process, a hand edit, a test writing config.yaml directly), the
// next read reloads it. A long-running `corral web` therefore sees changes
// made by a concurrent `corral context add` without a restart.
//
// store.mu serializes the load-mutate-save span of every mutating helper
// (see mutate below) within this process, and lockConfigDir extends that
// to other corral processes on platforms with flock, so two concurrent
// mutations can't both load the same on-disk state and have one silently
// overwrite the other's Save.
//
// Explicit-path calls to Load bypass this cache entirely: tests and callers
// that pass their own path expect an always-fresh read of that file, not
// the cached "default config file" bytes.
//
// The zero value is ready to use.
var store configStore

type configStore struct {
	mu    sync.Mutex
	path  string      // resolved DefaultPath() these bytes were read for
	info  os.FileInfo // stat of path when bytes were read; nil if it didn't exist
	bytes []byte      // nil means "file does not exist" once valid is true
	valid bool
}

// loadDefault returns a freshly unmarshalled *Config for DefaultPath(),
// reading the file only if the cached copy is stale.
func (s *configStore) loadDefault() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// loadLocked is loadDefault's body. Callers must already hold s.mu.
func (s *configStore) loadLocked() (*Config, error) {
	path := DefaultPath()
	info, statErr := statTolerant(path)
	if statErr != nil || !s.freshLocked(path, info) {
		// Stat before read: if the file changes in between, the cached
		// info is older than the bytes, so the next read reloads again
		// rather than serving stale bytes.
		data, notFound, err := readFileTolerant(path)
		if err != nil {
			return nil, err
		}
		if notFound {
			data, info = nil, nil
		}
		s.bytes, s.path, s.info, s.valid = data, path, info, true
	}
	return unmarshalConfig(s.bytes)
}

// freshLocked reports whether the cached bytes still match path's current
// stat. Callers must already hold s.mu.
func (s *configStore) freshLocked(path string, info os.FileInfo) bool {
	if !s.valid || s.path != path {
		return false
	}
	if info == nil || s.info == nil {
		return info == nil && s.info == nil
	}
	return os.SameFile(s.info, info) &&
		s.info.ModTime().Equal(info.ModTime()) &&
		s.info.Size() == info.Size()
}

// saveLocked marshals and writes cfg to DefaultPath() and updates the
// cache to match. Callers must already hold s.mu and the config dir lock.
func (s *configStore) saveLocked(cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	path := DefaultPath()
	if err := writeFileAtomic(path, data); err != nil {
		return err
	}
	info, err := statTolerant(path)
	if err != nil || info == nil {
		// The write landed but can't be stat'ed; drop the cache so the
		// next read goes to disk.
		s.valid = false
		return nil
	}
	s.bytes, s.path, s.info, s.valid = data, path, info, true
	return nil
}

// withLock runs fn holding store.mu and the cross-process config dir lock.
func withLock(fn func() error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	unlock, err := lockConfigDir(ConfigDir())
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// mutate runs fn under the store's lock: it loads the current default
// config, passes it to fn to modify in place (or fn may ignore it and
// build its own), and saves whatever fn returns. The whole load-mutate-save
// span is one critical section, so two concurrent mutate calls (from two
// web request handlers, or two corral processes) serialize instead of one
// silently discarding the other's change. fn must not call back into this
// package's exported helpers: the lock is not reentrant.
func mutate(fn func(cfg *Config) (*Config, error)) error {
	return withLock(func() error {
		cfg, err := store.loadLocked()
		if err != nil {
			return err
		}
		next, err := fn(cfg)
		if err != nil {
			return err
		}
		return store.saveLocked(next)
	})
}

// Update applies fn to the current config and saves the result, holding the
// store's locks across the whole load-modify-save span. Use it instead of a
// Load+Save pair outside this package so concurrent writers can't lose each
// other's changes. fn must not call back into this package's exported
// helpers: the lock is not reentrant.
func Update(fn func(cfg *Config) error) error {
	return mutate(func(cfg *Config) (*Config, error) {
		return cfg, fn(cfg)
	})
}

// statTolerant stats path, mapping "does not exist" (and ENOSYS on
// platforms with no filesystem, such as the js/wasm browser demo) to a
// nil FileInfo and nil error.
func statTolerant(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, errors.ErrUnsupported) {
			return nil, nil
		}
		return nil, err
	}
	return info, nil
}

func readFileTolerant(path string) (data []byte, notFound bool, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		// A platform with no filesystem has no config file either: the
		// browser demo (js/wasm, #284) gets ENOSYS for every file call.
		if os.IsNotExist(err) || errors.Is(err, errors.ErrUnsupported) {
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
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
