// Operations: power, snapshot, clone, migrate, export.
//
// Every method resolves the VM by name or UUID first, then invokes the
// matching XenAPI VM message. Signatures follow the XenAPI VM class
// (https://xapi-project.github.io/xen-api/classes/vm.html):
//
//	VM.start(session, ref, paused, force)
//	VM.clean_shutdown / VM.hard_shutdown(session, ref)
//	VM.clean_reboot / VM.hard_reboot(session, ref)
//	VM.suspend(session, ref)
//	VM.snapshot(session, ref, new_name, empty VDI set)
//	VM.revert(session, snapshot ref)
//	VM.clone(session, ref, new_name)
//	VM.pool_migrate(session, ref, host, options)
//
// VM export is not an XML-RPC method at all: it is an HTTP GET on /export
// authenticated by session_id
// (https://xapi-project.github.io/xen-api/importexport.html).

package xen

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Start boots a halted VM (VM.start with paused=false, force=false — a plain
// boot, never a forced one).
func (c *Client) Start(name string) error {
	ref, err := c.VMRef(name)
	if err != nil {
		return err
	}
	_, err = c.call("VM.start", ref, false, false)
	return err
}

// Stop shuts the VM down gracefully via a clean shutdown first and falls
// back to a hard shutdown when the guest does not cooperate (a halted guest,
// a guest without tools). Graceful first gives the guest its shutdown
// ordering; hard only when graceful fails — the same policy pkg/virtualbox
// applies with ACPI-then-poweroff.
func (c *Client) Stop(name string) error {
	ref, err := c.VMRef(name)
	if err != nil {
		return err
	}
	if _, err := c.call("VM.clean_shutdown", ref); err == nil {
		return nil
	} else if apiErr, ok := err.(*APIError); ok && apiErr.Code == "VM_BAD_POWER_STATE" {
		return err
	}
	_, err = c.call("VM.hard_shutdown", ref)
	return err
}

// Reboot asks the guest to reboot cleanly first and forces a hard reboot
// when the guest ignores the request.
func (c *Client) Reboot(name string) error {
	ref, err := c.VMRef(name)
	if err != nil {
		return err
	}
	if _, err := c.call("VM.clean_reboot", ref); err == nil {
		return nil
	}
	_, err = c.call("VM.hard_reboot", ref)
	return err
}

// Suspend freezes the guest in place; its memory stays allocated. Resume
// continues from that exact point. Distinct from Stop/Start, which is a
// reboot.
func (c *Client) Suspend(name string) error {
	ref, err := c.VMRef(name)
	if err != nil {
		return err
	}
	_, err = c.call("VM.suspend", ref)
	return err
}

// Resume continues a suspended VM on its current host.
func (c *Client) Resume(name string) error {
	ref, err := c.VMRef(name)
	if err != nil {
		return err
	}
	_, err = c.call("VM.resume", ref, false, false)
	return err
}

// Snapshot takes a VM snapshot named name, returning the snapshot's
// reference. The empty VDI set means "snapshot every disk", which is the
// documented default behavior of the VDI-set parameter.
func (c *Client) Snapshot(vmName, name string) (string, error) {
	ref, err := c.VMRef(vmName)
	if err != nil {
		return "", err
	}
	v, err := c.call("VM.snapshot", ref, name, []any{})
	if err != nil {
		return "", err
	}
	snap, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("xen: VM.snapshot returned %T, want a VM reference", v)
	}
	return snap, nil
}

// Revert rolls the VM back to the named snapshot. XenAPI addresses the
// snapshot by its own reference: the argument is resolved by name first,
// and a VM name that is itself the snapshot works unchanged.
func (c *Client) Revert(snapshot string) error {
	ref, err := c.VMRef(snapshot)
	if err != nil {
		return err
	}
	_, err = c.call("VM.revert", ref)
	return err
}

// Clone copies the VM's metadata (and, for a template, provisions its
// disks), returning the new VM's reference under the given name.
func (c *Client) Clone(vmName, name string) (string, error) {
	ref, err := c.VMRef(vmName)
	if err != nil {
		return "", err
	}
	v, err := c.call("VM.clone", ref, name)
	if err != nil {
		return "", err
	}
	clone, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("xen: VM.clone returned %T, want a VM reference", v)
	}
	return clone, nil
}

// Migrate live-migrates a running VM to the named host
// (VM.pool_migrate with empty options — no VDI/VIF remapping, which is the
// cross-pool migrate_send path, not intra-pool motion).
func (c *Client) Migrate(vmName, host string) error {
	ref, err := c.VMRef(vmName)
	if err != nil {
		return err
	}
	hostRef, err := c.HostRef(host)
	if err != nil {
		return err
	}
	_, err = c.call("VM.pool_migrate", ref, hostRef, map[string]any{})
	return err
}

// HostRef returns the opaque reference for a host by name, label, or
// hostname.
func (c *Client) HostRef(name string) (string, error) {
	hosts, err := c.Hosts()
	if err != nil {
		return "", err
	}
	for ref, h := range hosts {
		if h.Name == name || h.Hostname == name || h.UUID == name || ref == name {
			return ref, nil
		}
	}
	return "", fmt.Errorf("xen: no host named %q", name)
}

// Export downloads the VM as an XVA image via the /export HTTP endpoint,
// streaming the body so a multi-GB disk never sits in memory. The caller
// closes the stream.
func (c *Client) Export(name string) (io.ReadCloser, error) {
	vmRef, err := c.VMRef(name)
	if err != nil {
		return nil, err
	}
	sess := c.cachedSession()
	if sess == "" {
		if sess, err = c.Login(); err != nil {
			return nil, err
		}
	}
	endpoint := c.base + "export?" + url.Values{
		"session_id": {sess},
		"ref":        {vmRef},
	}.Encode()
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("xen: export %q: %w", name, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xen: export %q: %w", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("xen: export %q: unexpected HTTP status %s", name, resp.Status)
	}
	return resp.Body, nil
}
