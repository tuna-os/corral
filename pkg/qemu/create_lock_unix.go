//go:build unix

package qemu

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes an exclusive advisory lock without blocking, released
// by unlockFile. Unix only: flock(2) exists on Linux and macOS; other
// platforms fall back to the in-process slot alone (see
// create_lock_other.go). Callers poll this through lockFlockCtx's bounded
// wait: EWOULDBLOCK means "held", anything else is a real failure.
func tryLockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// lockContended reports whether a tryLockFile error means the lock is held
// (retry) rather than broken (fail).
func lockContended(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
