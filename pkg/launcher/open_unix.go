//go:build unix

package launcher

import (
	"os"
	"syscall"
)

// openFileRead opens path read-only without ever blocking on a FIFO: with
// O_NONBLOCK the open itself returns immediately, and the caller refuses
// the non-regular descriptor. (For regular files O_NONBLOCK changes
// nothing about the open; reads usually complete promptly but can still
// block on stalled storage — admission budgets bound the operation, not
// this flag.) Without this, a path swapped for a FIFO between the
// admission stat and the open would hang the open itself, before any
// descriptor check could run.
func openFileRead(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
