package nutanix

// Snapshots, via the dataprotection namespace's recovery points.
//
// A v4 VM snapshot is a crash-consistent recovery point of the VM's disks —
// NEVER filesystem- or application-consistent. The NX-0 spike was explicit:
// guest-quiesced snapshots need in-guest VSS/NGT cooperation no v4 VM action
// proves, so this client takes no quiesce flag at all (unlike vsphere's
// CreateSnapshot, where ToolsRunning can honor one) and labels every point
// crash-consistent. Wiring MUST NOT map these to a `filesystem` consistency
// cell; that claim needs proof this client does not have.
//
// Paths and bodies below are provisional v4 dataprotection shapes, pinned by
// the httptest seam and verified against a live PC at wiring time — the same
// standing arrangement as the $actions verbs in ops.go. The one confirmed
// fact they rest on is the vmm action index entry "Revert VM … from a
// specified VM Recovery Point".

import (
	"fmt"
	"net/url"
)

// CrashConsistent is the only consistency this client ever reports. A named
// constant rather than a string literal, so a future `filesystem` claim has
// to delete this line to lie — reviewable in one glance.
const CrashConsistent = "crash"

// SnapshotInfo is one recovery point as dataprotection records it.
type SnapshotInfo struct {
	ExtID       string `json:"extId"`
	Name        string `json:"name"`
	CreatedAt   string `json:"creationTime"`
	Consistency string `json:"consistency"`
	// VMExtID is the VM this point was taken of — the key ListSnapshots
	// filters on. It is identity plumbing, not a consistency claim.
	VMExtID string `json:"vmExtId"`
}

// recoveryPointsResponse is the v4 list envelope for recovery points.
type recoveryPointsResponse struct {
	Data []SnapshotInfo `json:"data"`
	Meta struct {
		TotalAvailableResults int `json:"totalAvailableResults"`
	} `json:"metadata"`
}

// SupportsQuiesce reports whether guest-quiesced snapshots exist. False:
// there is no v4 VM snapshot quiesce flag, and a quiesce parameter that
// silently snapshots crash-consistent anyway would be the lie the spike
// warned about. The adapter layer asks this instead of guessing.
func (c *Client) SupportsQuiesce() bool { return false }

// dpPath prefixes the versioned dataprotection namespace.
func (c *Client) dpPath(suffix string) string {
	return "/dataprotection/" + c.cfg.APIVersion + suffix
}

// ListSnapshots returns every recovery point of the VM.
func (c *Client) ListSnapshots(vmName string) ([]SnapshotInfo, error) {
	vm, err := c.Resolve(vmName)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("$filter", "vmExtId eq '"+odataEscape(vm.ExtID)+"'")
	var resp recoveryPointsResponse
	if err := c.get(c.dpPath("/recovery-points?"+q.Encode()), &resp); err != nil {
		return nil, err
	}
	for i := range resp.Data {
		resp.Data[i].Consistency = CrashConsistent
	}
	return resp.Data, nil
}

// CreateSnapshot captures a crash-consistent recovery point of the VM's
// disks. There is no memory flag (recovery points are disk-only, unlike a
// vSphere snapshot with memory) and no quiesce flag (see SupportsQuiesce).
func (c *Client) CreateSnapshot(vmName, snap string) (Task, error) {
	vm, err := c.Resolve(vmName)
	if err != nil {
		return Task{}, err
	}
	var resp actionResponse
	body := map[string]any{"name": snap, "vmExtIds": []string{vm.ExtID}}
	if err := c.post(c.dpPath("/recovery-points"), body, &resp); err != nil {
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
func (c *Client) DeleteSnapshot(vmName, snap string) (Task, error) {
	p, err := c.resolveSnapshot(vmName, snap)
	if err != nil {
		return Task{}, err
	}
	return c.deleteTask(c.dpPath("/recovery-points/"+p.ExtID), "")
}

// RevertSnapshot rolls the VM back to a recovery point in place — the vmm
// "Revert VM from Recovery Point" action, resolved through the point's name.
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
	body := map[string]any{"recoveryPointExtId": p.ExtID}
	if err := c.post(c.vmmPath("/"+vm.ExtID+"/$actions/revert"), body, &resp); err != nil {
		return Task{}, err
	}
	if resp.Data.ExtID == "" {
		return Task{}, fmt.Errorf("nutanix: revert %q to %q: mutation returned no task extId", vmName, snap)
	}
	return Task{ExtID: resp.Data.ExtID}, nil
}

// deleteTask issues a DELETE and decodes the 202 task envelope. etag is the
// If-Match value; empty means no precondition (recovery-point deletes read
// the point just before deleting it, so the lookup is the guard). The
// idempotency header still goes out on a bodyless DELETE (see client.do).
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
