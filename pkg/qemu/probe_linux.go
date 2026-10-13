//go:build linux

package qemu

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Native Linux process-identity readers (see direct.go for the model).
// The executable comes from /proc/pid/exe — the kernel-resolved binary the
// process actually runs, immune to argv[0] spoofing — and the start identity
// from the starttime field of /proc/pid/stat. A pid plus its starttime names
// exactly one process lifetime: reuse always changes the starttime.

// nativeExeID reports "exe:<resolved path>" for pid.
func nativeExeID(pid int) (string, error) {
	link, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(link); err == nil {
		link = resolved
	}
	return "exe:" + link, nil
}

// nativeStartID reports the starttime field of /proc/pid/stat for pid.
func nativeStartID(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx < 0 {
		return "", fmt.Errorf("unparsable stat")
	}
	fields := strings.Fields(s[idx+2:])
	// fields[0] is the state (original field 3), so starttime (original
	// field 22) is fields[19].
	if len(fields) < 20 {
		return "", fmt.Errorf("short stat")
	}
	return fields[19], nil
}

// nativeIsZombie reports whether pid is a zombie (dead but unreaped). A
// zombie still answers signal 0, so without this check a QEMU that exited
// on a bad flag would look like a VM that is simply slow.
func nativeIsZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(data)
	// comm may contain spaces and parens; the state follows the last ")".
	if idx := strings.LastIndex(s, ")"); idx >= 0 {
		if rest := strings.Fields(s[idx+2:]); len(rest) > 0 && rest[0] == "Z" {
			return true
		}
		return false
	}
	fields := strings.Fields(s)
	return len(fields) > 2 && fields[2] == "Z"
}
