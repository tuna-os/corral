package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TUI model (stub for now)
type mainModel struct {
	choices  []string
	cursor   int
	selected int
}

func (m mainModel) Init() tea.Cmd {
	return nil
}

func (m mainModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.choices)-1 {
				m.cursor++
			}
		case "enter":
			m.selected = m.cursor
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m mainModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Render("TailVM — select a VM:")
	s := title + "\n\n"

	for i, choice := range m.choices {
		cursor := "  "
		if m.cursor == i {
			cursor = lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Render("▶ ")
		}
		s += fmt.Sprintf("%s%s\n", cursor, choice)
	}

	s += "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("↑/↓ navigate  ⏎ select  q quit")
	return s
}

func runTUI() {
	// Collect VM names
	names := allVMNames()
	if len(names) == 0 {
		fmt.Println("No VMs found. Create one: tailvm create <name>")
		return
	}

	m := mainModel{choices: names}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
	}
}
