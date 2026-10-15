package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/types"
)

// ── Hardware edit (CPU / RAM) ─────────────────────────────────────

type hwEditModel struct {
	vm  types.VM
	cpu textinput.Model
	mem textinput.Model
	// isCT switches apply() to ct.Scale. The web UI scales a CT from the same
	// form it scales a VM from, so the editor is shared rather than copied —
	// only the call at the end and the note under it differ.
	isCT   bool
	ct     ct.CT
	focus  int // 0 = cpu, 1 = mem
	status string
	done   bool
}

func newHWEditModel(vm types.VM) hwEditModel {
	cpu := textinput.New()
	cpu.SetValue(strconv.Itoa(vm.CPU))
	cpu.CharLimit = 3
	cpu.SetWidth(6)
	cpu.Focus()

	mem := textinput.New()
	mem.SetValue(vm.Mem)
	mem.CharLimit = 8
	mem.SetWidth(8)

	return hwEditModel{vm: vm, cpu: cpu, mem: mem}
}

func (m hwEditModel) Init() tea.Cmd { return textinput.Blink }

func (m hwEditModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
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
			// A failed scale keeps the form open with the reason on screen —
			// closing it would drop the only copy of the error.
			m.done = m.status == ""
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
	if m.isCT {
		cpu := m.ct.CPU
		if v, err := strconv.Atoi(strings.TrimSpace(m.cpu.Value())); err == nil && v > 0 {
			cpu = v
		}
		mem := strings.TrimSpace(m.mem.Value())
		if mem == "" {
			mem = m.ct.Mem
		}
		if cpu == m.ct.CPU && mem == m.ct.Mem {
			return
		}
		if err := ct.Scale(m.ct.Name, ns, cpu, mem); err != nil {
			m.status = err.Error()
		}
		return
	}
	c := kubevirt.NewClient(ns)
	if v, err := strconv.Atoi(strings.TrimSpace(m.cpu.Value())); err == nil && v > 0 && v != m.vm.CPU {
		c.ScaleCPU(m.vm.Name, v)
	}
	if mem := strings.TrimSpace(m.mem.Value()); mem != "" && mem != m.vm.Mem {
		c.ScaleMemory(m.vm.Name, mem)
	}
}

func (m hwEditModel) render() string {
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
	switch {
	case m.isCT:
		note = "resized in place where the cluster allows it, otherwise stop/start to apply"
	case !m.vm.LiveMigratable:
		note = "VM will restart to apply"
	}
	sb.WriteString("\n" + tuiHelp.Render("  "+note))
	if m.status != "" {
		sb.WriteString("\n" + tuiStopped.Render("  "+m.status))
	}
	sb.WriteString("\n" + tuiHelp.Render("  tab switch  enter apply  esc cancel"))
	return sb.String()
}

func (m hwEditModel) View() tea.View { return tea.NewView(m.render()) }
