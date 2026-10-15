package kubevirt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tuna-os/corral/pkg/shell"
)

// This file holds VM lifecycle extras and live migration, layered on top of
// the basic lifecycle in client.go. Everything shells out to virtctl/kubectl
// — no client-side Kubernetes SDK — to keep Corral a single static binary.

// ── Lifecycle extras ──────────────────────────────────────────────

// RestartVM live-restarts a VirtualMachine.
func (c *Client) RestartVM(name string) error {
	return c.virtctlRun("restart", name, "-n", c.Namespace)
}

// PauseVM pauses a running VM (freezes vCPUs).
func (c *Client) PauseVM(name string) error {
	return c.virtctlRun("pause", "vm", name, "-n", c.Namespace)
}

// UnpauseVM resumes a paused VM.
func (c *Client) UnpauseVM(name string) error {
	return c.virtctlRun("unpause", "vm", name, "-n", c.Namespace)
}

// Migrate live-migrates a VM to another node. When targetNode is set, the VM's
// nodeSelector is pinned to it first (so it lands there); otherwise the
// scheduler picks. Only works for live-migratable VMs (ephemeral disk or RWX).
func (c *Client) Migrate(name, targetNode string) error {
	if targetNode == "" && !c.canLiveMigrate(name) {
		return fmt.Errorf("%s cannot be live-migrated: it is not migratable, or no other schedulable node shares its CPU vendor (live migration is impossible between e.g. Intel and AMD hosts)", name)
	}
	if targetNode != "" {
		patch := fmt.Sprintf(`{"spec":{"template":{"spec":{"nodeSelector":{"kubernetes.io/hostname":%q}}}}}`, targetNode)
		if err := c.patchVMMerge(name, patch); err != nil {
			return err
		}
	}
	return c.virtctlRun("migrate", name, "-n", c.Namespace)
}

// MigrationState is a snapshot of a VM's most recent live migration, read from
// the VMI's status.migrationState.
type MigrationState struct {
	Present    bool   `json:"present"` // a migrationState exists on the VMI
	Active     bool   `json:"active"`
	Completed  bool   `json:"completed"`
	Failed     bool   `json:"failed"`
	SourceNode string `json:"sourceNode"`
	TargetNode string `json:"targetNode"`
}

// MigrationState reads the VMI's live-migration progress. A VMI with no
// migration yet returns Present=false (not an error).
func (c *Client) MigrationState(name string) (MigrationState, error) {
	out, err := c.runner().Run("kubectl", "get", "vmi", name, "-n", c.Namespace, "-o", "json")
	if err != nil {
		return MigrationState{}, err
	}
	var vmi struct {
		Status struct {
			MigrationState *struct {
				Completed  bool   `json:"completed"`
				Failed     bool   `json:"failed"`
				SourceNode string `json:"sourceNode"`
				TargetNode string `json:"targetNode"`
			} `json:"migrationState"`
		} `json:"status"`
	}
	if err := json.Unmarshal(out, &vmi); err != nil {
		return MigrationState{}, err
	}
	ms := vmi.Status.MigrationState
	if ms == nil {
		return MigrationState{}, nil
	}
	return MigrationState{
		Present:    true,
		Active:     !ms.Completed && !ms.Failed,
		Completed:  ms.Completed,
		Failed:     ms.Failed,
		SourceNode: ms.SourceNode,
		TargetNode: ms.TargetNode,
	}, nil
}

func (c *Client) virtctlRun(args ...string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}
	_, err = c.runner().Run(virtctl, args...)
	return err
}

func (c *Client) patchVMMerge(name, patch string) error {
	_, err := c.runner().Run("kubectl", "patch", "vm", name, "-n", c.Namespace,
		"--type", "merge", "-p", patch)
	return err
}

// canLiveMigrate reports whether the VM can ACTUALLY be live-migrated: KubeVirt
// must mark it LiveMigratable (masquerade net, migratable storage) AND there
// must be another schedulable node sharing its CPU vendor. The vendor check
// matters on heterogeneous clusters — you cannot live-migrate a running VM
// between an Intel and an AMD host, so without a same-vendor target the
// migration would hang in Scheduling forever.
func (c *Client) canLiveMigrate(name string) bool {
	s, ok := vmiStatusIndexWithRunner(c.runner())[c.Namespace+"/"+name]
	if !ok || !s.LiveMigratable {
		return false
	}
	return hasMigrationTarget(s.Node, nodeVendorsWithRunner(c.runner()))
}

// nodeVendorsWithRunner maps each schedulable node to its KubeVirt CPU vendor label.
func nodeVendorsWithRunner(r shell.Runner) map[string]string {
	out, err := r.Run("kubectl", "get", "nodes", "-o", "json")
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
	vendors := map[string]string{}
	for _, n := range res.Items {
		if n.Metadata.Labels["kubevirt.io/schedulable"] != "true" {
			continue
		}
		for k := range n.Metadata.Labels {
			if v, ok := strings.CutPrefix(k, "cpu-vendor.node.kubevirt.io/"); ok {
				vendors[n.Metadata.Name] = v
				break
			}
		}
	}
	return vendors
}

// hasMigrationTarget reports whether a node other than `node` shares its vendor.
func hasMigrationTarget(node string, vendors map[string]string) bool {
	want, ok := vendors[node]
	if !ok || want == "" {
		return false
	}
	for n, v := range vendors {
		if n != node && v == want {
			return true
		}
	}
	return false
}
