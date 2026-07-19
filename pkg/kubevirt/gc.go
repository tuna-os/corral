package kubevirt

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/types"
)

// stoppedAtAnnotation records when gc (not the user) stopped an ephemeral VM,
// so a later gc pass can tell "stopped by TTL expiry, past the grace period"
// apart from "the user stopped it themselves and might restart it".
const stoppedAtAnnotation = "corral.dev/gc-stopped-at"

// ManagedLabel marks resources corral created (currently disk PVCs). It's the
// safe signal for orphan GC: only labeled PVCs are ever candidates for
// deletion, so an unrelated application PVC that happens to share corral's
// naming shape is never touched. PVCs created before this label existed won't
// carry it and are left alone (delete them by hand once).
const ManagedLabel = "corral.dev/managed"

// GCDefaultDeleteAfter is how long an ephemeral VM sits stopped (PVCs
// intact) before gc deletes it outright. Generous on purpose: stopping
// reclaims the scarce resource (cluster CPU/RAM) immediately, so there's
// little pressure to rush the destructive step.
const GCDefaultDeleteAfter = 72 * time.Hour

// GCResult reports what GC did (or, in dry-run, would do) this pass.
type GCResult struct {
	Stopped      []string // running past ExpiresAt -> stopped, PVCs kept
	Deleted      []string // stopped by gc past deleteAfter -> fully deleted
	OrphanedPVCs []string // "<ns>/<pvc>" disks whose owning VM is gone -> deleted
	Errors       []string // "<vm>: <error>"
}

// pvcSuffixes are the disk-name suffixes corral appends to a VM name when it
// creates PVCs (see DeleteVM / bootcBuildDisk). A PVC named "<base><suffix>"
// belongs to VM "<base>"; if that VM no longer exists, the PVC is an orphan.
var pvcSuffixes = []string{"-bootc-disk", "-disk", "-data", "-iso"}

// pvcOwner returns the VM name a corral-created PVC belongs to, and whether the
// name matched a known corral disk-naming pattern. "gate-x-bootc-disk" -> "gate-x".
func pvcOwner(pvcName string) (owner string, isCorralPVC bool) {
	for _, s := range pvcSuffixes {
		if strings.HasSuffix(pvcName, s) {
			return strings.TrimSuffix(pvcName, s), true
		}
	}
	return "", false
}

// planOrphanPVCs is the pure decision core for orphan collection: given the
// PVCs corral could own and the set of live VM names, which PVCs have no
// backing VM? Split out from exec for unit-testing, mirroring planGC.
func planOrphanPVCs(pvcs []pvcRef, liveVMs map[string]bool) []pvcRef {
	var orphans []pvcRef
	for _, p := range pvcs {
		owner, ok := pvcOwner(p.Name)
		if !ok {
			continue // not a corral-shaped disk name — never touch it
		}
		if !liveVMs[owner] {
			orphans = append(orphans, p)
		}
	}
	return orphans
}

// pvcRef is a namespace-qualified PVC name.
type pvcRef struct {
	Namespace string
	Name      string
}

func (p pvcRef) String() string { return p.Namespace + "/" + p.Name }

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

// listManagedPVCs returns every PVC (all namespaces) that carries ManagedLabel
// — i.e. corral created it. Label-scoped on purpose: an unrelated app PVC is
// never a candidate, even if its name looks like a corral disk.
func listManagedPVCs() ([]pvcRef, error) {
	out, err := getPackageRunner().Run("kubectl", "get", "pvc", "-A",
		"-l", ManagedLabel+"=true",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name}{\"\\n\"}{end}")
	if err != nil {
		return nil, fmt.Errorf("listing PVCs: %w", err)
	}
	var refs []pvcRef
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ns, name, ok := strings.Cut(line, "/")
		if !ok {
			continue
		}
		refs = append(refs, pvcRef{Namespace: ns, Name: name})
	}
	return refs, nil
}

// collectOrphanPVCs deletes (or, in dryRun, lists) corral-managed disk PVCs
// whose owning VM no longer exists — the leaked disks left behind when a build
// or gate died between creating the PVC and creating/cleaning up its VM. bootc
// builder VMs are transient, so a "-bootc-disk" with no VM is the common leak.
func collectOrphanPVCs(dryRun bool, liveVMs map[string]bool, result *GCResult) {
	pvcs, err := listManagedPVCs()
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return
	}
	orphans := planOrphanPVCs(pvcs, liveVMs)
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].String() < orphans[j].String() })
	for _, p := range orphans {
		if dryRun {
			result.OrphanedPVCs = append(result.OrphanedPVCs, p.String())
			continue
		}
		if _, err := getPackageRunner().Run("kubectl", "delete", "pvc", p.Name,
			"-n", p.Namespace, "--ignore-not-found"); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: delete pvc: %v", p, err))
			continue
		}
		result.OrphanedPVCs = append(result.OrphanedPVCs, p.String())
	}
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

	// Second pass: collect leaked disk PVCs whose owning VM is gone. Build the
	// live-VM set from the VMs we just listed, minus any this run deleted.
	liveVMs := make(map[string]bool, len(vms))
	for _, vm := range vms {
		liveVMs[vm.Name] = true
	}
	if !dryRun {
		for _, name := range result.Deleted {
			delete(liveVMs, name)
		}
	}
	collectOrphanPVCs(dryRun, liveVMs, &result)

	return result, nil
}
