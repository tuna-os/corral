//go:build !unix

package qemu

import "os/exec"

// isolateBoundedCmd has no process group to create off Unix: the child
// keeps the caller's group, so the retirement kill below cannot address
// descendants without risking strangers.
func isolateBoundedCmd(cmd *exec.Cmd) {}

// killBounded kills the bounded child's leader only: without a private
// group there is no safe descendant signal. WaitDelay (see bounded.go)
// still bounds the wait — pipe-holding descendants cannot wedge it past
// the delay — but on this platform they may survive as orphans where Unix
// retires them.
func killBounded(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
