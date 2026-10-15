package cmd

// Backend work started from the TUI runs as a tea.Cmd, off the Update loop.
// A screen marks the model busy (the header spinner turns), returns one of the
// commands below, and the result comes back as a message that Update applies.
// A slow cluster then turns the spinner instead of freezing the terminal, and
// every command captures plain values rather than the model.

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/doctor"
	"github.com/tuna-os/corral/pkg/types"
)

// fleetSnapshot is the inventory as every backend reported it.
type fleetSnapshot struct {
	items  []list.Item
	errors map[string]string
}

// loadFleet lists every VM and CT, styled for th.
func loadFleet(th theme) fleetSnapshot {
	items, errs := loadTUIItems(th)
	cts, _ := ct.ListCTs()
	for _, c := range cts {
		items = append(items, ctToItem(c, th))
	}
	return fleetSnapshot{items: items, errors: errs}
}

// fleetMsg is the fleet after an operation, with the operation's outcome for
// the status bar. An empty notice leaves the status bar as it was.
type fleetMsg struct {
	fleet      fleetSnapshot
	notice     string
	noticeKind string
}

// cloneDoneMsg reports a clone. On failure the clone form stays open.
type cloneDoneMsg struct {
	err   error
	fleet fleetSnapshot
}

// createDoneMsg reports a create. On failure the create form stays open.
type createDoneMsg struct {
	name  string
	err   error
	fleet fleetSnapshot
}

// doctorMsg carries a fresh set of health checks.
type doctorMsg struct{ rows []doctor.Check }

// portsAppliedMsg reports the ports form's change; err is why it did not
// reach the cluster.
type portsAppliedMsg struct{ err error }

// hwAppliedMsg reports a hardware change. A non-empty status is why it
// failed, and keeps the form open.
type hwAppliedMsg struct {
	status string
	fleet  fleetSnapshot
}

// actionTarget is what an action applies to: a VM, or a CT when isCT is set.
type actionTarget struct {
	vm   types.VM
	ct   ct.CT
	isCT bool
}

func (m tuiModel) target() actionTarget {
	return actionTarget{vm: m.selected, ct: m.selectedCT, isCT: m.isCT}
}

// busyWith marks the model busy until a result message clears it.
func (m *tuiModel) busyWith(label string) {
	m.busy, m.busyLabel = true, label
}

// runActionCmd runs action on the selected VM or CT, then reloads the fleet.
func (m *tuiModel) runActionCmd(action string) tea.Cmd {
	t, th := m.target(), m.th
	name := t.vm.Name
	if t.isCT {
		name = t.ct.Name
	}
	m.busyWith(action + " " + name)
	return func() tea.Msg {
		notice, kind := t.run(action)
		return fleetMsg{fleet: loadFleet(th), notice: notice, noticeKind: kind}
	}
}

// refreshCmd reloads the fleet and reports notice (which may be empty).
func (m *tuiModel) refreshCmd(notice string) tea.Cmd {
	th := m.th
	m.busyWith("Refreshing")
	return func() tea.Msg {
		return fleetMsg{fleet: loadFleet(th), notice: notice}
	}
}

// cloneCmd clones the selected VM to dst.
func (m *tuiModel) cloneCmd(dst string) tea.Cmd {
	src, th := m.selected, m.th
	m.busyWith("Cloning " + src.Name)
	return func() tea.Msg {
		if err := runClone(src, dst); err != nil {
			return cloneDoneMsg{err: err}
		}
		return cloneDoneMsg{fleet: loadFleet(th)}
	}
}

// createCmd creates the VM the form describes.
func (m *tuiModel) createCmd() tea.Cmd {
	form, th := m.create, m.th
	name := strings.TrimSpace(form.name.Value())
	m.busyWith("Creating " + name)
	return func() tea.Msg {
		if err := applyCreate(form); err != nil {
			return createDoneMsg{name: name, err: err}
		}
		return createDoneMsg{name: name, fleet: loadFleet(th)}
	}
}

// doctorCmd runs the fleet health checks, reconciling what it can first when
// fix is set.
func (m *tuiModel) doctorCmd(fix bool) tea.Cmd {
	m.busyWith("Checking the fleet")
	return func() tea.Msg {
		if fix {
			// A fix that fails shows up in the checks that follow it.
			_, _ = doctor.Fix()
		}
		return doctorMsg{rows: fleetDiagnosis()}
	}
}

// portsCmd pushes the ports form's toggles to the cluster. The form stays
// open; the fleet is reloaded when it closes.
func (m *tuiModel) portsCmd() tea.Cmd {
	m.edit.apply = false
	vm, ports := m.edit.vm, m.edit.enabledPorts()
	m.busyWith("Applying ports")
	return func() tea.Msg {
		return portsAppliedMsg{err: applyPorts(vm, ports)}
	}
}

// editLoadedMsg carries a ports form filled with the VM's current ports.
type editLoadedMsg struct{ edit editModel }

// portsLoadCmd reads which ports the selected VM exposes and builds the form.
func (m *tuiModel) portsLoadCmd() tea.Cmd {
	vm := m.selected
	m.busyWith("Loading ports")
	return func() tea.Msg { return editLoadedMsg{edit: newEditModel(vm)} }
}

// hwApplyCmd scales the VM or CT to the hardware form's values.
func (m *tuiModel) hwApplyCmd() tea.Cmd {
	m.hwEdit.submit = false
	form, th := m.hwEdit, m.th
	m.busyWith("Applying hardware")
	return func() tea.Msg {
		if status := form.apply(); status != "" {
			return hwAppliedMsg{status: status}
		}
		return hwAppliedMsg{fleet: loadFleet(th)}
	}
}

// updateOp applies the result of a command from cmd/tui_ops.go,
// cmd/tui_views.go, or cmd/tui_pools.go, and reports whether msg was one.
func (m *tuiModel) updateOp(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case fleetMsg:
		m.busy = false
		m.applyFleet(msg.fleet)
		if msg.notice != "" {
			m.notice, m.noticeKind = msg.notice, msg.noticeKind
		}
	case cloneDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.cloneErr = msg.err.Error()
			return true
		}
		m.applyFleet(msg.fleet)
		m.mode = ModeList
	case createDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.create.err = msg.err.Error()
			return true
		}
		m.applyFleet(msg.fleet)
		m.mode = ModeList
		m.notice, m.noticeKind = "Created "+msg.name, "ok"
	case doctorMsg:
		m.busy = false
		m.doctorRows = msg.rows
	case editLoadedMsg:
		m.busy = false
		m.edit = msg.edit
	case portsAppliedMsg:
		m.busy = false
		if msg.err != nil {
			m.notice, m.noticeKind = "ports: "+msg.err.Error(), "error"
		}
	case hwAppliedMsg:
		m.busy = false
		// A failed scale keeps the form open with the reason on screen —
		// closing it would drop the only copy of the error.
		if msg.status != "" {
			m.hwEdit.status = msg.status
			return true
		}
		m.applyFleet(msg.fleet)
		m.mode = ModeList
	case snapshotsMsg:
		m.busy = false
		m.snapshots.apply(msg)
	case eventsMsg:
		m.busy = false
		m.events = msg.state
	case planMsg:
		m.busy = false
		m.pools.showPlan(msg.plan)
	case moveDoneMsg:
		m.busy = false
		m.applyFleet(msg.fleet)
		m.mode = ModeList
		if msg.err != nil {
			m.notice, m.noticeKind = "move failed: "+msg.err.Error(), "error"
		} else {
			m.notice, m.noticeKind = msg.notice, "ok"
		}
	default:
		return false
	}
	return true
}

// applyFleet shows a freshly loaded fleet, keeping the context filter.
func (m *tuiModel) applyFleet(f fleetSnapshot) {
	m.allItems, m.errors, m.refreshedAt = f.items, f.errors, time.Now()
	m.applyContextFilter()
}

// restyle redraws the rows already loaded in the current theme, without asking
// any backend for the fleet again.
func (m *tuiModel) restyle() {
	items := make([]list.Item, 0, len(m.allItems))
	for _, it := range m.allItems {
		switch v := it.(type) {
		case vmItem:
			items = append(items, vmToItem(v.vm, m.th))
		case ctItem:
			items = append(items, ctToItem(v.ct, m.th))
		default:
			items = append(items, it)
		}
	}
	m.allItems = items
	m.applyContextFilter()
}
