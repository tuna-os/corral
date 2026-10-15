package nutanix

// Snapshots, via the vmm namespace's own VM recovery points
// (/ahv/config/vm-recovery-points), restored in place through the VM's
// $actions/revert.
//
// A v4 VM snapshot is a crash-consistent recovery point of the VM's disks —
// NEVER filesystem- or application-consistent. The SDK proves why: the only
// application-consistency mechanism in either recovery-point model
// (vmm VmRecoveryPoint, dataprotection VmRecoveryPoint) is VssProperties —
// Windows VSS state recorded guest-side — which this client never sets and
// takes no flag for. Wiring MUST NOT map these to a `filesystem`
// consistency cell; that claim needs proof this client does not have.
//
// Contract provenance (ntnx-api-golang-clients, authoritative over these
// fixtures): vmm-go-client/api/vm_api.go RevertVm
// (POST .../vms/{extId}/$actions/revert, body {vmRecoveryPointExtId}) and
// vmm-go-client/api/vm_recovery_points_api.go (POST/GET
// .../ahv/config/vm-recovery-points, DELETE .../{extId}); models in
// vmm-go-client/models/vmm/v4/ahv/config/config_model.go (VmRecoveryPoint
// carries name/vmExtId/creationTime; RevertParams requires
// vmRecoveryPointExtId). The dataprotection top-level recovery points are
// deliberately NOT used: they wrap VM points in a second entity the per-VM
// contract does not need.
//
// Two known caveats, stated not hidden: the VmRecoveryPoint model marks
// projectExtId required for authorization — project-scoped PCs may refuse a
// create without it, in which case the remedy is a default project, not a
// client flag; and list filtering sends $filter=vmExtId eq (param and field
// both SDK-proven) while ALSO filtering client-side, so an uncooperative
// filter degrades to paging rather than failing.

import (
	"fmt"
	"net/url"
	"strconv"
)

// CrashConsistent is the only consistency this client ever reports. A named
// constant rather than a string literal, so a future `filesystem` claim has
// to delete this line to lie — reviewable in one glance.
const CrashConsistent = "crash"

// SnapshotInfo is one VM recovery point as vmm records it.
type SnapshotInfo struct {
	ExtID       string `json:"extId"`
	Name        string `json:"name"`
	CreatedAt   string `json:"creationTime"`
	Consistency string `json:"consistency"`
	// VMExtID is the VM this point was taken of — the key ListSnapshots
	// filters on. It is identity plumbing, not a consistency claim.
	VMExtID string `json:"vmExtId"`
}

// vmRecoveryPointsResponse is the v4 list envelope for VM recovery points.
type vmRecoveryPointsResponse struct {
	Data []SnapshotInfo `json:"data"`
	Meta struct {
		TotalAvailableResults int `json:"totalAvailableResults"`
	} `json:"metadata"`
}

// SupportsQuiesce reports whether guest-quiesced snapshots exist. False:
// quiesce on this path would mean VSS properties the client cannot produce,
// and a quiesce parameter that silently snapshots crash-consistent anyway
// would be the lie the spike warned about. The adapter layer asks this
// instead of guessing.
func (c *Client) SupportsQuiesce() bool { return false }

// rpPath prefixes the versioned vmm recovery-point collection.
func (c *Client) rpPath(suffix string) string {
	return "/vmm/" + c.cfg.APIVersion + "/ahv/config/vm-recovery-points" + suffix
}

// ListSnapshots returns every recovery point of the VM, following every
// page. The request carries $filter=vmExtId eq and the client re-checks
// each point itself, so a server that ignores the filter costs pages, not
// correctness.
func (c *Client) ListSnapshots(vmName string) ([]SnapshotInfo, error) {
	vm, err := c.Resolve(vmName)
	if err != nil {
		return nil, err
	}
	const limit = 100
	var out []SnapshotInfo
	for page := 0; ; page++ {
		q := url.Values{}
		q.Set("$page", strconv.Itoa(page))
		q.Set("$limit", strconv.Itoa(limit))
		q.Set("$filter", "vmExtId eq '"+odataEscape(vm.ExtID)+"'")
		var resp vmRecoveryPointsResponse
		if err := c.get(c.rpPath("?"+q.Encode()), &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Data {
			if p.VMExtID != "" && p.VMExtID != vm.ExtID {
				continue
			}
			p.Consistency = CrashConsistent
			out = append(out, p)
		}
		if len(resp.Data) < limit || len(out) >= resp.Meta.TotalAvailableResults {
			return out, nil
		}
	}
}

// CreateSnapshot captures a crash-consistent recovery point of the VM's
// disks: POST .../vm-recovery-points with {name, vmExtId} per the
// VmRecoveryPoint model. There is no memory flag (points are disk-only,
// unlike a vSphere snapshot with memory) and no quiesce flag (see
// SupportsQuiesce).
func (c *Client) CreateSnapshot(vmName, snap string) (Task, error) {
	vm, err := c.Resolve(vmName)
	if err != nil {
		return Task{}, err
	}
	var resp actionResponse
	body := map[string]any{"name": snap, "vmExtId": vm.ExtID}
	if err := c.post(c.rpPath(""), body, &resp); err != nil {
		return Task{}, err
	}
	if resp.Data.ExtID == "" {
		return Task{}, fmt.Errorf("nutanix: snapshot %q of %q: recovery point returned no task extId", snap, vmName)
	}
	return Task{ExtID: resp.Data.ExtID}, nil
}

// resolveSnapshot finds a recovery point by name, refusing ambiguity the
// way Resolve refuses it for VMs.
func (c *Client) resolveSnapshot(vmName, snap string) (SnapshotInfo, error) {
	points, err := c.ListSnapshots(vmName)
	if err != nil {
		return SnapshotInfo{}, err
	}
	var found []SnapshotInfo
	for _, p := range points {
		if p.Name == snap {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return SnapshotInfo{}, fmt.Errorf("nutanix: no snapshot named %q on %q", snap, vmName)
	case 1:
		return found[0], nil
	default:
		return SnapshotInfo{}, fmt.Errorf("nutanix: %d snapshots named %q on %q; use the extId", len(found), snap, vmName)
	}
}

// DeleteSnapshot deletes a recovery point, leaving the VM untouched:
// DELETE .../vm-recovery-points/{extId}, no ETag dance — the resolve just
// before it is the guard.
func (c *Client) DeleteSnapshot(vmName, snap string) (Task, error) {
	p, err := c.resolveSnapshot(vmName, snap)
	if err != nil {
		return Task{}, err
	}
	return c.deleteTask(c.rpPath("/"+p.ExtID), "")
}

// RevertSnapshot rolls the VM back to a recovery point in place: the vmm
// RevertVm action (POST .../vms/{extId}/$actions/revert) with the required
// {vmRecoveryPointExtId}, resolved through the point's name.
func (c *Client) RevertSnapshot(vmName, snap string) (Task, error) {
	p, err := c.resolveSnapshot(vmName, snap)
	if err != nil {
		return Task{}, err
	}
	vm, err := c.Resolve(vmName)
	if err != nil {
		return Task{}, err
	}
	var resp actionResponse
	body := map[string]any{"vmRecoveryPointExtId": p.ExtID}
	if err := c.post(c.vmmPath("/"+vm.ExtID+"/$actions/revert"), body, &resp); err != nil {
		return Task{}, err
	}
	if resp.Data.ExtID == "" {
		return Task{}, fmt.Errorf("nutanix: revert %q to %q: mutation returned no task extId", vmName, snap)
	}
	return Task{ExtID: resp.Data.ExtID}, nil
}

// deleteTask issues a DELETE and decodes the 202 task envelope. etag is the
// If-Match value; empty means no precondition. The idempotency header still
// goes out on a bodyless DELETE (see client.do).
func (c *Client) deleteTask(path, etag string) (Task, error) {
	var extra map[string]string
	if etag != "" {
		extra = map[string]string{"If-Match": etag}
	}
	var resp actionResponse
	if err := c.doWithHeaders("DELETE", path, nil, &resp, extra); err != nil {
		return Task{}, err
	}
	if resp.Data.ExtID == "" {
		return Task{}, fmt.Errorf("nutanix: DELETE %s returned no task extId", path)
	}
	return Task{ExtID: resp.Data.ExtID}, nil
}
