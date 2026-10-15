// Package virtualbox manages local VirtualBox VMs through VBoxManage.
package virtualbox

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
)

var runner shell.Runner = shell.Real{}

// SetRunner swaps the command runner (production VBoxManage vs. shell.Fake
// in tests), mirroring pkg/libvirt.
func SetRunner(r shell.Runner) { runner = r }

// Client talks to the VirtualBox on this host. VirtualBox has no remote
// connection URI the way libvirt does, so there are no connection flags to
// prepend; Context is a local name label kept for future multi-host use and
// is recorded on listed VMs.
type Client struct {
	Context string
}

// NewClient returns a Client scoped to the given local context name, which
// may be empty.
func NewClient(context string) Client { return Client{Context: context} }

func (c Client) run(args ...string) ([]byte, error) {
	return runner.Run("VBoxManage", args...)
}

// List enumerates every registered VM (`VBoxManage list vms`, one
// `"name" {uuid}` line each) and enriches each with `showvminfo <name>
// --machinereadable`. A per-VM showvminfo failure leaves that VM with just
// its listed name rather than failing the whole listing, mirroring
// libvirt.List's tolerance of domstate/dominfo failures.
func (c Client) List() ([]types.VM, error) {
	out, err := c.run("list", "vms")
	if err != nil {
		return nil, fmt.Errorf("virtualbox list: %w", err)
	}
	var vms []types.VM
	for _, line := range strings.Split(string(out), "\n") {
		name, ok := parseListLine(line)
		if !ok {
			continue
		}
		vm := types.VM{
			Name:      name,
			Backend:   "virtualbox",
			Context:   c.Context,
			Namespace: "virtualbox",
			Node:      "localhost",
		}
		if info, err := c.run("showvminfo", name, "--machinereadable"); err == nil {
			fields := parseMachineReadable(string(info))
			if n, ok := fields["name"]; ok && n != "" {
				vm.Name = n
			}
			state := fields["VMState"]
			vm.Status = state
			// State mapping, deliberately simple and matching the
			// libvirt precedent (Running only when the state string
			// contains "running"): "running" → Running+Ready, while
			// "paused", "saved", "poweroff", "aborted" and anything
			// else report Running=false, Ready=false. A paused guest
			// still holds its memory, but it is not executing, so it
			// must not present as running the way libvirt's paused
			// domains don't.
			running := strings.Contains(strings.ToLower(state), "running")
			vm.Running = running
			vm.Ready = running
			if n, err := strconv.Atoi(fields["cpus"]); err == nil {
				vm.CPU = n
			}
			if m := fields["memory"]; m != "" {
				vm.Mem = m + " MB"
			}
		}
		vm.SetIdentity()
		vms = append(vms, vm)
	}
	return vms, nil
}

// parseListLine parses one `VBoxManage list vms` line, shaped
// `"name" {uuid}`. Anything else is malformed and skipped.
func parseListLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, `"`) {
		return "", false
	}
	end := strings.Index(line[1:], `"`)
	if end < 0 {
		return "", false
	}
	name := line[1 : 1+end]
	rest := strings.TrimSpace(line[1+end+1:])
	if !strings.HasPrefix(rest, "{") || !strings.HasSuffix(rest, "}") {
		return "", false
	}
	if name == "" {
		return "", false
	}
	return name, true
}

// parseMachineReadable parses `showvminfo --machinereadable` output:
// `Key="value"` lines (values may be unquoted for numbers in some builds,
// so quotes are trimmed only when present).
func parseMachineReadable(out string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
			v = v[1 : len(v)-1]
		}
		fields[k] = v
	}
	return fields
}

// Exists reports whether a VM with the given name is registered.
func (c Client) Exists(name string) bool {
	_, err := c.run("showvminfo", name, "--machinereadable")
	return err == nil
}

// Start boots the VM headless (no VirtualBox GUI window).
func (c Client) Start(name string) error {
	_, err := c.run("startvm", name, "--type", "headless")
	return err
}

// Stop shuts the VM down gracefully via an ACPI power-button event first and
// falls back to a hard poweroff when the guest does not accept the ACPI
// request (powered-off guests, guests without ACPI drivers). Graceful first
// gives the guest its shutdown ordering; hard only when graceful fails.
func (c Client) Stop(name string) error {
	if _, err := c.run("controlvm", name, "acpipowerbutton"); err == nil {
		return nil
	}
	_, err := c.run("controlvm", name, "poweroff")
	return err
}

// Delete unregisters the VM and deletes its disk images (`unregistervm
// --delete`), mirroring libvirt's `undefine --remove-all-storage`.
func (c Client) Delete(name string) error {
	_, err := c.run("unregistervm", name, "--delete")
	return err
}

// Reset is the backend's own hard reset (the reset button): the guest reboots
// immediately without guest cooperation. Distinct from Stop-then-Start, which
// is a power cycle rather than a reset.
func (c Client) Reset(name string) error {
	_, err := c.run("controlvm", name, "reset")
	return err
}

// Pause freezes the guest in place; its memory stays allocated. Resume
// continues from that exact point. Distinct from Stop/Start, which is a
// reboot.
func (c Client) Pause(name string) error {
	_, err := c.run("controlvm", name, "pause")
	return err
}
func (c Client) Resume(name string) error {
	_, err := c.run("controlvm", name, "resume")
	return err
}

// Running reports whether the VM's VMState is "running". A showvminfo
// failure is an error, not "stopped": callers gate offline-only edits on it.
func (c Client) Running(name string) (bool, error) {
	out, err := c.run("showvminfo", name, "--machinereadable")
	if err != nil {
		return false, fmt.Errorf("virtualbox showvminfo %s: %w", name, err)
	}
	return parseMachineReadable(string(out))["VMState"] == "running", nil
}

// Clone makes a full, registered copy of source named target (`clonevm
// --register`). VirtualBox clones a running VM from its current state.
func (c Client) Clone(source, target string) error {
	if _, err := c.run("clonevm", source, "--name", target, "--register"); err != nil {
		return fmt.Errorf("virtualbox clonevm %s: %w", source, err)
	}
	return nil
}

// Scale sets vCPUs and memory with `modifyvm`, which VirtualBox only accepts
// on a powered-off VM — so a running VM is refused up front with the remedy,
// rather than letting VBoxManage fail with a session-lock error. Zero cores
// and empty mem leave that setting alone.
func (c Client) Scale(name string, cores int, mem string) error {
	args := []string{"modifyvm", name}
	if cores > 0 {
		args = append(args, "--cpus", strconv.Itoa(cores))
	}
	if mem != "" {
		mib, err := memoryMiB(mem)
		if err != nil {
			return err
		}
		args = append(args, "--memory", strconv.Itoa(mib))
	}
	if len(args) == 2 {
		return nil
	}
	running, err := c.Running(name)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf("virtualbox: %q is running and VirtualBox changes CPU and memory only while powered off; stop it first", name)
	}
	if _, err := c.run(args...); err != nil {
		return fmt.Errorf("virtualbox modifyvm %s: %w", name, err)
	}
	return nil
}

// memoryMiB reads a Corral memory size (4Gi, 2048Mi, 2G, or a bare MiB
// count) as whole MiB, the unit modifyvm --memory takes.
func memoryMiB(mem string) (int, error) {
	lower := strings.ToLower(strings.TrimSpace(mem))
	multiplier := 1
	switch {
	case strings.HasSuffix(lower, "gi"), strings.HasSuffix(lower, "gb"), strings.HasSuffix(lower, "g"):
		multiplier = 1024
	case strings.HasSuffix(lower, "mi"), strings.HasSuffix(lower, "mb"), strings.HasSuffix(lower, "m"):
	default:
		if strings.TrimLeft(lower, "0123456789.") != "" {
			return 0, fmt.Errorf("virtualbox: cannot read memory %q; use e.g. 4Gi, 2048Mi, or 2048", mem)
		}
	}
	value, err := strconv.ParseFloat(strings.TrimRight(lower, "gimb"), 64)
	if err != nil {
		return 0, fmt.Errorf("virtualbox: cannot read memory %q: %w", mem, err)
	}
	mib := int(value * float64(multiplier))
	if mib <= 0 {
		return 0, fmt.Errorf("virtualbox: memory %q is not a positive size", mem)
	}
	return mib, nil
}
