//go:build !unix

package qemu

import "os"

// Non-Unix fallback: no flock(2) exists here, so cross-process creation
// races are not excluded — only the in-process mutex in create_lock.go
// applies. The only such build is the js/wasm demo, which never creates a
// real VM; every real host (Linux/macOS) is Unix.
func tryLockFile(f *os.File) error {
	return nil
}

func lockContended(err error) bool {
	return false
}

func unlockFile(f *os.File) error {
	return nil
}
