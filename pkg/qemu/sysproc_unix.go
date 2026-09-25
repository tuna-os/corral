//go:build unix

package qemu

import "syscall"

// newSessionAttr puts the QEMU child in its own session, so the VM outlives
// the shell that started it.
func newSessionAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
