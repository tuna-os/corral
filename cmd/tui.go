package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/doctor"
	"github.com/tuna-os/corral/pkg/fleet"
	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/plugin"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/tuna-os/corral/pkg/types"
)

// ── Styles ────────────────────────────────────────────────────────
var (
	tuiTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Padding(0, 1)
	tuiRunning  = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	tuiStopped  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	tuiProxyOn  = "●"
	tuiProxyOff = "○"
	tuiHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// postQuitAction is set by the TUI when an action needs to run
	// after the Bubble Tea program quits (e.g. SSH, Viewer).
	postQuitAction func()
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
	case "hardware", "ports", "clone", "export", "template", "events":
		return vm.Backend == "kubevirt"
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

type tuiModel struct {
	list        list.Model
	actionsList list.Model
	quitting    bool
	state       string // "list", "actions", "edit", "hwedit", "confirmDelete", "doctor", "cloneInput", "snapshots", "events", "pools"
	// snapshots and events are the web UI's per-VM tabs as states of this
	// model — see cmd/tui_views.go.
	snapshots   snapshotsState
	events      eventsState
	pools       poolsState
	selected    types.VM
	isCT        bool // true when the actions menu / performAction target is selectedCT, not selected
	selectedCT  ct.CT
	edit        editModel
	hwEdit      hwEditModel
	doctorRows  []doctor.Check
	cloneInput  textinput.Model
	cloneErr    string
	create      createModel
	plugins     pluginsModel
	width       int
	height      int
	allItems    []list.Item
	contextName string
	contextPos  int
	errors      map[string]string
	refreshedAt time.Time
	notice      string
	noticeKind  string // "", "ok", "warn", "error" — drives the notice colour
	showHelp    bool
	// lastClickRow / lastClickAt turn two clicks on the same row into a double
	// click — see cmd/tui_mouse.go.
	lastClickRow int
	lastClickAt  time.Time
	spin         spinner.Model
	busy         bool // an action is running; the header shows the spinner
	busyLabel    string
	// th is rebuilt when the terminal answers the background-colour query.
	// lipgloss v2 removed AdaptiveColor, so a style cannot resolve until the
	// background is known — see cmd/tui_theme.go.
	th theme
}

func newTUIModel() tuiModel {
	// A dark background is the starting assumption. The terminal is asked for
	// the real answer in Init, and BackgroundColorMsg rebuilds the theme — see
	// Update. lipgloss v2 has no AdaptiveColor to defer this for us.
	th := newTheme(true)

	items, errs := loadTUIItems(th)
	cts, _ := ct.ListCTs()
	for _, c := range cts {
		items = append(items, ctToItem(c, th))
	}

	l := list.New(items, vmItemDelegate{th: th}, 0, 0)
	l.SetSize(80, 36)
	l.Title = tuiListTitle(items)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.Styles.Title = th.title

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(th.p.primary)

	m := tuiModel{list: l, state: "list", allItems: items, errors: errs,
		refreshedAt: time.Now(), width: 80, height: 40, spin: sp, th: th}
	m.actionsList = m.newActionsList()
	return m
}

func (m *tuiModel) newActionsList() list.Model {
	title := "Actions"
	source := actionsListItems
	if m.isCT {
		if m.selectedCT.Name != "" {
			title = fmt.Sprintf("%s (ct)", m.selectedCT.Name)
		}
		source = actionsListItemsCT
	} else if m.selected.Name != "" {
		b := m.selected.Backend
		if b == "" {
			b = "qemu"
		}
		title = fmt.Sprintf("%s (%s)", m.selected.Name, b)
	}

	listItems := make([]list.Item, 0, len(source))
	for _, a := range source {
		if m.isCT && m.selectedCT.Name != "" && !ctActionApplies(a.id, m.selectedCT) {
			continue
		}
		if !m.isCT && m.selected.Name != "" && !actionApplies(a.id, m.selected) {
			continue
		}
		if a.id == "template" && !m.isCT {
			a.label = templateActionLabel(m.selected)
		}
		listItems = append(listItems, a)
	}

	// Height: 2 rows per item + title/padding, capped to the window so every
	// action (Delete included) is visible without paginating.
	h := len(listItems)*2 + 4
	if m.height > 2 && h > m.height-2 {
		h = m.height - 2
	}
	l := list.New(listItems, actionItemDelegate{th: m.th}, 30, h)
	l.Title = title
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowHelp(false)
	l.SetShowTitle(true)
	l.Styles.Title = tuiTitle
	l.KeyMap.Quit.Unbind()
	l.KeyMap.ForceQuit.Unbind()
	return l
}

// Init starts the spinner and asks the terminal what colour its background is.
//
// The second half is new in v2. lipgloss no longer resolves an adaptive colour
// behind your back, so the query is explicit: the answer arrives as a
// BackgroundColorMsg and Update rebuilds the theme from it. Until then the
// dark palette is assumed, which is right far more often than not.
func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, tea.RequestBackgroundColor)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		// The terminal answered. Rebuild every style for the real background,
		// and hand the new theme to the pieces that cached it.
		m.th = newTheme(msg.IsDark())
		m.list.Styles.Title = m.th.title
		m.list.SetDelegate(vmItemDelegate{th: m.th})
		m.spin.Style = lipgloss.NewStyle().Foreground(m.th.p.primary)
		m.actionsList = m.newActionsList()
		// The item descriptions carry colour, so they have to be rebuilt too.
		m.refreshList()
		return m, nil

	case spinner.TickMsg:
		// Keep ticking whatever the state: the header spinner has to keep
		// turning while a long action blocks the list.
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		listWidth := msg.Width
		if msg.Width >= 100 {
			listWidth = msg.Width * 2 / 3
		}
		// Two rows for the header, two for the status bar.
		m.list.SetSize(listWidth, msg.Height-5)
		m.actionsList.SetSize(msg.Width, msg.Height-2)
		return m, nil

	case tea.MouseClickMsg, tea.MouseWheelMsg:
		// A click or a wheel tick, mapped to a row by cmd/tui_mouse.go. An
		// event that lands on nothing leaves the model untouched.
		m.handleMouse(msg)
		return m, nil

	case pluginsLoadedMsg:
		m.plugins.loading = false
		if msg.err != nil {
			m.plugins.err = msg.err.Error()
		} else {
			m.plugins.entries = msg.entries
			if m.plugins.cursor >= len(msg.entries) {
				m.plugins.cursor = 0
			}
		}
		return m, nil

	case pluginActionMsg:
		m.plugins.busy = false
		if msg.err != nil {
			m.plugins.err = fmt.Sprintf("%s %s failed: %v", msg.verb, msg.name, msg.err)
			m.plugins.notice = ""
			return m, nil
		}
		// A plugin may add backends or commands, so refresh the installed set.
		m.plugins.installed = map[string]string{}
		for _, p := range plugin.Installed() {
			m.plugins.installed[p.Name] = p.Version
		}
		m.plugins.err, m.plugins.notice = "", fmt.Sprintf("%s %s", msg.verb, msg.name)
		return m, nil

	case tea.KeyPressMsg:
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		if m.state == "edit" {
			em, cmd := m.edit.Update(msg)
			m.edit = em.(editModel)
			if m.edit.done {
				m.state = "list"
				m.refreshList()
				return m, nil
			}
			return m, cmd
		}

		if m.state == "hwedit" {
			hm, cmd := m.hwEdit.Update(msg)
			m.hwEdit = hm.(hwEditModel)
			if m.hwEdit.done {
				m.state = "list"
				m.refreshList()
				return m, nil
			}
			return m, cmd
		}

		if m.state == "create" {
			cm, cmd, action := m.create.Update(msg)
			m.create = cm
			switch action {
			case crCancel:
				if msg.String() == "ctrl+c" {
					m.quitting = true
					return m, tea.Quit
				}
				m.state = "list"
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
				m.state = "list"
				m.notice, m.noticeKind = "Created "+name, "ok"
				return m, nil
			}
			return m, cmd
		}

		if m.state == "plugins" {
			pm, cmd, back := m.plugins.Update(msg)
			m.plugins = pm
			if back && msg.String() == "ctrl+c" {
				m.quitting = true
				return m, tea.Quit
			}
			if back {
				m.state = "list"
				return m, nil
			}
			return m, cmd
		}

		if m.state == "cloneInput" {
			switch msg.String() {
			case "esc":
				m.state = "actions"
				return m, nil
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
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
				m.state = "list"
				return m, nil
			}
			var cmd tea.Cmd
			m.cloneInput, cmd = m.cloneInput.Update(msg)
			return m, cmd
		}

		if m.state == "confirmDelete" {
			switch msg.String() {
			case "y", "Y":
				m.performAction("delete")
				m.refreshList()
				m.state = "list"
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			default:
				m.state = "actions"
			}
			return m, nil
		}

		if m.state == "pools" {
			if msg.String() == "ctrl+c" {
				m.quitting = true
				return m, tea.Quit
			}
			if !m.updatePools(msg) {
				if m.pools.notice != "" {
					m.notice, m.noticeKind = m.pools.notice, "ok"
				}
				m.refreshList()
				m.state = "list"
			}
			return m, nil
		}

		if m.state == "snapshots" {
			if msg.String() == "ctrl+c" {
				m.quitting = true
				return m, tea.Quit
			}
			if !m.updateSnapshots(msg) {
				m.state = "actions"
				// A restore or a delete changes what the list shows.
				m.refreshList()
			}
			return m, nil
		}

		if m.state == "events" {
			switch msg.String() {
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "esc", "q", "enter":
				m.state = "actions"
			case "r":
				m.events = newEventsState(m.selected)
			}
			return m, nil
		}

		if m.state == "actions" {
			switch msg.String() {
			case "esc":
				m.state = "list"
				return m, nil
			case "q", "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "enter":
				return m, m.runSelectedAction()
			}
			var cmd tea.Cmd
			m.actionsList, cmd = m.actionsList.Update(msg)
			return m, cmd
		}

		if m.state == "doctor" {
			switch msg.String() {
			case "f":
				doctor.Fix()
				m.doctorRows = fleetDiagnosis()
			case "esc", "q", "enter":
				m.state = "list"
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}

		// VM list state
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "c":
			m.create = newCreateModel()
			m.state = "create"
			return m, m.create.syncFocus()
		case "p":
			m.plugins = newPluginsModel()
			m.state = "plugins"
			return m, fetchPluginsCmd()
		case "d":
			m.doctorRows = fleetDiagnosis()
			m.state = "doctor"
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
	}

	if m.state == "list" {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	return m, nil
}

// runSelectedAction runs whatever the actions menu has highlighted. Enter and a
// double click both come through here, so a mouse can never reach an action a
// keyboard cannot — including the destructive ones, which still land on their
// confirmation rather than firing.
func (m *tuiModel) runSelectedAction() tea.Cmd {
	item, ok := m.actionsList.SelectedItem().(actionItem)
	if !ok {
		return nil
	}
	switch item.id {
	case "ports":
		m.state = "edit"
		m.edit = newEditModel(m.selected)
		return m.edit.Init()
	case "hardware":
		m.state = "hwedit"
		if m.isCT {
			m.hwEdit = newCTHWEditModel(m.selectedCT)
		} else {
			m.hwEdit = newHWEditModel(m.selected)
		}
		return m.hwEdit.Init()
	case "clone":
		m.state = "cloneInput"
		m.cloneErr = ""
		m.cloneInput = newCloneInput(m.selected.Name)
		return m.cloneInput.Focus()
	case "snapshot":
		// Parity with the web UI's Snapshots tab: the action opens the captures
		// the VM already has rather than silently taking one more.
		m.snapshots = newSnapshotsState(m.selected)
		m.state = "snapshots"
	case "events":
		m.events = newEventsState(m.selected)
		m.state = "events"
	case "pool":
		m.pools = newPoolAssignState(m.selected)
		m.state = "pools"
	case "move":
		// Deliberately not "migrate": that verb is the live, same-backend one,
		// and the two must not be confusable in a menu (ADR-0010).
		m.pools = newMoveDestinationState(m.selected)
		m.state = "pools"
	case "template":
		m.toggleTemplate()
		m.refreshList()
		m.state = "list"
	case "start", "stop", "restart", "pause", "unpause", "migrate":
		m.performAction(item.id)
		m.refreshList()
		m.state = "list"
	case "ssh", "viewer", "export":
		actionID := item.id
		postQuitAction = func() { m.performAction(actionID) }
		m.quitting = true
		return tea.Quit
	case "console":
		name, ns := m.selectedCT.Name, m.selectedCT.Namespace
		postQuitAction = func() { ct.Console(name, ns) }
		m.quitting = true
		return tea.Quit
	case "delete":
		m.state = "confirmDelete"
	}
	return nil
}

func (m *tuiModel) performAction(action string) {
	if m.isCT {
		m.performCTAction(action)
		return
	}
	name := m.selected.Name
	backend := m.selected.Backend
	contextName := m.selected.Context
	ns := m.selected.Namespace
	if ns == "" {
		ns = "default"
	}

	// Power, pause, and migrate go through the operation contract
	// (pkg/backend): one adapter per backend, asserted for the family the
	// action needs. This replaced a per-backend if/else ladder per action —
	// the shape that made every backend but KubeVirt best-effort, because
	// adding an action meant remembering to add four more branches.
	//
	// The refusals matter as much as the calls: an action a backend cannot
	// perform now reports why, where the ladder silently did nothing.
	switch action {
	case "start", "stop", "restart", "delete", "pause", "unpause", "migrate":
		m.performBackendAction(action)
		if action == "delete" && registryStore != nil {
			registryStore.RemoveRef(m.selected.Ref())
		}
		return
	case "export":
		if backend == "kubevirt" {
			out, err := kubevirt.NewClientForContext(ns, contextName).Export(name, "", "")
			if err != nil {
				fmt.Fprintln(os.Stderr, "export failed:", err)
			} else {
				fmt.Println("Exported to", out)
			}
		}
	case "viewer":
		if err := viewSelected(m.selected.Ref()); err != nil {
			fmt.Fprintln(os.Stderr, "viewer failed:", err)
		}
	case "ssh":
		user, password := "", ""
		if registryStore != nil {
			if entry, ok := registryStore.GetRef(m.selected.Ref()); ok {
				user, password = entry.Username, entry.Password
			}
		}
		if user == "" {
			user = os.Getenv("USER")
		}
		if user == "" {
			user = "root"
		}
		if backend == "kubevirt" {
			kubevirt.NewClientForContext(ns, contextName).SSH(name, user, "", "", 22, password, nil)
		} else if backend == "incus" {
			incus.NewClient(contextName).SSH(name, "")
		} else {
			qemu.SSH(name, user, "", "", 22, password, nil)
		}
	}
}

// viewSelected opens the graphical console of the instance through its
// backend's Viewer, the same path `corral viewer` takes.
func viewSelected(ref types.InstanceRef) error {
	adapter, err := backend.For(ref)
	if err != nil {
		return err
	}
	viewer, ok := adapter.(backend.Viewer)
	if !ok {
		return unsupported(adapter, "viewer")
	}
	return viewer.View(ref.Name)
}

// performCTAction is performAction's CT counterpart — a much smaller
// surface (no backend split, no migrate/snapshot/pause) since a CT is
// always a plain pod, never a hypervisor guest. "console" isn't handled
// here — it's a quit-then-exec action (see the "actions" state's enter
// handler), same as ssh/viewer/export, since it needs the real terminal
// back after Bubble Tea releases it.
func (m *tuiModel) performCTAction(action string) {
	name, ns := m.selectedCT.Name, m.selectedCT.Namespace

	switch action {
	case "start":
		ct.Start(name, ns)
	case "stop":
		ct.Stop(name, ns)
	case "delete":
		ct.Delete(name, ns)
	}
}

// newCloneInput sets up the target-name text input for the Clone action,
// pre-filled with a "-clone" suggestion off the source VM's name.
func newCloneInput(sourceName string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "target VM name"
	ti.SetValue(sourceName + "-clone")
	ti.CharLimit = 63
	ti.SetWidth(30)
	return ti
}

// runClone mirrors cmd/clone.go's logic (kubevirt-only, registers the clone
// in the local registry) so the TUI's Clone action behaves identically to
// `corral clone` — same checks, same errors, just driven by a text input
// instead of positional args.
func runClone(src types.VM, dst string) error {
	if src.Backend != "kubevirt" {
		return fmt.Errorf("cloning is only supported on KubeVirt VMs (VM %q uses backend %q)", src.Name, src.Backend)
	}
	if existing := resolveBackend(dst); existing != "" {
		return fmt.Errorf("target VM %q already exists (backend: %s)", dst, existing)
	}
	ns := src.Namespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	if err := kubevirt.NewClientForContext(ns, src.Context).Clone(src.Name, dst); err != nil {
		return err
	}
	if registryStore != nil {
		ref := types.InstanceRef{Backend: "kubevirt", Context: src.Context, Namespace: ns, Name: dst}
		if err := registryStore.SetRef(ref, types.RegistryEntry{Backend: "kubevirt", Context: src.Context, Namespace: ns}); err != nil {
			return fmt.Errorf("saving registry entry: %w", err)
		}
	}
	return nil
}

func (m *tuiModel) refreshList() {
	items, errs := loadTUIItems(m.th)
	cts, _ := ct.ListCTs()
	for _, c := range cts {
		items = append(items, ctToItem(c, m.th))
	}
	m.allItems, m.errors, m.refreshedAt = items, errs, time.Now()
	m.applyContextFilter()
}

func loadTUIItems(th theme) ([]list.Item, map[string]string) {
	result := fleet.List(context.Background())
	items := make([]list.Item, 0, len(result.VMs))
	seen := map[string]bool{}
	for _, vm := range result.VMs {
		items = append(items, vmToItem(vm, th))
		seen[vm.ID] = true
	}
	// A local Incus daemon with instances is useful zero-config discovery and
	// keeps demo mode representative; configured remotes are already in fleet.
	if discovered, err := incus.List(); err == nil {
		for _, vm := range discovered {
			if !seen[vm.ID] {
				items = append(items, vmToItem(vm, th))
			}
		}
	}
	return items, result.Errors
}

func (m *tuiModel) cycleContext(delta int) {
	targets := []string{"all"}
	for _, target := range config.Contexts() {
		targets = append(targets, target.Name)
	}
	m.contextPos = (m.contextPos + delta + len(targets)) % len(targets)
	m.contextName = targets[m.contextPos]
	m.applyContextFilter()
}

func (m *tuiModel) applyContextFilter() {
	items := m.allItems
	if m.contextName != "" && m.contextName != "all" {
		if target, ok := config.FindContext(m.contextName); ok {
			items = nil
			for _, item := range m.allItems {
				switch value := item.(type) {
				case vmItem:
					if value.vm.Backend == target.Backend && value.vm.Context == target.Context {
						items = append(items, item)
					}
				case ctItem:
					if target.Backend == "kubevirt" {
						items = append(items, item)
					}
				}
			}
		}
	}
	m.list.SetItems(items)
	m.list.Title = tuiListTitle(items) + "  ·  context:" + defaultString(m.contextName, "all")
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// tuiListTitle summarizes the fleet in the header: "Corral · 8 VMs · 2 CTs".
func tuiListTitle(items []list.Item) string {
	nVM, nCT := 0, 0
	for _, it := range items {
		if _, ok := it.(ctItem); ok {
			nCT++
		} else {
			nVM++
		}
	}
	t := fmt.Sprintf("%d VMs", nVM)
	if nCT > 0 {
		t += fmt.Sprintf("  ·  %d CTs", nCT)
	}
	return t
}

func (m tuiModel) render() string {
	if m.quitting {
		return ""
	}
	if m.showHelp {
		return tuiHelpView()
	}

	if m.state == "edit" {
		return m.edit.render()
	}

	if m.state == "hwedit" {
		return m.hwEdit.render()
	}

	if m.state == "create" {
		return m.create.render(m.th, m.width)
	}

	if m.state == "plugins" {
		return m.plugins.render(m.th, m.width)
	}

	if m.state == "cloneInput" {
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

	if m.state == "doctor" {
		return m.doctorView()
	}

	if m.state == "pools" {
		return m.poolsView()
	}

	if m.state == "snapshots" {
		return m.snapshotsView()
	}

	if m.state == "events" {
		return m.eventsView()
	}

	if m.state == "confirmDelete" {
		name, what := m.selected.Name, "and its disks"
		if m.isCT {
			name, what = m.selectedCT.Name, "and its data volume"
		}
		return fmt.Sprintf("\n  %s\n\n  %s\n",
			tuiTitle.Render(fmt.Sprintf(" Delete %s %s? ", name, what)),
			tuiHelp.Render("y confirm  any other key cancel"))
	}

	if m.state == "actions" {
		return m.actionsList.View()
	}

	// Header: brand, selected context, live counts. Always on screen, so
	// "where am I and how much is here" never needs a keystroke.
	rows := make([]listRow, 0, len(m.list.Items()))
	for _, it := range m.list.Items() {
		switch v := it.(type) {
		case vmItem:
			rows = append(rows, listRow{state: v.state})
		case ctItem:
			rows = append(rows, listRow{state: v.state})
		}
	}
	top := m.th.headerBar(m.width, m.contextName, countFleet(rows), m.busy,
		m.spin.View()+" "+m.th.muted.Render(m.busyLabel))

	listView := m.list.View()
	if m.width >= 100 {
		detailWidth := m.width - m.width*2/3 - 3
		listView = lipgloss.JoinHorizontal(lipgloss.Top, listView,
			tuiDetailView(m.list.SelectedItem(), detailWidth, m.errors, m.th))
	}

	// The bindings that work in this state, not every binding there is.
	hints := []keyHint{
		{"/", "search"}, {"tab", "context"}, {"enter", "actions"},
		{"c", "create"}, {"s", "start"}, {"x", "stop"}, {"r", "refresh"},
		{"p", "extensions"}, {"d", "doctor"}, {"?", "help"}, {"q", "quit"},
	}
	notice, noticeStyle := m.notice, m.th.muted
	switch m.noticeKind {
	case "ok":
		noticeStyle = m.th.ok
	case "warn":
		noticeStyle = m.th.warn
	case "error":
		noticeStyle = m.th.danger
	}
	if notice == "" && len(m.errors) > 0 {
		notice = fmt.Sprintf("⚠ %d context(s) unavailable — press d for details", len(m.errors))
		noticeStyle = m.th.warn
	}
	if notice == "" && !hasWorkingBackend() {
		notice, noticeStyle = "No working backend — run `corral setup` to configure one", m.th.warn
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		top,
		listView,
		m.th.statusBar(m.width, hints, notice, noticeStyle),
	)
}

// doctorView renders the fleet health check as a real table.
//
// It was hand-aligned with %-30s, which breaks the moment a check name is
// longer than the pad or a detail wraps — and both happen, because the peer
// checks added in #135 have long remedies. lipgloss/table measures the
// content and sizes the columns itself.
func (m tuiModel) doctorView() string {
	fixable := false

	t := table.New().
		Width(tableWidth(m.width)).
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(m.th.p.border)).
		Headers("", "TARGET", "CHECK", "DETAIL").
		StyleFunc(func(row, col int) lipgloss.Style {
			base := lipgloss.NewStyle().Padding(0, 1)
			if row == table.HeaderRow {
				return base.Bold(true).Foreground(m.th.p.muted)
			}
			switch col {
			case 1:
				return base.Foreground(m.th.p.accent)
			case 3:
				return base.Foreground(m.th.p.muted).MaxWidth(maxDetail(m.width))
			default:
				return base
			}
		})

	for _, c := range m.doctorRows {
		mark := m.th.ok.Render("●")
		switch {
		case c.OK:
		case c.Severity == "warning":
			mark = m.th.warn.Render("!")
		default:
			mark = m.th.danger.Render("✖")
		}
		detail := c.Detail
		if !c.OK && c.Fixable {
			detail += m.th.warn.Render("  (fixable)")
			fixable = true
		}
		t.Row(mark, c.Context+" ["+c.Backend+"]", c.Name, detail)
	}

	hints := []keyHint{{"esc", "back"}}
	if fixable {
		hints = append([]keyHint{{"f", "reconcile fixable"}}, hints...)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		m.th.title.Render(" Fleet health "),
		"",
		t.String(),
		m.th.statusBar(m.width, hints, "", m.th.muted),
	)
}

// maxDetail keeps the detail column from pushing the table off screen. The
// remedies the doctor prints are sentences, not labels.
func maxDetail(width int) int {
	limit := width - 48
	if limit < 24 {
		return 24
	}
	if limit > 90 {
		return 90
	}
	return limit
}

// tableWidth fits the table to the terminal. Without it lipgloss sizes the
// columns from the content, and the doctor's remedies are long enough to run
// off the right edge — which is where they were before this view was a table.
func tableWidth(width int) int {
	if width < 60 {
		return 60
	}
	return width - 2
}

func tuiHelpView() string {
	return tuiTitle.Render(" Corral command deck ") + "\n\n" +
		"  ↑/↓ or j/k   move through the fleet\n" +
		"  /            fuzzy search name, ID, backend, context, node, or IP\n" +
		"  tab / [ ]    cycle all and individual contexts\n" +
		"  enter        capability-aware actions\n" +
		"               (power, migrate, clone, snapshots, events, template,\n" +
		"                hardware, ports, export, ssh, viewer, delete)\n" +
		"  c            create a VM\n" +
		"  p            extensions (marketplace)\n" +
		"  s / x        quick start / graceful stop\n" +
		"  r            refresh every backend\n" +
		"  d            doctor: backend diagnostics\n" +
		"  q            quit\n" +
		"  mouse        click to select, double-click to open, wheel to scroll\n\n" + tuiHelp.Render("  press any key to return")
}

func tuiDetailView(item list.Item, width int, errs map[string]string, th theme) string {
	style := lipgloss.NewStyle().Width(width).Padding(1).BorderLeft(true).
		BorderStyle(lipgloss.RoundedBorder()).BorderForeground(th.p.border)
	label := th.label
	vmItem, ok := item.(vmItem)
	if !ok {
		if ctItem, yes := item.(ctItem); yes {
			return style.Render(fmt.Sprintf("%s\n\n%s\n%s %s\n\n%s\n%s\n\n%s\n%d CPU · %s",
				label.Render("CONTAINER"),
				th.nameSelected.Render(ctItem.ct.Name),
				th.statusDot(ctItem.state), th.muted.Render(statusWords(ctItem.ct.Phase)),
				label.Render("BACKEND"), th.backendChip(ctItem.ct.Backend),
				label.Render("RESOURCES"), ctItem.ct.CPU, ctItem.ct.Mem))
		}
		if len(errs) > 0 {
			var lines []string
			for target, message := range errs {
				lines = append(lines, "⚠ "+target+"\n  "+message)
			}
			return style.Render(th.warn.Render("PARTIAL FLEET") + "\n\n" + strings.Join(lines, "\n\n"))
		}
		return style.Render("No instances in this view.\n\nPress tab to change context or / to clear search.")
	}
	vm := vmItem.vm
	body := fmt.Sprintf("%s\n%s\n\n%s %s\n\n%s\n%s\n\n%s\n%s\n\n%s\n%s / %s\n\n%s\n%d CPU · %s\n\n%s\n%s\n\n%s\n%s",
		th.nameSelected.Render(vm.Name),
		th.help.Render(vm.ID),
		th.statusDot(vmItem.state), th.muted.Render(statusWords(vm.Status)),
		label.Render("BACKEND"), th.backendChip(vm.Backend),
		label.Render("CONTEXT"), defaultString(vm.Context, "local"),
		label.Render("LOCATION"), defaultString(vm.Namespace, "—"), defaultString(vm.Node, "—"),
		label.Render("RESOURCES"), vm.CPU, defaultString(vm.Mem, "—"),
		label.Render("NETWORK"), defaultString(vm.IP, th.muted.Render("not discovered")),
		label.Render("CAN DO"), th.capabilityChips(vm.Capabilities))
	return style.Render(body)
}

// ── Proxy helpers ─────────────────────────────────────────────────

func exposedPorts(name, ns string) []int {
	return kubevirt.ExposedPorts(name, ns)
}

// ── Bubble Tea v2 View ────────────────────────────────────────────
//
// v2's View returns a struct rather than a string: terminal features are
// declared on it instead of being toggled with commands at startup. That is
// why the alt screen is set here and not passed to tea.NewProgram.
//
// The rendering itself stays in render(), a plain string method, so the view
// layer is still testable without building a tea.View.

func (m tuiModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	// Cell-motion tracking: enough for clicks and the wheel, without asking the
	// terminal to report every idle movement.
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// fleetDiagnosis is what the TUI's doctor view shows: every compute context
// plus every configured peer, matching the fleet the list view aggregates.
func fleetDiagnosis() []doctor.Check {
	return append(doctor.RunContexts(config.Contexts()), doctor.RunPeers(config.Peers())...)
}

// hasWorkingBackend is the first-run gate: anything beyond the implicit
// local context, or any sign this host can reach a backend (qemu/incus
// binaries, a kubeconfig), counts as working. No probing, no dialing —
// just presence, enough to decide whether the setup nudge earns its line.
func hasWorkingBackend() bool {
	if len(config.Contexts()) > 1 {
		return true
	}
	for _, bin := range []string{"qemu-system-x86_64", "incus", "virsh"} {
		if _, err := exec.LookPath(bin); err == nil {
			return true
		}
	}
	return config.KubeconfigPresent()
}
