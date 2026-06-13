package proxmox

import (
	"fmt"
	"hash/crc32"
	"strings"

	"github.com/hanthor/corral/pkg/types"
)

// ── translate: Proxmox ↔ KubeVirt shape mapping ──────────────────
//
// Pure functions — no HTTP, no kubectl, no server struct dependency.
// Testable in isolation: given K8s/KubeVirt types, produce Proxmox
// response shapes (or vice versa).

// vmidFor derives a stable Proxmox-style numeric ID from the VM name.
// Proxmox VMIDs live in [100, 999999999]; crc32 keeps them deterministic
// with no state to store. Collisions are theoretically possible but
// irrelevant at homelab scale.
func VmidFor(name string) int {
	return 100 + int(crc32.ChecksumIEEE([]byte(name))%899999900)
}

// PVEStatus maps a corral VM running state to Proxmox's running/stopped
// vocabulary.
func PVEStatus(vm *types.VM) string {
	if vm.Running {
		return "running"
	}
	return "stopped"
}

// MemBytes parses corral memory strings ("4G", "4Gi", "4096M") to a
// byte count.  Used to populate Proxmox maxmem fields.
func MemBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var num float64
	var unit string
	fmt.Sscanf(s, "%f%s", &num, &unit)
	switch strings.TrimSuffix(strings.ToLower(unit), "i") {
	case "g":
		return int64(num * float64(1<<30))
	case "m", "":
		return int64(num * float64(1<<20))
	case "k":
		return int64(num * float64(1<<10))
	}
	return int64(num)
}

// upid fabricates a Proxmox task ID. Corral's operations are synchronous,
// so the matching task-status endpoint always reports the task finished OK.
func UPID(node, action string, vmid int) string {
	return fmt.Sprintf("UPID:%s:00000000:00000000:00000000:%s:%d:corral@pve:", node, action, vmid)
}

// vmNode resolves a VM's Proxmox node. KubeVirt VMs may have no node when
// stopped (unplaced); Proxmox always expects a node, so we default to the
// first ready node in the supplied list.
func VMNode(vm *types.VM, nodes []NodeInfo) string {
	if vm.Node != "" && vm.Node != "—" {
		return vm.Node
	}
	for _, n := range nodes {
		if n.Ready {
			return n.Name
		}
	}
	return ""
}

// vmEntry builds a Proxmox VM row (for qemu lists and cluster/resources)
// using an explicit vmid and node.
func VMEntry(vm *types.VM, vmid int, node string) map[string]any {
	return map[string]any{
		"vmid":     vmid,
		"name":     vm.Name,
		"status":   PVEStatus(vm),
		"node":     node,
		"cpus":     vm.CPU,
		"maxmem":   MemBytes(vm.Mem),
		"maxdisk":  0,
		"uptime":   0,
		"template": 0,
	}
}

// ── Access control roles ─────────────────────────────────────────

// k8sRolesToProxmox returns a fixed set of Proxmox roles mapped from
// well-known K8s ClusterRoles.  See docs/adr/0001-k8s-rbac-to-proxmox-privileges.md.
func K8sRolesToProxmox() []map[string]any {
	return []map[string]any{
		{
			"roleid":  "Administrator",
			"privs":   AllProxmoxPrivileges(),
			"special": 1,
			"comment": "K8s cluster-admin → full Proxmox privileges",
		},
		{
			"roleid":  "Operator",
			"privs":   "VM.Allocate,VM.Audit,VM.Clone,VM.Config.CDROM,VM.Config.CPU,VM.Config.Disk,VM.Config.Memory,VM.Config.Network,VM.Config.Options,VM.Console,VM.Migrate,VM.Monitor,VM.PowerMgmt,VM.Snapshot,Datastore.Allocate,Datastore.AllocateSpace,Datastore.Audit,Sys.Audit,Sys.Console",
			"special": 0,
			"comment": "K8s admin → VM + Datastore management",
		},
		{
			"roleid":  "Viewer",
			"privs":   "VM.Audit,Datastore.Audit,Sys.Audit",
			"special": 0,
			"comment": "K8s view → read-only",
		},
		{
			"roleid":  "NoAccess",
			"privs":   "",
			"special": 0,
			"comment": "Default — no privileges",
		},
	}
}

// allProxmoxPrivileges returns the complete Proxmox VE privilege set
// as a comma-separated string.
func AllProxmoxPrivileges() string {
	return strings.Join([]string{
		"VM.Allocate", "VM.Audit", "VM.Clone",
		"VM.Config.CDROM", "VM.Config.CPU", "VM.Config.Disk",
		"VM.Config.Memory", "VM.Config.Network", "VM.Config.Options",
		"VM.Console", "VM.Migrate", "VM.Monitor", "VM.PowerMgmt",
		"VM.Snapshot",
		"Datastore.Allocate", "Datastore.AllocateSpace",
		"Datastore.AllocateTemplate", "Datastore.Audit",
		"Sys.Audit", "Sys.Console", "Sys.Modify",
		"Sys.PowerMgmt", "Sys.Syslog",
		"Permissions.Modify",
	}, ",")
}
