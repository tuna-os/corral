package proxmox

import (
	"fmt"
	"hash/crc32"
	"sort"
	"strings"

	"github.com/tuna-os/corral/pkg/types"
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

// ── Nodes ─────────────────────────────────────────────────────────

// NodeStatus maps a K8s node's Ready condition to Proxmox's online/offline
// vocabulary.
func NodeStatus(ready bool) string {
	if ready {
		return "online"
	}
	return "offline"
}

// NodeEntry builds a full Proxmox node row, as returned by GET /nodes.
func NodeEntry(n NodeInfo) map[string]any {
	return map[string]any{
		"node":            n.Name,
		"id":              "node/" + n.Name,
		"type":            "node",
		"status":          NodeStatus(n.Ready),
		"maxcpu":          n.CPU,
		"maxmem":          MemBytes(n.MemRaw),
		"ssl_fingerprint": "",
		"uptime":          0,
		"cpu":             0.0,
		"mem":             0,
		"maxdisk":         0,
		"disk":            0,
		"level":           "",
	}
}

// NodeResourceEntry builds the abbreviated node row used by
// GET /cluster/resources?type=node — a subset of NodeEntry's fields.
func NodeResourceEntry(n NodeInfo) map[string]any {
	return map[string]any{
		"id": "node/" + n.Name, "node": n.Name, "type": "node",
		"status": NodeStatus(n.Ready), "maxcpu": n.CPU, "maxmem": MemBytes(n.MemRaw),
	}
}

// ── Pools ─────────────────────────────────────────────────────────

// PoolsFromNamespaces groups vms by K8s namespace into Proxmox pool rows, as
// returned by GET /pools — namespaces are corral's pools (see ADR-0001-adjacent
// web-UI folder-view work: the namespace is the stable axis KubeVirt VMs live
// on, unlike node, which changes under live migration). vmidFor resolves each
// VM's Proxmox vmid the same way vmEntry/vmEntryWithID do.
func PoolsFromNamespaces(vms []types.VM, vmidFor func(name string) int) []map[string]any {
	byNS := map[string][]int{}
	for _, vm := range vms {
		if vm.Namespace == "" {
			continue
		}
		byNS[vm.Namespace] = append(byNS[vm.Namespace], vmidFor(vm.Name))
	}
	names := make([]string, 0, len(byNS))
	for ns := range byNS {
		names = append(names, ns)
	}
	sort.Strings(names)

	out := make([]map[string]any, 0, len(names))
	for _, ns := range names {
		members := byNS[ns]
		sort.Ints(members)
		out = append(out, map[string]any{"poolid": ns, "members": members})
	}
	return out
}

// ── Storage ───────────────────────────────────────────────────────

// ProxmoxStorageEntry builds a Proxmox storage row from a K8s StorageClass,
// as returned by GET /nodes/{node}/storage.
func ProxmoxStorageEntry(sc StorageEntry, node string) map[string]any {
	return map[string]any{
		"storage": sc.Name,
		"node":    node,
		"type":    sc.Type,
		"content": "images,rootdir",
		"active":  1,
		"enabled": 1,
		"shared":  0,
		"avail":   0,
		"total":   0,
		"used":    0,
	}
}

// ── Access control users/groups ──────────────────────────────────

// RBACUsersToProxmox maps K8s RBAC users onto Proxmox user rows, as returned
// by GET /access/users. See docs/adr/0001-k8s-rbac-to-proxmox-privileges.md.
func RBACUsersToProxmox(users []RBACUser) []map[string]any {
	var out []map[string]any
	for _, u := range users {
		out = append(out, map[string]any{
			"userid":    u.UserID,
			"enable":    1,
			"expire":    0,
			"email":     "",
			"comment":   u.Comment,
			"firstname": "",
			"lastname":  "",
			"tokens":    []any{},
		})
	}
	return out
}

// RBACGroupsToProxmox maps K8s RBAC groups onto Proxmox group rows, as
// returned by GET /access/groups.
func RBACGroupsToProxmox(groups []RBACGroup) []map[string]any {
	var out []map[string]any
	for _, g := range groups {
		out = append(out, map[string]any{
			"groupid": g.GroupID,
			"comment": "",
			"members": []any{},
		})
	}
	return out
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
