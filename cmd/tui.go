package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/doctor"
	"github.com/tuna-os/corral/pkg/fleet"
	"github.com/tuna-os/corral/pkg/incus"
	"github.com/tuna-os/corral/pkg/plugin"
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

type tuiModel struct {
	list        list.Model
	actionsList list.Model
	quitting    bool
	// mode is the screen on show; screens (cmd/tui_screens.go) routes keys
	// and drawing for it. Snapshots and events are the web UI's per-VM tabs as
	// screens of this model — see cmd/tui_views.go.
	mode        TUIMode
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

	fleet := loadFleet(th)
	items, errs := fleet.items, fleet.errors

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

	m := tuiModel{list: l, mode: ModeList, allItems: items, errors: errs,
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
	// Every operation the TUI started reports back here, whatever screen is up.
	if m.updateOp(msg) {
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		// The terminal answered. Rebuild every style for the real background,
		// and hand the new theme to the pieces that cached it.
		m.th = newTheme(msg.IsDark())
		m.list.Styles.Title = m.th.title
		m.list.SetDelegate(vmItemDelegate{th: m.th})
		m.spin.Style = lipgloss.NewStyle().Foreground(m.th.p.primary)
		m.actionsList = m.newActionsList()
		// The item descriptions carry colour, so they have to be redrawn too.
		m.restyle()
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
		// event that lands on nothing leaves the model untouched, and so does
		// one that arrives while an operation runs, like a key would.
		if m.busy {
			return m, nil
		}
		_, cmd := m.handleMouse(msg)
		return m, cmd

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
		// Keys typed while an operation runs would act on a fleet that is
		// about to change, so they are dropped; ctrl+c still quits.
		if m.busy {
			if msg.String() == "ctrl+c" {
				next, cmd := m.quit()
				return next, cmd
			}
			return m, nil
		}
		next, cmd := screens[m.mode].update(m, msg)
		return next, cmd
	}

	if m.mode == ModeList {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	return m, nil
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
					// A CT belongs to the contexts of the backend that runs it:
					// KubeVirt pet pods or Incus LXC containers.
					if value.ct.Backend == target.Backend {
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
	return screens[m.mode].view(m)
}

// listView is the fleet list screen: header, list, detail pane, status bar.
func (m tuiModel) listView() string {
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
	// The checks run in a command; until they land the table is empty.
	progress := ""
	if m.busy {
		progress = m.spin.View() + " " + m.busyLabel
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		m.th.title.Render(" Fleet health "),
		"",
		t.String(),
		m.th.statusBar(m.width, hints, progress, m.th.muted),
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
