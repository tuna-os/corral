package kubevirt

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/hanthor/corral/pkg/types"
)

// This file holds the "Proxmox-parity" VM operations layered on top of the
// basic lifecycle in client.go: live migration, CPU/memory scaling (live with
// an offline fallback), volume hotplug, disk expansion, snapshots, clone, and
// guest-agent introspection. Everything shells out to virtctl/kubectl — no
// client-side Kubernetes SDK — to keep Corral a single static binary.

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

func (c *Client) virtctlRun(args ...string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}
	out, err := exec.Command(virtctl, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("virtctl %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *Client) patchVMMerge(name, patch string) error {
	out, err := exec.Command("kubectl", "patch", "vm", name, "-n", c.Namespace,
		"--type", "merge", "-p", patch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("patch vm %s: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}

// ── CPU / memory scaling ──────────────────────────────────────────

type vmDomainInfo struct {
	sockets, cores, threads, maxSockets int
	guestMib, maxGuestMib               int
	running                             bool
}

func (c *Client) vmDomain(name string) (vmDomainInfo, error) {
	out, err := exec.Command("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "json").Output()
	if err != nil {
		return vmDomainInfo{}, fmt.Errorf("reading VM %s: %w", name, err)
	}
	var vm struct {
		Spec struct {
			Running  *bool `json:"running"`
			Template struct {
				Spec struct {
					Domain struct {
						CPU struct {
							Cores      int `json:"cores"`
							Sockets    int `json:"sockets"`
							Threads    int `json:"threads"`
							MaxSockets int `json:"maxSockets"`
						} `json:"cpu"`
						Memory struct {
							Guest    string `json:"guest"`
							MaxGuest string `json:"maxGuest"`
						} `json:"memory"`
					} `json:"domain"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &vm); err != nil {
		return vmDomainInfo{}, err
	}
	d := vm.Spec.Template.Spec.Domain
	return vmDomainInfo{
		sockets:     d.CPU.Sockets,
		cores:       d.CPU.Cores,
		threads:     d.CPU.Threads,
		maxSockets:  d.CPU.MaxSockets,
		guestMib:    quantityToMib(d.Memory.Guest),
		maxGuestMib: quantityToMib(d.Memory.MaxGuest),
		running:     vm.Spec.Running != nil && *vm.Spec.Running,
	}, nil
}

// canLiveMigrate reports whether the VM can ACTUALLY be live-migrated: KubeVirt
// must mark it LiveMigratable (masquerade net, migratable storage) AND there
// must be another schedulable node sharing its CPU vendor. The vendor check
// matters on heterogeneous clusters — you cannot live-migrate a running VM
// between an Intel and an AMD host, so without a same-vendor target the
// migration would hang in Scheduling forever.
func (c *Client) canLiveMigrate(name string) bool {
	s, ok := vmiStatusIndex()[c.Namespace+"/"+name]
	if !ok || !s.LiveMigratable {
		return false
	}
	return hasMigrationTarget(s.Node, nodeVendors())
}

// nodeVendors maps each schedulable node to its KubeVirt CPU vendor label.
func nodeVendors() map[string]string {
	out, err := exec.Command("kubectl", "get", "nodes", "-o", "json").Output()
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

// ScaleCPU sets the VM's vCPU count (live when possible, else offline).
func (c *Client) ScaleCPU(name string, vcpus int) error {
	if vcpus < 1 {
		return fmt.Errorf("cpu must be >= 1")
	}
	return c.Scale(name, vcpus, "")
}

// ScaleMemory sets the VM's guest memory, e.g. "8G" (live when possible, else offline).
func (c *Client) ScaleMemory(name, mem string) error {
	return c.Scale(name, 0, mem)
}

// Scale changes CPU and/or memory in one operation. When the VM is running and
// genuinely live-migratable (same-vendor target node, masquerade net) and the
// new values fit the maxSockets/maxGuest headroom, both are hotplugged live
// with no downtime. Otherwise both are applied in a single stop→patch→start
// cycle (one reboot, not two). vcpus==0 / mem=="" mean "leave unchanged".
func (c *Client) Scale(name string, vcpus int, mem string) error {
	if vcpus == 0 && mem == "" {
		return fmt.Errorf("nothing to change")
	}
	d, err := c.vmDomain(name)
	if err != nil {
		return err
	}
	mib := 0
	if mem != "" {
		if mib = quantityToMib(mem); mib < 1 {
			return fmt.Errorf("invalid memory %q", mem)
		}
	}
	socketsBased := d.cores <= 1 && d.threads <= 1
	cpuFits := vcpus == 0 || (socketsBased && d.maxSockets >= vcpus)
	memFits := mem == "" || mib <= d.maxGuestMib

	if d.running && cpuFits && memFits && c.canLiveMigrate(name) {
		if vcpus > 0 {
			if err := c.patchVMMerge(name,
				fmt.Sprintf(`{"spec":{"template":{"spec":{"domain":{"cpu":{"sockets":%d}}}}}}`, vcpus)); err != nil {
				return err
			}
		}
		if mem != "" {
			if err := c.patchVMMerge(name,
				fmt.Sprintf(`{"spec":{"template":{"spec":{"domain":{"memory":{"guest":"%dMi"}}}}}}`, mib)); err != nil {
				return err
			}
		}
		return nil
	}

	domain := map[string]any{}
	if vcpus > 0 {
		domain["cpu"] = cpuSpec(vcpus)
	}
	if mem != "" {
		domain["memory"] = memSpec(mib)
	}
	return c.offlinePatch(name, d.running, domain)
}

// offlinePatch applies a domain-level merge patch, stopping/starting the VM if
// it is running so the change takes effect immediately.
func (c *Client) offlinePatch(name string, running bool, domain map[string]any) error {
	body, err := json.Marshal(map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"domain": domain}}},
	})
	if err != nil {
		return err
	}
	if running {
		if err := c.StopVM(name); err != nil {
			return err
		}
		c.waitStopped(name)
	}
	if err := c.patchVMMerge(name, string(body)); err != nil {
		return err
	}
	if running {
		return c.StartVM(name)
	}
	return nil
}

func (c *Client) waitStopped(name string) {
	for i := 0; i < 60; i++ {
		if exec.Command("kubectl", "get", "vmi", name, "-n", c.Namespace).Run() != nil {
			return // VMI gone
		}
		time.Sleep(time.Second)
	}
}

// ── Volumes ───────────────────────────────────────────────────────

// AddVolume creates a new PVC and hotplugs it onto the running VM. Returns the
// PVC name. The PVC is labeled for cleanup on VM delete.
func (c *Client) AddVolume(name, size string) (string, error) {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return "", err
	}
	if size == "" {
		size = "10Gi"
	}
	pvcName := fmt.Sprintf("%s-hp-%d", name, time.Now().Unix())
	pvc := GeneratePVCWithClass(pvcName, c.Namespace, size, PreferredStorageClass())
	pvc["metadata"].(map[string]any)["labels"] = map[string]any{"corral.dev/vm": name}
	if err := Apply(pvc); err != nil {
		return "", fmt.Errorf("creating disk PVC: %w", err)
	}
	out, err := exec.Command(virtctl, "addvolume", name, "--volume-name="+pvcName, "-n", c.Namespace).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("addvolume: %s", strings.TrimSpace(string(out)))
	}
	return pvcName, nil
}

// RemoveVolume detaches a hotplugged volume from the VM.
func (c *Client) RemoveVolume(name, vol string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}
	out, err := exec.Command(virtctl, "removevolume", name, "--volume-name="+vol, "-n", c.Namespace).CombinedOutput()
	if err != nil {
		return fmt.Errorf("removevolume: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ExpandDisk grows a PVC to the given size (requires an expandable StorageClass).
func (c *Client) ExpandDisk(pvc, size string) error {
	patch := fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":%q}}}}`, size)
	out, err := exec.Command("kubectl", "patch", "pvc", pvc, "-n", c.Namespace,
		"--type", "merge", "-p", patch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("expand pvc %s: %s", pvc, strings.TrimSpace(string(out)))
	}
	return nil
}

// ── Snapshots / clone ─────────────────────────────────────────────

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
	out, err := exec.Command("kubectl", "get", "vmsnapshot", "-n", c.Namespace, "-o", "json").Output()
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
	out, err := exec.Command("kubectl", "delete", "vmsnapshot", snap, "-n", c.Namespace, "--ignore-not-found").CombinedOutput()
	if err != nil {
		return fmt.Errorf("delete snapshot %s: %s", snap, strings.TrimSpace(string(out)))
	}
	return nil
}

// Clone creates a VirtualMachineClone from src into a new VM named dst.
func (c *Client) Clone(src, dst string) error {
	obj := map[string]any{
		"apiVersion": "clone.kubevirt.io/v1beta1",
		"kind":       "VirtualMachineClone",
		"metadata": map[string]any{
			"name":      fmt.Sprintf("clone-%s-%d", dst, time.Now().Unix()),
			"namespace": c.Namespace,
		},
		"spec": map[string]any{
			"source": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": src},
			"target": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": dst},
		},
	}
	return Apply(obj)
}

// ── Guest agent ───────────────────────────────────────────────────

// GuestInfo returns guest-agent data (OS, filesystems, users). Errors when the
// agent is not connected.
func (c *Client) GuestInfo(name string) (map[string]any, error) {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return nil, err
	}
	res := map[string]any{}
	out, err := exec.Command(virtctl, "guestosinfo", name, "-n", c.Namespace).Output()
	if err != nil {
		return nil, fmt.Errorf("guest agent not available (is qemu-guest-agent installed and running?)")
	}
	var os any
	json.Unmarshal(out, &os)
	res["os"] = os
	if out, err := exec.Command(virtctl, "fslist", name, "-n", c.Namespace).Output(); err == nil {
		var fs any
		json.Unmarshal(out, &fs)
		res["filesystems"] = fs
	}
	if out, err := exec.Command(virtctl, "userlist", name, "-n", c.Namespace).Output(); err == nil {
		var users any
		json.Unmarshal(out, &users)
		res["users"] = users
	}
	return res, nil
}

// ── Storage / capability detection ────────────────────────────────

// GeneratePVCWithClass is GeneratePVC with an explicit StorageClass ("" = cluster default).
func GeneratePVCWithClass(name, namespace, size, storageClass string) map[string]any {
	pvc := GeneratePVC(name, namespace, size)
	if storageClass != "" {
		pvc["spec"].(map[string]any)["storageClassName"] = storageClass
	}
	return pvc
}

// PreferredStorageClass returns the StorageClass Corral prefers for new disks:
// "longhorn" when present (RWX, snapshots, expansion), else "" (cluster default).
func PreferredStorageClass() string {
	return ClusterCapabilities().StorageClass
}

// ClusterCapabilities reports which optional storage operations are available,
// so callers can enable/disable UI controls rather than fail on click.
func ClusterCapabilities() types.Capabilities {
	out, err := exec.Command("kubectl", "get", "sc", "-o", "json").Output()
	if err != nil {
		return types.Capabilities{}
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			AllowVolumeExpansion *bool `json:"allowVolumeExpansion"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &res) != nil {
		return types.Capabilities{}
	}
	expand := map[string]bool{}
	var preferred, def string
	for _, it := range res.Items {
		expand[it.Metadata.Name] = it.AllowVolumeExpansion != nil && *it.AllowVolumeExpansion
		if it.Metadata.Name == "longhorn" {
			preferred = "longhorn"
		}
		if it.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" {
			def = it.Metadata.Name
		}
	}
	effective := preferred
	if effective == "" {
		effective = def
	}
	return types.Capabilities{
		StorageClass: preferred,
		CanExpand:    expand[effective],
		CanSnapshot:  hasSnapshotClass(),
	}
}

func hasSnapshotClass() bool {
	out, err := exec.Command("kubectl", "get", "volumesnapshotclass", "-o", "name").Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// quantityToMib parses a Kubernetes memory quantity (Gi/G/Mi/M/Ki/K or raw) to MiB.
func quantityToMib(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var num float64
	var unit string
	fmt.Sscanf(s, "%f%s", &num, &unit)
	switch strings.ToLower(unit) {
	case "gi", "g":
		return int(num * 1024)
	case "mi", "m", "":
		return int(num)
	case "ki", "k":
		return int(num / 1024)
	default:
		return int(num)
	}
}
