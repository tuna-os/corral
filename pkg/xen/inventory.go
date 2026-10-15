// Inventory: pool listing in Corral terms.
//
// XenAPI addresses everything by opaque references; Corral addresses
// everything by name. List resolves one VM.get_all into per-VM get_record
// calls (the pool is small — tens of VMs, not thousands), then joins host
// names and disk sizes from single get_all_records calls each, so the whole
// listing is 4+N round trips and no operation needs a second lookup to find
// out where an instance lives.

package xen

import (
	"fmt"
	"strings"

	"github.com/tuna-os/corral/pkg/types"
)

// VMRecord is the XenAPI VM.record subset Corral reads. Field names follow
// the XenAPI VM class: name_label, name_description, power_state, uuid,
// is_a_template, is_control_domain, is_a_snapshot, VCPUs_max,
// memory_static_max, resident_on, VBDs, tags.
type VMRecord struct {
	Ref             string
	UUID            string
	Name            string
	Description     string
	PowerState      string
	IsTemplate      bool
	IsControlDomain bool
	IsSnapshot      bool
	VCPUs           int64
	MemoryBytes     int64
	ResidentOn      string
	VBDs            []string
	Tags            []string
}

// HostRecord is the XenAPI host.record subset Corral reads.
type HostRecord struct {
	Ref      string
	UUID     string
	Name     string
	Hostname string
}

// VMRef returns the opaque reference for a VM by name or UUID, using the
// pool's own indexes (VM.get_by_name_label / VM.get_by_uuid) rather than a
// List scan, so power operations stay one round trip past login.
func (c *Client) VMRef(nameOrUUID string) (string, error) {
	if strings.HasPrefix(nameOrUUID, "OpaqueRef:") {
		return nameOrUUID, nil
	}
	if v, err := c.call("VM.get_by_uuid", nameOrUUID); err == nil {
		if s, ok := v.(string); ok && s != "" {
			return s, nil
		}
	} else if apiErr, ok := err.(*APIError); !ok || (apiErr.Code != "UUID_INVALID" && apiErr.Code != "HANDLE_INVALID") {
		return "", err
	}
	v, err := c.call("VM.get_by_name_label", nameOrUUID)
	if err != nil {
		return "", err
	}
	refs := asStrings(v)
	if len(refs) == 0 {
		return "", fmt.Errorf("xen: no VM named %q", nameOrUUID)
	}
	if len(refs) > 1 {
		return "", fmt.Errorf("xen: %q matches %d VMs; use a UUID", nameOrUUID, len(refs))
	}
	return refs[0], nil
}

// VMRecord fetches one VM's record by reference.
func (c *Client) VMRecord(ref string) (VMRecord, error) {
	v, err := c.call("VM.get_record", ref)
	if err != nil {
		return VMRecord{}, err
	}
	rec, ok := v.(map[string]any)
	if !ok {
		return VMRecord{}, fmt.Errorf("xen: VM.get_record returned %T, want a struct", v)
	}
	return parseVMRecord(ref, rec), nil
}

func parseVMRecord(ref string, rec map[string]any) VMRecord {
	return VMRecord{
		Ref:             ref,
		UUID:            asString(rec["uuid"]),
		Name:            asString(rec["name_label"]),
		Description:     asString(rec["name_description"]),
		PowerState:      asString(rec["power_state"]),
		IsTemplate:      asBool(rec["is_a_template"]),
		IsControlDomain: asBool(rec["is_control_domain"]),
		IsSnapshot:      asBool(rec["is_a_snapshot"]),
		VCPUs:           asInt(rec["VCPUs_max"]),
		MemoryBytes:     asInt(rec["memory_static_max"]),
		ResidentOn:      asString(rec["resident_on"]),
		VBDs:            asStrings(rec["VBDs"]),
		Tags:            asStrings(rec["tags"]),
	}
}

// Hosts returns every host in the pool, keyed by reference for List's join.
func (c *Client) Hosts() (map[string]HostRecord, error) {
	v, err := c.call("host.get_all_records")
	if err != nil {
		return nil, err
	}
	recs, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("xen: host.get_all_records returned %T, want a struct", v)
	}
	out := make(map[string]HostRecord, len(recs))
	for ref, raw := range recs {
		rec, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out[ref] = HostRecord{
			Ref:      ref,
			UUID:     asString(rec["uuid"]),
			Name:     asString(rec["name_label"]),
			Hostname: asString(rec["hostname"]),
		}
	}
	return out, nil
}

// List converts the pool's VMs into Corral VMs. Control domains (Domain-0),
// templates, and snapshots are skipped — they are not guests Corral can
// drive, the same way pkg/proxmoxbe's List returns qemu guests but not LXC
// containers. A per-VM get_record failure skips that VM rather than failing
// the whole listing, mirroring libvirt.List's tolerance.
func (c *Client) List() ([]types.VM, error) {
	v, err := c.call("VM.get_all")
	if err != nil {
		return nil, err
	}
	refs := asStrings(v)

	hosts, err := c.Hosts()
	if err != nil {
		hosts = map[string]HostRecord{}
	}
	disks := map[string]int64{}
	if sizes, err := c.diskSizes(); err == nil {
		disks = sizes
	}

	var vms []types.VM
	for _, ref := range refs {
		rec, err := c.VMRecord(ref)
		if err != nil {
			continue
		}
		if rec.IsControlDomain || rec.IsTemplate || rec.IsSnapshot {
			continue
		}
		vms = append(vms, c.toVM(rec, hosts, disks))
	}
	return vms, nil
}

func (c *Client) toVM(rec VMRecord, hosts map[string]HostRecord, disks map[string]int64) types.VM {
	running := strings.EqualFold(rec.PowerState, "Running")
	status := rec.PowerState
	if status == "" {
		status = "Halted"
	}
	host := hosts[rec.ResidentOn]
	node := host.Name
	if node == "" {
		node = host.Hostname
	}
	vm := types.VM{
		Name:    rec.Name,
		Backend: "xen",
		// Namespace stays empty: a pool has no namespace, and repurposing
		// the field (for the pool, or a tag) would make identity mean two
		// things — the same call pkg/proxmoxbe makes.
		Context:        c.cfg.Host,
		Status:         status,
		Ready:          running,
		Running:        running,
		CPU:            int(rec.VCPUs),
		Mem:            humanBytes(rec.MemoryBytes),
		Disk:           humanBytes(disks[rec.Ref]),
		Node:           node,
		IsTemplate:     rec.IsTemplate,
		Tags:           rec.Tags,
		Capabilities:   types.CapabilitiesForBackend("xen"),
		LiveMigratable: running,
	}
	vm.SetIdentity()
	return vm
}

// diskSizes sums each VM's attached VDI virtual_size by joining one
// VBD.get_all_records with one VDI.get_all_records. Failures (a pool that
// denies VDI reads to this role) come back empty rather than failing List.
func (c *Client) diskSizes() (map[string]int64, error) {
	v, err := c.call("VBD.get_all_records")
	if err != nil {
		return nil, err
	}
	vbds, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("xen: VBD.get_all_records returned %T, want a struct", v)
	}
	d, err := c.call("VDI.get_all_records")
	if err != nil {
		return nil, err
	}
	vdis, ok := d.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("xen: VDI.get_all_records returned %T, want a struct", d)
	}
	sizes := map[string]int64{}
	vdiSize := map[string]int64{}
	for ref, raw := range vdis {
		if rec, ok := raw.(map[string]any); ok {
			vdiSize[ref] = asInt(rec["virtual_size"])
		}
	}
	for _, raw := range vbds {
		rec, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		vmRef := asString(rec["VM"])
		vdiRef := asString(rec["VDI"])
		if vmRef == "" || vdiRef == "" || vdiRef == "OpaqueRef:NULL" {
			continue
		}
		sizes[vmRef] += vdiSize[vdiRef]
	}
	return sizes, nil
}

// Exists reports whether a VM is present. A pool that cannot be reached
// reports false, matching every other backend's Exists.
func (c *Client) Exists(name string) bool {
	_, err := c.VMRef(name)
	return err == nil
}

func humanBytes(n int64) string {
	if n <= 0 {
		return ""
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
