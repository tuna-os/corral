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
	"github.com/hanthor/tailvm-go/pkg/kubevirt"
	"github.com/hanthor/tailvm-go/pkg/qemu"
	"github.com/hanthor/tailvm-go/pkg/types"
)

// ── Styles ────────────────────────────────────────────────────────
var (
	tuiTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Padding(0, 1)
	tuiRunning  = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	tuiStopped  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	tuiProxyOn  = "🔵"
	tuiProxyOff = "○"
	tuiHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// ── VM item for the list ──────────────────────────────────────────

type vmItem struct {
	vm      types.VM
	display string
}

func (i vmItem) Title() string       { return i.vm.Name }
func (i vmItem) Description() string  { return i.display }
func (i vmItem) FilterValue() string  { return i.vm.Name }

func vmToItem(vm types.VM) vmItem {
	proxy := tuiProxyOff
	if vm.VNC == "on" {
		proxy = tuiProxyOn
	} else if vm.VNC == "pending" {
		proxy = "⏳"
	}
	return vmItem{
		vm: vm,
		display: fmt.Sprintf("%s  %s  ports:%s  %d CPU / %s",
			vm.Status, vm.Backend, proxy, vm.CPU, vm.Mem),
	}
}

// ── Main model ────────────────────────────────────────────────────

type tuiModel struct {
	list     list.Model
	quitting bool
	state    string // "list", "actions", "edit"
	selected types.VM
	edit     editModel
	width    int
	height   int
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
		fmt.Println("No VMs found. Create one: tailvm create <name>")
		os.Exit(0)
	}

	l := list.New(items, vmItemDelegate{}, 0, 0)
	l.Title = "TailVM"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.Styles.Title = tuiTitle

	return tuiModel{list: l, state: "list"}
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

		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "enter":
			if m.state == "list" {
				if item, ok := m.list.SelectedItem().(vmItem); ok {
					m.selected = item.vm
					m.state = "actions"
					return m, nil
				}
			}
			return m, nil
		case "esc":
			if m.state == "actions" {
				m.state = "list"
				return m, nil
			}
		case "e":
			if m.state == "actions" {
				m.state = "edit"
				m.edit = newEditModel(m.selected)
				return m, m.edit.Init()
			}
		case "s":
			if m.state == "actions" {
				m.performAction("start")
				m.refreshList()
				m.state = "list"
			}
		case "x":
			if m.state == "actions" {
				m.performAction("stop")
				m.refreshList()
				m.state = "list"
			}
		case "d":
			if m.state == "actions" {
				m.performAction("delete")
				m.refreshList()
				m.state = "list"
			}
		case "v":
			if m.state == "actions" {
				m.performAction("viewer")
				m.state = "list"
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
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

	if m.state == "actions" {
		return m.actionsView()
	}

	return m.list.View()
}

func (m tuiModel) actionsView() string {
	vm := m.selected
	b := vm.Backend
	if b == "" {
		b = "qemu"
	}

	var sb strings.Builder
	sb.WriteString(tuiTitle.Render(fmt.Sprintf(" %s (%s) ", vm.Name, b)))
	sb.WriteString("\n\n")

	actions := []struct {
		key, label string
	}{
		{"s", "▶  Start"},
		{"x", "■  Stop"},
		{"v", "🖵  Viewer (VNC)"},
		{"e", "✎  Edit ports"},
		{"d", "✕  Delete"},
	}
	for _, a := range actions {
		sb.WriteString(fmt.Sprintf("  [%s] %s\n", tuiRunning.Render(a.key), a.label))
	}

	sb.WriteString("\n")
	sb.WriteString(tuiHelp.Render("  esc back  ctrl+c quit"))
	return sb.String()
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
	// Get current exposed ports
	current := exposedPorts(vm.Name, vm.Namespace)
	toggled := make(map[int]bool)
	for _, p := range current {
		toggled[p] = true
	}

	allPorts := append([]int{}, types.DefaultPorts...)
	// Add any custom ports not in defaults
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
				// "Add port" selected
				m.adding = true
				m.addInput.Focus()
				return m, textinput.Blink
			} else if m.cursor == len(m.ports)+1 {
				// "Remove all" selected
				m.toggled = make(map[int]bool)
				m.applyPorts()
			}
		case "backspace":
			if m.cursor < len(m.ports) {
				// Remove custom port
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
		// Delete proxy resources
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

// ── Proxy helpers ─────────────────────────────────────────────────

func exposedPorts(name, ns string) []int {
	return kubevirt.ExposedPorts(name, ns)
}

// ── List delegate ─────────────────────────────────────────────────

type vmItemDelegate struct{}

func (d vmItemDelegate) Height() int                             { return 2 }
func (d vmItemDelegate) Spacing() int                            { return 0 }
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
