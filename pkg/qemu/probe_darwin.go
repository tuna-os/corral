//go:build darwin

package qemu

import (
	"bytes"
	"fmt"

	"golang.org/x/sys/unix"
)

// Native macOS process-identity readers (see direct.go for the model).
// macOS has no /proc, so identity comes from sysctl kern.proc.pid.<pid>:
// the kernel start time (immutable per process lifetime — reuse always
// changes it) plus the kernel-recorded executable name. The comm is the
// basename the kernel holds, not argv[0], so it cannot be spoofed by an
// argument that merely mentions QEMU.

// darwinZombie is the P_stat value for a zombie (SZOMB in sys/proc.h).
const darwinZombie = 5

func kinfoForPID(pid int) (*unix.KinfoProc, error) {
	return unix.SysctlKinfoProc("kern.proc.pid", pid)
}

// nativeExeID reports "comm:<kernel executable basename>" for pid. The
// kernel truncates comm to 16 bytes; the matcher in direct.go applies the
// same truncation to the recorded binary's basename.
func nativeExeID(pid int) (string, error) {
	kp, err := kinfoForPID(pid)
	if err != nil {
		return "", err
	}
	comm := string(bytes.TrimRight(kp.Proc.P_comm[:], "\x00"))
	if comm == "" {
		return "", fmt.Errorf("empty comm for pid %d", pid)
	}
	return "comm:" + comm, nil
}

// nativeStartID reports the kernel process start time (seconds.microseconds)
// for pid.
func nativeStartID(pid int) (string, error) {
	kp, err := kinfoForPID(pid)
	if err != nil {
		return "", err
	}
	start := kp.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", start.Sec, start.Usec), nil
}

// nativeIsZombie reports whether pid is a zombie (dead but unreaped).
func nativeIsZombie(pid int) bool {
	kp, err := kinfoForPID(pid)
	if err != nil {
		return false
	}
	return kp.Proc.P_stat == darwinZombie
}
