package cmd

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/tuna-os/corral/pkg/doctor"
)

// screen is one of the TUI's screens: how it handles a key and how it draws.
//
// The root model used to route both through a chain of `if m.state == "…"`
// blocks in Update and render. Each screen now owns its pair, and the table
// below is the only place a mode is wired to them, so adding a screen is one
// entry here instead of an edit to two long switches.
type screen struct {
	update func(tuiModel, tea.KeyPressMsg) (tuiModel, tea.Cmd)
	view   func(tuiModel) string
}

// screens maps every TUIMode to its screen. TestTUIScreens_EveryModeIsWired
// fails when a mode has no entry.
var screens = [modeCount]screen{
	ModeList:          {tuiModel.updateList, tuiModel.listView},
	ModeActions:       {tuiModel.updateActions, tuiModel.actionsView},
	ModeEdit:          {tuiModel.updateEdit, tuiModel.editView},
	ModeHWEdit:        {tuiModel.updateHWEdit, tuiModel.hwEditView},
	ModeConfirmDelete: {tuiModel.updateConfirmDelete, tuiModel.confirmDeleteView},
	ModeDoctor:        {tuiModel.updateDoctor, tuiModel.doctorView},
	ModeCloneInput:    {tuiModel.updateCloneInput, tuiModel.cloneInputView},
	ModeSnapshots:     {tuiModel.updateSnapshotsScreen, tuiModel.snapshotsView},
	ModeEvents:        {tuiModel.updateEvents, tuiModel.eventsView},
	ModePools:         {tuiModel.updatePoolsScreen, tuiModel.poolsView},
	ModePlugins:       {tuiModel.updatePlugins, tuiModel.pluginsView},
	ModeCreate:        {tuiModel.updateCreate, tuiModel.createView},
}

// quit ends the program from any screen.
func (m tuiModel) quit() (tuiModel, tea.Cmd) {
	m.quitting = true
	return m, tea.Quit
}

// ── the fleet list ────────────────────────────────────────────────

func (m tuiModel) updateList(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m.quit()
	case "c":
		m.create = newCreateModel()
		m.mode = ModeCreate
		return m, m.create.syncFocus()
	case "p":
		m.plugins = newPluginsModel()
		m.mode = ModePlugins
		return m, fetchPluginsCmd()
	case "d":
		m.doctorRows = fleetDiagnosis()
		m.mode = ModeDoctor
		return m, nil
	case "?":
		m.showHelp = true
		return m, nil
	case "r":
		m.refreshList()
		m.notice = "Fleet refreshed"
		return m, nil
	case "tab", "]":
		m.cycleContext(1)
		return m, nil
	case "shift+tab", "[":
		m.cycleContext(-1)
		return m, nil
	case "s":
		if item, ok := m.list.SelectedItem().(vmItem); ok && actionApplies("start", item.vm) {
			m.selected, m.isCT = item.vm, false
			m.performAction("start")
			m.refreshList()
		}
		return m, nil
	case "x":
		if item, ok := m.list.SelectedItem().(vmItem); ok && actionApplies("stop", item.vm) {
			m.selected, m.isCT = item.vm, false
			m.performAction("stop")
			m.refreshList()
		}
		return m, nil
	case "enter":
		// Anything that is not an instance row (an empty list, or the
		// filter prompt) falls through to the list itself.
		if m.openActionsForSelection() {
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// ── the actions menu ──────────────────────────────────────────────

func (m tuiModel) updateActions(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeList
		return m, nil
	case "q", "ctrl+c":
		return m.quit()
	case "enter":
		cmd := m.runSelectedAction()
		return m, cmd
	}
	var cmd tea.Cmd
	m.actionsList, cmd = m.actionsList.Update(msg)
	return m, cmd
}

func (m tuiModel) actionsView() string { return m.actionsList.View() }

// ── ports and hardware editors ────────────────────────────────────

func (m tuiModel) updateEdit(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	em, cmd := m.edit.Update(msg)
	m.edit = em.(editModel)
	if m.edit.done {
		m.mode = ModeList
		m.refreshList()
		return m, nil
	}
	return m, cmd
}

func (m tuiModel) editView() string { return m.edit.render() }

func (m tuiModel) updateHWEdit(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	hm, cmd := m.hwEdit.Update(msg)
	m.hwEdit = hm.(hwEditModel)
	if m.hwEdit.done {
		m.mode = ModeList
		m.refreshList()
		return m, nil
	}
	return m, cmd
}

func (m tuiModel) hwEditView() string { return m.hwEdit.render() }

// ── create and plugins ────────────────────────────────────────────

func (m tuiModel) updateCreate(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	cm, cmd, action := m.create.Update(msg)
	m.create = cm
	switch action {
	case crCancel:
		if msg.String() == "ctrl+c" {
			return m.quit()
		}
		m.mode = ModeList
		return m, nil
	case crSubmit:
		name := strings.TrimSpace(m.create.name.Value())
		m.busy, m.busyLabel = true, "Creating "+name
		if err := applyCreate(m.create); err != nil {
			m.busy = false
			m.create.err = err.Error()
			return m, nil
		}
		m.busy = false
		m.refreshList()
		m.mode = ModeList
		m.notice, m.noticeKind = "Created "+name, "ok"
		return m, nil
	}
	return m, cmd
}

func (m tuiModel) createView() string { return m.create.render(m.th, m.width) }

func (m tuiModel) updatePlugins(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	pm, cmd, back := m.plugins.Update(msg)
	m.plugins = pm
	if back && msg.String() == "ctrl+c" {
		return m.quit()
	}
	if back {
		m.mode = ModeList
		return m, nil
	}
	return m, cmd
}

func (m tuiModel) pluginsView() string { return m.plugins.render(m.th, m.width) }

// ── clone ─────────────────────────────────────────────────────────

func (m tuiModel) updateCloneInput(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeActions
		return m, nil
	case "ctrl+c":
		return m.quit()
	case "enter":
		dst := strings.TrimSpace(m.cloneInput.Value())
		if dst == "" {
			m.cloneErr = "name can't be empty"
			return m, nil
		}
		if err := runClone(m.selected, dst); err != nil {
			m.cloneErr = err.Error()
			return m, nil
		}
		m.refreshList()
		m.mode = ModeList
		return m, nil
	}
	var cmd tea.Cmd
	m.cloneInput, cmd = m.cloneInput.Update(msg)
	return m, cmd
}

func (m tuiModel) cloneInputView() string {
	var sb strings.Builder
	sb.WriteString(tuiTitle.Render(fmt.Sprintf(" Clone %s ", m.selected.Name)))
	sb.WriteString("\n\n  Target name: ")
	sb.WriteString(m.cloneInput.View())
	sb.WriteString("\n")
	if m.cloneErr != "" {
		sb.WriteString("\n  " + tuiStopped.Render(m.cloneErr) + "\n")
	}
	sb.WriteString("\n" + tuiHelp.Render("  enter clone · esc cancel"))
	return sb.String()
}

// ── delete confirmation ───────────────────────────────────────────

func (m tuiModel) updateConfirmDelete(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.performAction("delete")
		m.refreshList()
		m.mode = ModeList
	case "ctrl+c":
		return m.quit()
	default:
		m.mode = ModeActions
	}
	return m, nil
}

func (m tuiModel) confirmDeleteView() string {
	name, what := m.selected.Name, "and its disks"
	if m.isCT {
		name, what = m.selectedCT.Name, "and its data volume"
	}
	return fmt.Sprintf("\n  %s\n\n  %s\n",
		tuiTitle.Render(fmt.Sprintf(" Delete %s %s? ", name, what)),
		tuiHelp.Render("y confirm  any other key cancel"))
}

// ── pools, snapshots, events, doctor ──────────────────────────────

func (m tuiModel) updatePoolsScreen(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m.quit()
	}
	if !m.updatePools(msg) {
		if m.pools.notice != "" {
			m.notice, m.noticeKind = m.pools.notice, "ok"
		}
		m.refreshList()
		m.mode = ModeList
	}
	return m, nil
}

func (m tuiModel) updateSnapshotsScreen(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m.quit()
	}
	if !m.updateSnapshots(msg) {
		m.mode = ModeActions
		// A restore or a delete changes what the list shows.
		m.refreshList()
	}
	return m, nil
}

func (m tuiModel) updateEvents(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.quit()
	case "esc", "q", "enter":
		m.mode = ModeActions
	case "r":
		m.events = newEventsState(m.selected)
	}
	return m, nil
}

func (m tuiModel) updateDoctor(msg tea.KeyPressMsg) (tuiModel, tea.Cmd) {
	switch msg.String() {
	case "f":
		doctor.Fix()
		m.doctorRows = fleetDiagnosis()
	case "esc", "q", "enter":
		m.mode = ModeList
	case "ctrl+c":
		return m.quit()
	}
	return m, nil
}
