package qemu

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidVMName marks a VM name refused before it reaches the
// filesystem. Callers that serve requests map it to a client error.
var ErrInvalidVMName = errors.New("invalid VM name")

// vmNamePattern is the shape of a new VM name: it becomes a directory under
// VMHome, the systemd unit corral-<name>.service, and the QEMU -name value,
// so it is one plain path component that is also a valid unit-name segment.
var vmNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// reservedVMNames are directories VMHome holds that are not VMs.
var reservedVMNames = map[string]bool{"cache": true}

// ValidateVMName checks a name for a new VM. Create and RecordCreating
// refuse anything else, so a request can never point VM state, or the
// cleanup of a failed create, outside VMHome.
func ValidateVMName(name string) error {
	if err := CheckVMName(name); err != nil {
		return err
	}
	if !vmNamePattern.MatchString(name) {
		return fmt.Errorf("%w %q: use 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", ErrInvalidVMName, name)
	}
	return nil
}

// CheckVMName is the guard for operations on a VM that may already exist
// (lifecycle, info, console, snapshots): the name must be exactly one
// directory below VMHome and not one of its reserved directories. It is
// looser than ValidateVMName so VMs created before that check existed stay
// manageable.
func CheckVMName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: the name is empty", ErrInvalidVMName)
	case name == "." || name == "..":
		return fmt.Errorf("%w %q", ErrInvalidVMName, name)
	case strings.ContainsAny(name, `/\`+"\x00"):
		return fmt.Errorf("%w %q: the name must not contain a path separator", ErrInvalidVMName, name)
	case reservedVMNames[name]:
		return fmt.Errorf("%w %q: the name is reserved", ErrInvalidVMName, name)
	}
	return nil
}
