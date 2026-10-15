//go:build unix

package config

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// lockConfigDir takes an exclusive flock on dir/.config.lock so config
// read-modify-write spans in different corral processes serialize. The
// lock is released when the returned func runs, or when the process exits.
func lockConfigDir(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".config.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	fd := int(f.Fd())
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(fd, syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
