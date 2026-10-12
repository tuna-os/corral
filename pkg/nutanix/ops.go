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

// action posts one $actions verb against the resolved VM and returns its
// task. The idempotency header goes out on every call (see client.do), so a
// retried power op does not execute twice.
func (c *Client) action(name, verb string) (Task, error) {
	vm, err := c.Resolve(name)
	if err != nil {
		return Task{}, err
	}
	var resp actionResponse
	if err := c.post(c.vmmPath("/"+vm.ExtID+"/$actions/"+verb), map[string]any{}, &resp); err != nil {
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

// Stop asks the guest to shut down cleanly (guest tools / ACPI). The hard
// power cut is Kill, so the destructive one has to be chosen by name —
// Corral's Stop never escalates to it quietly.
func (c *Client) Stop(name string) (Task, error) {
	return c.action(name, "shutdown")
}

// Kill cuts the power. An unclean shutdown of somebody's database is not a
// fallback to apply quietly.
func (c *Client) Kill(name string) (Task, error) {
	return c.action(name, "power-off")
}

// Restart reboots through the guest rather than stop-then-start. The dance
// loses the guest's own shutdown ordering and races with autostart.
func (c *Client) Restart(name string) (Task, error) {
	return c.action(name, "reboot")
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
