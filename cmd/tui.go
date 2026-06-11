package cmd

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/qemu"
	"github.com/hanthor/corral/pkg/types"
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
}

func (i vmItem) Title() string       { return i.vm.Name }
func (i vmItem) Description() string { return i.display }
func (i vmItem) FilterValue() string { return i.vm.Name }

func vmToItem(vm types.VM) vmItem {
	proxy := tuiProxyOff
	if vm.VNC == "on" {
		proxy = tuiProxyOn
	} else if vm.VNC == "pending" {
		proxy = "◐"
	}
	return vmItem{
		vm: vm,
		display: fmt.Sprintf("%s  %s  ports:%s  %d CPU / %s",
			vm.Status, vm.Backend, proxy, vm.CPU, vm.Mem),
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
	{id: "migrate", label: "Migrate"},
	{id: "hardware", label: "Edit CPU / RAM"},
	{id: "snapshot", label: "Snapshot"},
	{id: "export", label: "Export (backup disk)"},
	{id: "ssh", label: "SSH"},
	{id: "viewer", label: "Viewer (VNC)"},
	{id: "ports", label: "Edit ports"},
	{id: "delete", label: "Delete"},
}

// ── Main model ────────────────────────────────────────────────────

type tuiModel struct {
	list        list.Model
	actionsList list.Model
	quitting    bool
	state       string // "list", "actions", "edit", "hwedit", "confirmDelete"
	selected    types.VM
	edit        editModel
	hwEdit      hwEditModel
	width       int
	height      int
}

func newTUIModel() tuiModel {
	items := []list.Item{}
	vms, _ := kubevirt.NewClient("").ListVMs()
	for _, vm := range vms {
		items = append(items, vmToItem(vm))
	}
	qVMs, _ := qemu.List()
	for _, vm := range qVMs {
		items = append(items, vmToItem(vm))
	}

	if len(items) == 0 {
		fmt.Println("No VMs found. Create one: corral create <name>")
		os.Exit(0)
	}

	l := list.New(items, vmItemDelegate{}, 0, 0)
	l.Title = "Corral"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.Styles.Title = tuiTitle

	m := tuiModel{list: l, state: "list"}
	m.actionsList = m.newActionsList()
	return m
}

func (m *tuiModel) newActionsList() list.Model {
	title := "Actions"
	if m.selected.Name != "" {
		b := m.selected.Backend
		if b == "" {
			b = "qemu"
		}
		title = fmt.Sprintf("%s (%s)", m.selected.Name, b)
	}

	listItems := make([]list.Item, len(actionsListItems))
	for i, a := range actionsListItems {
		listItems[i] = a
	}

	l := list.New(listItems, actionItemDelegate{}, 30, len(listItems)*2+2)
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

func (m tuiModel) Init() tea.Cmd { return nil }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width, msg.Height-2)
		return m, nil

	case tea.KeyMsg:
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

		if m.state == "actions" {
			switch msg.String() {
			case "esc":
				m.state = "list"
				return m, nil
			case "q", "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "enter":
				if item, ok := m.actionsList.SelectedItem().(actionItem); ok {
					switch item.id {
					case "ports":
						m.state = "edit"
						m.edit = newEditModel(m.selected)
						return m, m.edit.Init()
					case "hardware":
						m.state = "hwedit"
						m.hwEdit = newHWEditModel(m.selected)
						return m, m.hwEdit.Init()
					case "start", "stop", "restart", "pause", "unpause", "migrate", "snapshot":
						m.performAction(item.id)
						m.refreshList()
						m.state = "list"
						return m, nil
					case "ssh", "viewer", "export":
						actionID := item.id
						postQuitAction = func() { m.performAction(actionID) }
						m.quitting = true
						return m, tea.Quit
					case "delete":
						m.state = "confirmDelete"
						return m, nil
					}
				}
			}
			var cmd tea.Cmd
			m.actionsList, cmd = m.actionsList.Update(msg)
			return m, cmd
		}

		// VM list state
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			if item, ok := m.list.SelectedItem().(vmItem); ok {
				m.selected = item.vm
				m.actionsList = m.newActionsList()
				m.state = "actions"
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

func (m *tuiModel) performAction(action string) {
	name := m.selected.Name
	backend := m.selected.Backend
	ns := m.selected.Namespace
	if ns == "" {
		ns = "default"
	}

	switch action {
	case "start":
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).StartVM(name)
		} else {
			qemu.Start(name)
		}
	case "stop":
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).StopVM(name)
		} else {
			qemu.Stop(name)
		}
	case "restart":
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).RestartVM(name)
		} else {
			qemu.Stop(name)
			qemu.Start(name)
		}
	case "pause":
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).PauseVM(name)
		}
	case "unpause":
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).UnpauseVM(name)
		}
	case "migrate":
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).Migrate(name, "")
		}
	case "snapshot":
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).Snapshot(name, "")
		}
	case "export":
		if backend == "kubevirt" {
			out, err := kubevirt.NewClient(ns).Export(name, "", "")
			if err != nil {
				fmt.Fprintln(os.Stderr, "export failed:", err)
			} else {
				fmt.Println("Exported to", out)
			}
		}
	case "delete":
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).DeleteVM(name)
		} else {
			qemu.Delete(name)
		}
		if registryStore != nil {
			registryStore.Remove(name)
		}
	case "viewer":
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).Viewer(name)
		} else {
			qemu.Viewer(name)
		}
	case "ssh":
		user := os.Getenv("USER")
		if user == "" {
			user = "root"
		}
		password := ""
		if registryStore != nil {
			if entry, ok := registryStore.Get(name); ok {
				password = entry.Password
			}
		}
		if backend == "kubevirt" {
			kubevirt.NewClient(ns).SSH(name, user, "", "", 22, password)
		} else {
			qemu.SSH(name, user, "", "", 22, password)
		}
	}
}

func (m *tuiModel) refreshList() {
	items := []list.Item{}
	vms, _ := kubevirt.NewClient("").ListVMs()
	for _, vm := range vms {
		items = append(items, vmToItem(vm))
	}
	qVMs, _ := qemu.List()
	for _, vm := range qVMs {
		items = append(items, vmToItem(vm))
	}
	m.list.SetItems(items)
}

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}

	if m.state == "edit" {
		return m.edit.View()
	}

	if m.state == "hwedit" {
		return m.hwEdit.View()
	}

	if m.state == "confirmDelete" {
		return fmt.Sprintf("\n  %s\n\n  %s\n",
			tuiTitle.Render(fmt.Sprintf(" Delete %s and its disks? ", m.selected.Name)),
			tuiHelp.Render("y confirm  any other key cancel"))
	}

	if m.state == "actions" {
		return m.actionsList.View()
	}

	return m.list.View()
}

// ── Actions list delegate ─────────────────────────────────────────

type actionItemDelegate struct{}

func (d actionItemDelegate) Height() int                               { return 1 }
func (d actionItemDelegate) Spacing() int                              { return 1 }
func (d actionItemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d actionItemDelegate) Render(w io.Writer, m list.Model, index int, li list.Item) {
	a, ok := li.(actionItem)
	if !ok {
		return
	}

	label := a.label
	if index == m.Index() {
		label = tuiRunning.Render("▶ " + label)
	} else {
		label = "  " + label
	}
	fmt.Fprint(w, label)
}

// ── Edit model (port toggles) ─────────────────────────────────────

type editModel struct {
	vm       types.VM
	ports    []int
	toggled  map[int]bool
	cursor   int
	done     bool
	addInput textinput.Model
	adding   bool
}

func newEditModel(vm types.VM) editModel {
	current := exposedPorts(vm.Name, vm.Namespace)
	toggled := make(map[int]bool)
	for _, p := range current {
		toggled[p] = true
	}

	allPorts := append([]int{}, types.DefaultPorts...)
	for _, p := range current {
		found := false
		for _, dp := range types.DefaultPorts {
			if p == dp {
				found = true
				break
			}
		}
		if !found {
			allPorts = append(allPorts, p)
		}
	}

	ti := textinput.New()
	ti.Placeholder = "port number"
	ti.CharLimit = 5

	return editModel{
		vm:       vm,
		ports:    allPorts,
		toggled:  toggled,
		addInput: ti,
	}
}

func (m editModel) Init() tea.Cmd { return nil }

func (m editModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.adding {
		return m.updateAdding(msg)
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			m.done = true
		case "q", "ctrl+c":
			m.done = true
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.ports) {
				m.cursor++
			}
		case " ", "enter":
			if m.cursor < len(m.ports) {
				p := m.ports[m.cursor]
				m.toggled[p] = !m.toggled[p]
				m.applyPorts()
			} else if m.cursor == len(m.ports) {
				m.adding = true
				m.addInput.Focus()
				return m, textinput.Blink
			} else if m.cursor == len(m.ports)+1 {
				m.toggled = make(map[int]bool)
				m.applyPorts()
			}
		case "backspace":
			if m.cursor < len(m.ports) {
				p := m.ports[m.cursor]
				isDefault := false
				for _, dp := range types.DefaultPorts {
					if p == dp {
						isDefault = true
						break
					}
				}
				if !isDefault && m.toggled[p] {
					delete(m.toggled, p)
					m.applyPorts()
				}
			}
		}
	}
	return m, nil
}

func (m editModel) updateAdding(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if port, err := strconv.Atoi(m.addInput.Value()); err == nil && port > 0 && port < 65536 {
				m.ports = append(m.ports, port)
				m.toggled[port] = true
				m.applyPorts()
			}
			m.adding = false
			m.addInput.Reset()
		case "esc":
			m.adding = false
			m.addInput.Reset()
		}
	}
	var cmd tea.Cmd
	m.addInput, cmd = m.addInput.Update(msg)
	return m, cmd
}

func (m *editModel) applyPorts() {
	var enabled []int
	for p, on := range m.toggled {
		if on {
			enabled = append(enabled, p)
		}
	}
	if len(enabled) == 0 {
		kubevirt.DeleteProxy(m.vm.Name, m.vm.Namespace)
	} else {
		kubevirt.ApplyProxy(m.vm.Name, m.vm.Namespace, enabled)
	}
}

func (m editModel) View() string {
	var sb strings.Builder
	host := m.vm.Name + "-vm.manatee-basking.ts.net"
	sb.WriteString(tuiTitle.Render(fmt.Sprintf(" Ports: %s ", host)))
	sb.WriteString("\n\n")

	for i, p := range m.ports {
		cursor := "  "
		if i == m.cursor {
			cursor = tuiRunning.Render("▶ ")
		}
		mark := "[OFF]"
		if m.toggled[p] {
			mark = tuiRunning.Render("[ON]")
		}
		label := fmt.Sprintf("port %d", p)
		for proto, port := range types.PortMap {
			if port == p {
				label = fmt.Sprintf("%s (%d)", proto, p)
				break
			}
		}
		sb.WriteString(fmt.Sprintf("%s%-20s  %s\n", cursor, label, mark))
	}

	cursor := "  "
	if m.cursor == len(m.ports) {
		cursor = tuiRunning.Render("▶ ")
	}
	sb.WriteString(fmt.Sprintf("%s+ Add port...\n", cursor))

	cursor = "  "
	if m.cursor == len(m.ports)+1 {
		cursor = tuiRunning.Render("▶ ")
	}
	if len(m.toggled) > 0 {
		sb.WriteString(fmt.Sprintf("%s✕ Remove all ports\n", cursor))
	}

	if m.adding {
		sb.WriteString(fmt.Sprintf("\n  Port: %s", m.addInput.View()))
	}

	sb.WriteString("\n" + tuiHelp.Render("  space toggle  ↑↓ nav  backspace remove  esc back"))
	return sb.String()
}

// ── Hardware edit (CPU / RAM) ─────────────────────────────────────

type hwEditModel struct {
	vm     types.VM
	cpu    textinput.Model
	mem    textinput.Model
	focus  int // 0 = cpu, 1 = mem
	status string
	done   bool
}

func newHWEditModel(vm types.VM) hwEditModel {
	cpu := textinput.New()
	cpu.SetValue(strconv.Itoa(vm.CPU))
	cpu.CharLimit = 3
	cpu.Width = 6
	cpu.Focus()

	mem := textinput.New()
	mem.SetValue(vm.Mem)
	mem.CharLimit = 8
	mem.Width = 8

	return hwEditModel{vm: vm, cpu: cpu, mem: mem}
}

func (m hwEditModel) Init() tea.Cmd { return textinput.Blink }

func (m hwEditModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			m.done = true
			return m, nil
		case "tab", "up", "down", "shift+tab":
			m.focus = (m.focus + 1) % 2
			if m.focus == 0 {
				m.cpu.Focus()
				m.mem.Blur()
			} else {
				m.mem.Focus()
				m.cpu.Blur()
			}
			return m, textinput.Blink
		case "enter":
			m.apply()
			m.done = true
			return m, nil
		}
	}
	var cmd tea.Cmd
	if m.focus == 0 {
		m.cpu, cmd = m.cpu.Update(msg)
	} else {
		m.mem, cmd = m.mem.Update(msg)
	}
	return m, cmd
}

func (m *hwEditModel) apply() {
	ns := m.vm.Namespace
	if ns == "" {
		ns = "default"
	}
	c := kubevirt.NewClient(ns)
	if v, err := strconv.Atoi(strings.TrimSpace(m.cpu.Value())); err == nil && v > 0 && v != m.vm.CPU {
		c.ScaleCPU(m.vm.Name, v)
	}
	if mem := strings.TrimSpace(m.mem.Value()); mem != "" && mem != m.vm.Mem {
		c.ScaleMemory(m.vm.Name, mem)
	}
}

func (m hwEditModel) View() string {
	var sb strings.Builder
	sb.WriteString(tuiTitle.Render(fmt.Sprintf(" Edit hardware: %s ", m.vm.Name)))
	sb.WriteString("\n\n")
	cpuMark, memMark := "  ", "  "
	if m.focus == 0 {
		cpuMark = tuiRunning.Render("▶ ")
	} else {
		memMark = tuiRunning.Render("▶ ")
	}
	sb.WriteString(fmt.Sprintf("%svCPUs   %s\n", cpuMark, m.cpu.View()))
	sb.WriteString(fmt.Sprintf("%sMemory  %s\n", memMark, m.mem.View()))
	note := "applies live (hotplug)"
	if !m.vm.LiveMigratable {
		note = "VM will restart to apply"
	}
	sb.WriteString("\n" + tuiHelp.Render("  "+note))
	sb.WriteString("\n" + tuiHelp.Render("  tab switch  enter apply  esc cancel"))
	return sb.String()
}

// ── Proxy helpers ─────────────────────────────────────────────────

func exposedPorts(name, ns string) []int {
	return kubevirt.ExposedPorts(name, ns)
}

// ── List delegates ────────────────────────────────────────────────

type vmItemDelegate struct{}

func (d vmItemDelegate) Height() int                               { return 2 }
func (d vmItemDelegate) Spacing() int                              { return 0 }
func (d vmItemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d vmItemDelegate) Render(w io.Writer, m list.Model, index int, li list.Item) {
	i, ok := li.(vmItem)
	if !ok {
		return
	}

	name := i.Title()
	desc := i.Description()

	if index == m.Index() {
		name = tuiRunning.Render("▶ " + name)
	} else {
		name = "  " + name
	}

	fmt.Fprintf(w, "%s\n%s", name, tuiHelp.Render("  "+desc))
}
