package nutanix

// The operations, one per row of the parity matrix.
//
// Every one of them resolves a name to an extId first, which is what lets a
// caller pass the name a human uses while Prism Central gets the UUID it
// needs. Each mutation returns its v4 task extId so a surface can wait, poll,
// or ignore it — the choice belongs to the caller, not to the backend.
//
// Action verbs are the v4 $actions sub-resource slugs
// (POST /vmm/{ver}/ahv/config/vms/{extId}/$actions/<verb>), the same shape
// the portal documents for e.g. $actions/associate-categories, with the
// power verbs confirmed in the v4 vmm action index. They will be verified
// against a live PC during wiring; the httptest seam below pins exactly what
// this client sends so any correction lands in one place.

import (
	"fmt"
	"time"
)

// DefaultTimeout bounds a wait a caller did not size itself.
const DefaultTimeout = 5 * time.Minute

// Task is a v4 task id. Mutations return 202 Accepted with the task extId;
// completion is polled through the prism namespace.
type Task struct {
	ExtID string
}

// Valid reports whether this is a real task. A synchronous endpoint returns
// no task, and waiting on nothing should be a no-op rather than an error.
func (t Task) Valid() bool { return t.ExtID != "" }

// actionResponse is the 202 envelope: the task extId, not the resource.
type actionResponse struct {
	Data struct {
		ExtID string `json:"extId"`
	} `json:"data"`
}

// action posts one $actions verb with an empty body against the resolved VM
// and returns its task. The idempotency header goes out on every call (see
// client.do), so a retried power op does not execute twice.
func (c *Client) action(name, verb string) (Task, error) {
	return c.actionWithBody(name, verb, map[string]any{})
}

// actionWithBody posts one $actions verb with a JSON body against the
// resolved VM and returns its task. Bodies for verbs beyond power (migrate,
// clone, revert) follow the v4 reference shape provisionally: the verb slugs
// are confirmed in the portal action index but exact body keys are verified
// against a live PC at wiring time, and the httptest seam pins exactly what
// this client sends so corrections land in one place.
func (c *Client) actionWithBody(name, verb string, body any) (Task, error) {
	vm, err := c.Resolve(name)
	if err != nil {
		return Task{}, err
	}
	var resp actionResponse
	if err := c.post(c.vmmPath("/"+vm.ExtID+"/$actions/"+verb), body, &resp); err != nil {
		return Task{}, err
	}
	if resp.Data.ExtID == "" {
		return Task{}, fmt.Errorf("nutanix: %s %s: mutation returned no task extId", verb, name)
	}
	return Task{ExtID: resp.Data.ExtID}, nil
}

// Start powers a VM on.
func (c *Client) Start(name string) (Task, error) {
	return c.action(name, "power-on")
}

// Stop shuts a VM down gracefully via the guest first and falls back to a
// hard power-off when the guest does not accept the request (no tools, or an
// already-stopped guest) — the vsphere Stop shape. Graceful first gives the
// guest its shutdown ordering; hard only when graceful fails. The graceful
// attempt is waited on (bounded by DefaultTimeout) because only its terminal
// task state tells whether the guest cooperated; the returned task is the
// graceful one when it succeeded, the hard fallback's when it did not.
func (c *Client) Stop(name string) (Task, error) {
	return c.gracefulOrHard(name, "shutdown", "power-off")
}

// gracefulOrHard issues the graceful verb, waits for its task, and issues
// the hard verb when the guest refused. A graceful request that PC itself
// rejects (unknown VM already resolved, auth, transport) returns before any
// fallback: failing closed beats powering off a guest PC never heard.
func (c *Client) gracefulOrHard(name, graceful, hard string) (Task, error) {
	task, err := c.action(name, graceful)
	if err != nil {
		return Task{}, err
	}
	if err := c.WaitTask(task, DefaultTimeout); err == nil {
		return task, nil
	}
	return c.action(name, hard)
}

// Kill cuts the power directly, with no graceful attempt first. It is the
// explicit hard path: Stop may escalate to this on guest refusal, but
// nothing else does — an unclean shutdown of somebody's database is not a
// fallback to apply quietly.
func (c *Client) Kill(name string) (Task, error) {
	return c.action(name, "power-off")
}

// Restart reboots through the guest rather than stop-then-start — the dance
// loses the guest's own shutdown ordering and races with autostart — and
// falls back to a hard power cycle when the guest refuses, mirroring Stop.
func (c *Client) Restart(name string) (Task, error) {
	return c.gracefulOrHard(name, "reboot", "power-cycle")
}

// Reset is the hard power cycle (the reset button): the guest reboots
// immediately without guest cooperation. Distinct from Stop-then-Start,
// which is a power cycle with shutdown ordering rather than a reset.
func (c *Client) Reset(name string) (Task, error) {
	return c.action(name, "power-cycle")
}

// Pause suspends a running guest to memory; its state stays allocated.
// Resume continues from that exact point.
func (c *Client) Pause(name string) (Task, error) {
	return c.action(name, "suspend")
}

// Resume unpauses a suspended guest.
func (c *Client) Resume(name string) (Task, error) {
	return c.action(name, "resume")
}

// ── movement ────────────────────────────────────────────────────────

// Migrate live-migrates a running VM to another AHV host. AHV migration is
// natively live for a running guest — no shared-storage precondition to
// check the way PVE demands — so there is no precondition call here, just
// the action. target is a host extId; an empty target is refused rather
// than guessed, because picking a host needs the clustermgmt host listing
// that lands with wiring, and migrating to a node Corral chose blind is how
// a guest lands on a host with no capacity.
func (c *Client) Migrate(name, target string) (Task, error) {
	if target == "" {
		return Task{}, fmt.Errorf("nutanix: migrate %q: a destination host extId is required (host listing lands with wiring)", name)
	}
	return c.actionWithBody(name, "migrate", map[string]any{
		"destinationHost": map[string]any{"extId": target},
	})
}

// Clone makes a full clone of the source under the target name. Full, not
// linked: a linked clone depends on its parent forever, and deleting the
// parent of a linked clone is a footgun Corral should not hand over by
// default — the proxmoxbe rationale, and the v4 clone action exists for
// exactly this.
func (c *Client) Clone(source, target string) (Task, error) {
	return c.actionWithBody(source, "clone", map[string]any{"name": target})
}

// ── export ──────────────────────────────────────────────────────────
//
// There is deliberately no Export method. The v4 spike proved no
// Corral-shaped export: no documented "download this VM as an image"
// endpoint in vmm, and the dataprotection export flows are unproven against
// fixtures. Like vsphere's ExportVm (no simulator handler, stays a Possible
// cell), export stays out until a fixture-backed slice lands it — a method
// that half-downloads a disk image is worse than no method.

// ── tasks ─────────────────────────────────────────────────────────

// TaskStatus is a v4 task's current state.
type TaskStatus struct {
	Status string `json:"status"`
}

// Done reports whether the task reached a terminal state.
func (s TaskStatus) Done() bool {
	switch s.Status {
	case "SUCCEEDED", "FAILED", "CANCELLED", "CANCELED":
		return true
	}
	return false
}

// OK reports success. Anything terminal that is not SUCCEEDED carries the
// failure, and surfacing the status verbatim beats "task failed".
func (s TaskStatus) OK() bool { return s.Status == "SUCCEEDED" }

// TaskStatus fetches a task's state from the prism namespace.
func (c *Client) TaskStatus(t Task) (TaskStatus, error) {
	var resp struct {
		Data TaskStatus `json:"data"`
	}
	if err := c.get("/prism/"+c.cfg.APIVersion+"/config/tasks/"+t.ExtID, &resp); err != nil {
		return TaskStatus{}, err
	}
	return resp.Data, nil
}

// WaitTask polls until the task finishes or the deadline passes.
func (c *Client) WaitTask(t Task, timeout time.Duration) error {
	if !t.Valid() {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		st, err := c.TaskStatus(t)
		if err != nil {
			return err
		}
		if st.Done() {
			if st.OK() {
				return nil
			}
			return fmt.Errorf("nutanix: task %s ended %s", t.ExtID, st.Status)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("nutanix: task %s not done after %s (last status %q)", t.ExtID, timeout, st.Status)
		}
		time.Sleep(2 * time.Second)
	}
}
