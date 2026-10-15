package kubevirt

import (
	"encoding/json"
	"fmt"
	"time"
)

// ── Snapshots ─────────────────────────────────────────────────────

// SnapshotInfo is a row in the snapshots list.
type SnapshotInfo struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Ready   bool   `json:"ready"`
	Created string `json:"created"`
}

// Snapshot creates a VirtualMachineSnapshot. If snap is empty a name is
// generated. Returns the snapshot name. With the guest agent connected,
// KubeVirt freezes the filesystem for a consistent snapshot automatically.
func (c *Client) Snapshot(vm, snap string) (string, error) {
	if snap == "" {
		snap = fmt.Sprintf("%s-snap-%d", vm, time.Now().Unix())
	}
	obj := map[string]any{
		"apiVersion": "snapshot.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineSnapshot",
		"metadata": map[string]any{
			"name":      snap,
			"namespace": c.Namespace,
			"labels":    map[string]any{"corral.dev/vm": vm},
		},
		"spec": map[string]any{
			"source": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": vm},
		},
	}
	return snap, Apply(obj)
}

// ListSnapshots returns snapshots, optionally filtered to a single VM.
func (c *Client) ListSnapshots(vm string) ([]SnapshotInfo, error) {
	out, err := c.runner().Run("kubectl", "get", "vmsnapshot", "-n", c.Namespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				CreationTimestamp string `json:"creationTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Source struct {
					Name string `json:"name"`
				} `json:"source"`
			} `json:"spec"`
			Status struct {
				ReadyToUse *bool `json:"readyToUse"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	snaps := []SnapshotInfo{}
	for _, it := range res.Items {
		if vm != "" && it.Spec.Source.Name != vm {
			continue
		}
		snaps = append(snaps, SnapshotInfo{
			Name:    it.Metadata.Name,
			Source:  it.Spec.Source.Name,
			Ready:   it.Status.ReadyToUse != nil && *it.Status.ReadyToUse,
			Created: it.Metadata.CreationTimestamp,
		})
	}
	return snaps, nil
}

// RestoreSnapshot restores a VM from a snapshot (VM must be stopped).
func (c *Client) RestoreSnapshot(vm, snap string) error {
	obj := map[string]any{
		"apiVersion": "snapshot.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineRestore",
		"metadata": map[string]any{
			"name":      fmt.Sprintf("%s-restore-%d", vm, time.Now().Unix()),
			"namespace": c.Namespace,
		},
		"spec": map[string]any{
			"target":                     map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": vm},
			"virtualMachineSnapshotName": snap,
		},
	}
	return Apply(obj)
}

// DeleteSnapshot removes a VirtualMachineSnapshot.
func (c *Client) DeleteSnapshot(snap string) error {
	_, err := c.runner().Run("kubectl", "delete", "vmsnapshot", snap, "-n", c.Namespace, "--ignore-not-found")
	return err
}
