package kubevirt

import (
	"fmt"
	"sort"
	"time"

	"github.com/tuna-os/corral/pkg/types"
)

// stoppedAtAnnotation records when gc (not the user) stopped an ephemeral VM,
// so a later gc pass can tell "stopped by TTL expiry, past the grace period"
// apart from "the user stopped it themselves and might restart it".
const stoppedAtAnnotation = "corral.dev/gc-stopped-at"

// GCDefaultDeleteAfter is how long an ephemeral VM sits stopped (PVCs
// intact) before gc deletes it outright. Generous on purpose: stopping
// reclaims the scarce resource (cluster CPU/RAM) immediately, so there's
// little pressure to rush the destructive step.
const GCDefaultDeleteAfter = 72 * time.Hour

// GCResult reports what GC did (or, in dry-run, would do) this pass.
type GCResult struct {
	Stopped []string // running past ExpiresAt -> stopped, PVCs kept
	Deleted []string // stopped by gc past deleteAfter -> fully deleted
	Errors  []string // "<vm>: <error>"
}

// GCAction is one VM's verdict from planGC: what stage it's due for, if any.
type GCAction int

const (
	GCNone GCAction = iota
	GCStop
	GCDelete
)

// planGC is the pure decision core (mirrors parseVMList's split from
// ListVMs): given the current VM list and clock, which ephemeral VMs are due
// to be stopped (TTL expired) or deleted (gc-stopped past deleteAfter)? No
// exec, no mutation — unit-testable without a cluster.
func planGC(vms []types.VM, now time.Time, deleteAfter time.Duration) map[string]GCAction {
	plan := make(map[string]GCAction)
	for _, vm := range vms {
		if !vm.Ephemeral {
			continue
		}

		if vm.Running {
			expiresAt, err := time.Parse(time.RFC3339, vm.ExpiresAt)
			if err != nil || now.Before(expiresAt) {
				continue // no/invalid expiry, or not due yet — leave it alone
			}
			plan[vm.Name] = GCStop
			continue
		}

		// Not running: only delete if *gc* stopped it (not the user), and
		// only past the separate, longer delete grace period.
		if vm.StoppedAt == "" {
			continue
		}
		stoppedAt, err := time.Parse(time.RFC3339, vm.StoppedAt)
		if err != nil || now.Before(stoppedAt.Add(deleteAfter)) {
			continue
		}
		plan[vm.Name] = GCDelete
	}
	return plan
}

// GC stops ephemeral VMs whose TTL (corral.dev/expires-at) has passed, and
// deletes ephemeral VMs gc previously stopped once they've sat idle past
// deleteAfter. Two stages on purpose (see GCDefaultDeleteAfter): stopping is
// cheap and reversible (`corral start`), so it happens as soon as the TTL
// expires; deleting destroys PVCs, so it waits for a much longer, separate
// grace period. dryRun reports planned actions without touching anything.
func GC(dryRun bool, deleteAfter time.Duration) (GCResult, error) {
	var result GCResult

	vms, err := NewClient("").ListVMs()
	if err != nil {
		return result, fmt.Errorf("listing VMs: %w", err)
	}
	vmByName := make(map[string]types.VM, len(vms))
	for _, vm := range vms {
		vmByName[vm.Name] = vm
	}

	now := time.Now()
	plan := planGC(vms, now, deleteAfter)
	names := make([]string, 0, len(plan))
	for name := range plan {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		vm := vmByName[name]
		switch plan[name] {
		case GCStop:
			if dryRun {
				result.Stopped = append(result.Stopped, name)
				continue
			}
			c := NewClient(vm.Namespace)
			if err := c.StopVM(name); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: stop: %v", name, err))
				continue
			}
			if _, err := c.runner().Run("kubectl", "annotate", "vm", name, "-n", vm.Namespace,
				stoppedAtAnnotation+"="+now.Format(time.RFC3339), "--overwrite"); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: annotate: %v", name, err))
			}
			result.Stopped = append(result.Stopped, name)

		case GCDelete:
			if dryRun {
				result.Deleted = append(result.Deleted, name)
				continue
			}
			if err := NewClient(vm.Namespace).DeleteVM(name); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: delete: %v", name, err))
				continue
			}
			result.Deleted = append(result.Deleted, name)
		}
	}

	return result, nil
}
