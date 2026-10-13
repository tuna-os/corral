//go:build unix

package qemu

import "syscall"

// freeBytesOnDir reports free bytes on the filesystem holding dir, for the
// unprivileged user (Bavail, not Bfree — reserved blocks are not ours).
func freeBytesOnDir(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
