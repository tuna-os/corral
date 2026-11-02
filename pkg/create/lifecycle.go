package create

import (
	"time"
)

// maybeStartAndWait handles --start / --wait-ssh for QEMU-backed VMs.
// --wait-ssh implies --start and turns creation into a CI gate: the process
// exits nonzero unless the guest answers SSH within --timeout seconds.
func (r request) maybeStartAndWait(name string) error {
	if !r.Start && !r.WaitSSH {
		return nil
	}
	if err := r.QEMUStart(name); err != nil {
		return err
	}
	if !r.WaitSSH {
		return nil
	}
	return r.QEMUWaitSSH(name, r.SSHUser, time.Duration(r.Timeout)*time.Second)
}
