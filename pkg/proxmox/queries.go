package proxmox

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hanthor/corral/pkg/shell"
)

// ── KubeVirt query adapters ───────────────────────────────────────
//
// These modules encapsulate kubectl JSON invocations behind typed
// interfaces.  Each adapter owns its kubectl command, JSON unmarshalling,
// and error handling.  Callers (HTTP handlers, the translate module)
// receive structured Go types — never raw kubectl output.
//
// All adapters accept a shell.Runner, making them testable with
// shell.Fake without a real cluster.

// NodeQuery returns K8s node facts (name, CPU capacity, memory, Ready
// condition) suitable for Proxmox node-list and node-status responses.
type NodeQuery struct {
	runner shell.Runner
}

type NodeInfo struct {
	Name   string
	Ready  bool
	CPU    int
	MemRaw string
}

func (q *NodeQuery) List() ([]NodeInfo, error) {
	out, err := q.runner.Run("kubectl", "get", "nodes", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %s", strings.TrimSpace(string(out)))
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Capacity struct {
					CPU    string `json:"cpu"`
					Memory string `json:"memory"`
				} `json:"capacity"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	var nodes []NodeInfo
	for _, it := range res.Items {
		n := NodeInfo{Name: it.Metadata.Name, MemRaw: it.Status.Capacity.Memory}
		n.CPU, _ = strconv.Atoi(it.Status.Capacity.CPU)
		for _, c := range it.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				n.Ready = true
			}
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// StorageQuery returns K8s StorageClasses mapped to Proxmox storage
// entries (type, content, enabled/active flags).
type StorageQuery struct {
	runner shell.Runner
}

type StorageEntry struct {
	Name        string
	Type        string // Proxmox storage type: lvmthin, dir, etc.
	Provisioner string
}

func (q *StorageQuery) List() ([]StorageEntry, error) {
	out, err := q.runner.Run("kubectl", "get", "sc", "-o", "json")
	if err != nil {
		return nil, err
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Provisioner string `json:"provisioner"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	var entries []StorageEntry
	for _, sc := range res.Items {
		var typ string
		switch {
		case strings.Contains(sc.Provisioner, "longhorn"):
			typ = "lvmthin"
		case strings.Contains(sc.Provisioner, "local-path"):
			typ = "dir"
		default:
			typ = "dir"
		}
		entries = append(entries, StorageEntry{
			Name:        sc.Metadata.Name,
			Type:        typ,
			Provisioner: sc.Provisioner,
		})
	}
	return entries, nil
}

// RBACQuery returns K8s ClusterRoleBinding subjects extracted as
// Proxmox-shaped users and groups.  Always includes `root@pam`.
type RBACQuery struct {
	runner shell.Runner
}

type RBACUser struct {
	UserID  string
	Comment string
}

type RBACGroup struct {
	GroupID string
}

func (q *RBACQuery) Subjects() (users []RBACUser, groups []RBACGroup) {
	out, err := q.runner.Run("kubectl", "get", "clusterrolebindings", "-o", "json")
	if err != nil {
		return nil, nil
	}
	var res struct {
		Items []struct {
			Subjects []struct {
				Kind     string `json:"kind"`
				Name     string `json:"name"`
				APIGroup string `json:"apiGroup"`
			} `json:"subjects"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &res) != nil {
		return nil, nil
	}

	seenUsers := map[string]bool{"root@pam": true}
	seenGroups := map[string]bool{}

	for _, crb := range res.Items {
		for _, subj := range crb.Subjects {
			switch subj.Kind {
			case "User":
				name := subj.Name
				if subj.APIGroup == "rbac.authorization.k8s.io" {
					name += "@k8s"
				} else {
					name += "@pve"
				}
				seenUsers[name] = true
			case "ServiceAccount":
				seenUsers[subj.Name+"@sa"] = true
			case "Group":
				seenGroups[subj.Name] = true
			}
		}
	}

	users = append(users, RBACUser{UserID: "root@pam", Comment: "Built-in superuser"})
	for u := range seenUsers {
		if u == "root@pam" {
			continue
		}
		users = append(users, RBACUser{UserID: u})
	}

	if len(seenGroups) == 0 {
		seenGroups["Administrators"] = true
	}
	for g := range seenGroups {
		groups = append(groups, RBACGroup{GroupID: g})
	}

	return users, groups
}

// VMIDLabelQuery queries the corral.io/proxmox-vmid label on VMs and
// returns a name→vmid lookup map.  VMs without the label are excluded.
type VMIDLabelQuery struct {
	runner shell.Runner
}

func (q *VMIDLabelQuery) Map() map[string]int {
	out, err := q.runner.Run("kubectl", "get", "vms", "-A", "-l",
		"corral.io/proxmox-vmid", "-o", "json")
	if err != nil {
		return nil
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &res) != nil {
		return nil
	}
	m := map[string]int{}
	for _, it := range res.Items {
		if v := it.Metadata.Labels["corral.io/proxmox-vmid"]; v != "" {
			if id, err := strconv.Atoi(v); err == nil {
				m[it.Metadata.Name] = id
			}
		}
	}
	return m
}

// Lookup finds a VM by its corral.io/proxmox-vmid label.
// Returns the VM name and namespace if found.
func (q *VMIDLabelQuery) Lookup(vmid int) (name, namespace string, found bool) {
	out, err := q.runner.Run("kubectl", "get", "vms", "-A", "-l",
		fmt.Sprintf("corral.io/proxmox-vmid=%d", vmid), "-o", "json")
	if err != nil {
		return "", "", false
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &res) != nil || len(res.Items) == 0 {
		return "", "", false
	}
	return res.Items[0].Metadata.Name, res.Items[0].Metadata.Namespace, true
}
