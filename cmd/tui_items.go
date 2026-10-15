package cmd

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/export"
	"github.com/tuna-os/corral/pkg/types"
)

// ── VM item for the list ──────────────────────────────────────────

type vmItem struct {
	vm      types.VM
	display string
	state   runState
}

func (i vmItem) Title() string       { return i.vm.Name }
func (i vmItem) Description() string { return i.display }
func (i vmItem) FilterValue() string {
	return strings.Join([]string{i.vm.Name, i.vm.ID, i.vm.Backend, i.vm.Context, i.vm.Namespace, i.vm.Node, i.vm.IP}, " ")
}

func vmToItem(vm types.VM, th theme) vmItem {
	proxy := tuiProxyOff
	if vm.VNC == "on" {
		proxy = tuiProxyOn
	} else if vm.VNC == "pending" {
		proxy = "◐"
	}
	// Backend first and in its own colour, so a mixed fleet reads as groups
	// rather than as a wall of identical rows.
	display := fmt.Sprintf("%s  %s  ports:%s  %d CPU / %s",
		th.backendChip(vm.Backend), th.muted.Render(statusWords(vm.Status)), proxy, vm.CPU, vm.Mem)
	if vm.Node != "" && vm.Node != "—" {
		display += "  " + th.muted.Render(vm.Node)
	}
	if vm.IP != "" {
		display += "  " + lipgloss.NewStyle().Foreground(th.p.accent).Render(vm.IP)
	}
	// The pool, when it has one. Shown because a grouping nobody can see on the
	// row is a grouping nobody uses — and omitted when empty, so a fleet that
	// has not been organised does not carry a column of "(none)".
	if pool := poolLabel(vm.Ref()); pool != "" {
		display += "  " + th.chip.Render(pool)
	}
	return vmItem{vm: vm, display: display, state: classifyStatus(vm.Status, vm.Running)}
}

// ── CT item for the list ────────────────────────────────────────────
//
// A CT is not a types.VM (see pkg/ct's package doc — deliberately not a
// types.Backend peer), so it gets its own list.Item rather than being
// coerced into vmItem's shape. list.Model just wants anything satisfying
// list.Item, so vmItem and ctItem values mix freely in one []list.Item —
// same "merged, distinguished by icon" list the web UI's tree now uses,
// matching real Proxmox's own single resource tree per node/pool.

type ctItem struct {
	ct      ct.CT
	display string
	state   runState
}

func (i ctItem) Title() string       { return "[CT] " + i.ct.Name }
func (i ctItem) Description() string { return i.display }
func (i ctItem) FilterValue() string { return i.ct.Name }

func ctToItem(c ct.CT, th theme) ctItem {
	priv := ""
	if c.Privileged {
		priv = "  privileged"
	}
	return ctItem{
		ct: c,
		display: fmt.Sprintf("%s  %s  %d CPU / %s%s",
			th.chip.Foreground(th.p.pending).Bold(true).Render("ct"),
			th.muted.Render(statusWords(c.Phase)), c.CPU, c.Mem, priv),
		state: classifyStatus(c.Phase, c.Ready),
	}
}

// ── Action item for the actions menu ──────────────────────────────

type actionItem struct {
	id    string
	label string
}

func (a actionItem) Title() string       { return a.label }
func (a actionItem) Description() string { return "" }
func (a actionItem) FilterValue() string { return a.label }

var actionsListItems = []actionItem{
	{id: "start", label: "Start"},
	{id: "stop", label: "Stop"},
	{id: "restart", label: "Restart"},
	{id: "pause", label: "Pause"},
	{id: "unpause", label: "Resume"},
	{id: "migrate", label: "Migrate (live, same backend)"},
	{id: "move", label: "Move to another backend"},
	{id: "pool", label: "Pool…"},
	{id: "clone", label: "Clone"},
	{id: "hardware", label: "Edit CPU / RAM"},
	{id: "snapshot", label: "Snapshots"},
	{id: "template", label: "Make template"},
	{id: "events", label: "Events"},
	{id: "export", label: "Export (backup disk)"},
	{id: "ssh", label: "SSH"},
	{id: "viewer", label: "Viewer (VNC)"},
	{id: "ports", label: "Edit ports"},
	{id: "delete", label: "Delete"},
}

// actionApplies reports whether an action is a valid transition from the
// VM's current power state — the menu only offers what can actually happen
// (Proxmox-style), instead of a fixed list where "Start" leads on a VM
// that's already running.
func actionApplies(id string, vm types.VM) bool {
	paused := strings.Contains(vm.Status, "Paused")
	switch id {
	case "start":
		return !vm.Running
	case "stop", "restart":
		return vm.Running
	case "pause":
		return vm.Running && !paused
	case "unpause":
		return paused
	case "migrate":
		return vm.Capabilities.Migrate && vm.Running && !paused
	case "snapshot":
		return vm.Capabilities.Snapshots
	case "clone":
		return vmProvides[backend.Cloner](vm)
	case "template":
		return vmProvides[backend.Templater](vm)
	case "events":
		return vmProvides[backend.Eventer](vm)
	case "hardware":
		return vmProvides[backend.Sizer](vm)
	case "ports":
		return vmProvides[backend.Publisher](vm)
	case "export":
		return export.Supported(vm.Backend)
	case "ssh", "viewer":
		if id == "ssh" {
			return vm.Capabilities.SSH && vm.Running && !paused
		}
		return vm.Capabilities.VNC && vm.Running && !paused
	case "move", "pool":
		// Always offered. Pooling touches nothing, and for a move the preflight
		// is the authority on whether it can run — it explains every refusal in
		// one screen, which is more useful than a menu entry quietly missing.
		return true
	case "delete":
		return vm.Capabilities.Delete
	default:
		return true
	}
}

// vmProvides reports whether the VM's backend adapter implements family T, so
// the menu offers an action exactly where the operation behind it exists.
// Building an adapter never dials anything (see backend.probe), which keeps
// this cheap enough to ask per menu entry.
func vmProvides[T any](vm types.VM) bool {
	adapter, err := backend.For(vm.Ref())
	if err != nil {
		return false
	}
	_, ok := adapter.(T)
	return ok
}

func ctActionApplies(id string, c ct.CT) bool {
	switch id {
	case "start":
		return c.Phase != "Running"
	case "stop", "console":
		return c.Phase == "Running"
	default:
		return true
	}
}

// templateActionLabel names the template action for the mark the VM already
// carries, the way the web UI's button flips between the two — a single
// "Toggle template" would leave the operator guessing which way it goes.
func templateActionLabel(vm types.VM) string {
	if vm.IsTemplate {
		return "Unmark template"
	}
	return "Make template"
}

// CT actions are a small, distinct set — no migrate/snapshot/hardware-edit/
// ports/clone, since those are VM (hypervisor) concepts a plain pod doesn't
// have. "Console" replaces "SSH": a CT is reached by kubectl exec, not a
// virtctl SSH tunnel.
var actionsListItemsCT = []actionItem{
	{id: "start", label: "Start"},
	{id: "stop", label: "Stop"},
	{id: "console", label: "Console"},
	{id: "hardware", label: "Edit CPU / RAM"},
	{id: "delete", label: "Delete"},
}

// ── Main model ────────────────────────────────────────────────────
