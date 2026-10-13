//go:build unix

package qemu

import (
	"os/exec"
	"syscall"
)

// isolateBoundedCmd puts the bounded child in its own process group, so the
// retirement kill can address the leader's whole descendant tree (which
// inherits the group) with one group signal.
func isolateBoundedCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killBounded SIGKILLs the bounded child's process group: with Setpgid the
// leader's pid is the group's id, so the negative-pid signal reaches the
// leader and every descendant still in the group — including ones holding
// the stdout/stderr pipes open. ESRCH means the group is already gone.
func killBounded(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
