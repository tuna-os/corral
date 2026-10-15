package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/types"
)

// ── Edit model (port toggles) ─────────────────────────────────────

type editModel struct {
	vm       types.VM
	ports    []int
	toggled  map[int]bool
	cursor   int
	done     bool
	addInput textinput.Model
	adding   bool
	// apply asks the root model to push toggled to the cluster: the form
	// itself never calls a backend (see portsCmd in cmd/tui_ops.go).
	apply bool
}

func newEditModel(vm types.VM) editModel {
	current := exposedPorts(vm)
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
	case tea.KeyPressMsg:
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
		// Bubble Tea v2 names the space key "space"; the old " " never
		// matches, which left the documented space-to-toggle binding dead.
		case " ", "space", "enter":
			return m, m.activateCursor()
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
					m.apply = true
				}
			}
		}
	}
	return m, nil
}

func (m editModel) updateAdding(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			if port, err := strconv.Atoi(m.addInput.Value()); err == nil && port > 0 && port < 65536 {
				m.ports = append(m.ports, port)
				m.toggled[port] = true
				m.apply = true
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

// activateCursor is what space, enter, and a click on a row all do: toggle the
// port under the cursor, or open the add prompt, or clear every port. One
// function, so a mouse click cannot drift from what the keyboard does.
func (m *editModel) activateCursor() tea.Cmd {
	switch {
	case m.cursor < len(m.ports):
		p := m.ports[m.cursor]
		m.toggled[p] = !m.toggled[p]
		m.apply = true
	case m.cursor == len(m.ports):
		m.adding = true
		m.addInput.Focus()
		return textinput.Blink
	case m.cursor == len(m.ports)+1:
		m.toggled = make(map[int]bool)
		m.apply = true
	}
	return nil
}

// enabledPorts is the set of ports the form has switched on.
func (m editModel) enabledPorts() []int {
	var enabled []int
	for p, on := range m.toggled {
		if on {
			enabled = append(enabled, p)
		}
	}
	return enabled
}

// applyPorts publishes exactly the enabled ports of vm through its backend's
// Publisher; none unpublishes.
func applyPorts(vm types.VM, enabled []int) error {
	publisher, err := vmPublisher(vm)
	if err != nil {
		return err
	}
	return publisher.Publish(vm.Name, enabled)
}

func (m editModel) render() string {
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

// ── Port helpers ──────────────────────────────────────────────────

// vmPublisher is the VM's backend Publisher, or a refusal naming the backend.
func vmPublisher(vm types.VM) (backend.Publisher, error) {
	adapter, err := backend.For(vm.Ref())
	if err != nil {
		return nil, err
	}
	publisher, ok := adapter.(backend.Publisher)
	if !ok {
		return nil, unsupported(adapter, "ports")
	}
	return publisher, nil
}

// exposedPorts lists the VM's published ports; none when its backend has no
// Publisher (the menu does not offer the editor there).
func exposedPorts(vm types.VM) []int {
	publisher, err := vmPublisher(vm)
	if err != nil {
		return nil
	}
	return publisher.PublishedPorts(vm.Name)
}

func (m editModel) View() tea.View { return tea.NewView(m.render()) }
