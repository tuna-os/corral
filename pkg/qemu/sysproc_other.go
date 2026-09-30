//go:build !unix

package qemu

import "syscall"

// newSessionAttr has no session to create off Unix. The only such build is
// the js/wasm demo (#284), which never starts a real QEMU.
func newSessionAttr() *syscall.SysProcAttr {
	return nil
}
