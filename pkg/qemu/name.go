package qemu

import (
	"fmt"
	"strings"
)

// ValidateVMName requires a single safe state-directory component: nonempty,
// bounded, no path separators, no parent traversal, no absolute paths, and
// not the reserved lifecycle-lock namespace (see below).
// qemu.Create joins the name into VM state paths, the systemd unit name and
// the recorded QEMU argv, so anything that is not one clean component is
// refused before any of those are touched.
func ValidateVMName(name string) error {
	if name == "" {
		return fmt.Errorf("VM name is required")
	}
	// "locks" is the lifecycle-lock directory (<VMHome>/locks holds one
	// permanent flock per VM name): a VM of that name would make Delete
	// unlink every live lock and Create scribble VM state into the lock
	// namespace. Every lifecycle entry validates first, so the refusal
	// holds at Create, Start, Stop, Delete, and the lock itself.
	if name == "locks" {
		return fmt.Errorf("VM name %q is reserved for Corral's lifecycle locks — choose another name", name)
	}
	if len(name) > 64 {
		return fmt.Errorf("VM name %q is too long (%d characters, max 64)", name, len(name))
	}
	if name == "." || name == ".." {
		return fmt.Errorf("VM name %q is not a usable name", name)
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("VM name %q is invalid: use letters, digits, '-', '_' or '.' only (no path separators or traversal)", name)
	}
	if strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return fmt.Errorf("VM name %q must be a single path component", name)
	}
	return nil
}
