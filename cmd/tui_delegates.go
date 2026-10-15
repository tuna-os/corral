package cmd

import (
	"fmt"
	"io"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ── Actions list delegate ─────────────────────────────────────────

type actionItemDelegate struct{ th theme }

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
		label = d.th.ok.Render("▶ " + label)
	} else {
		label = "  " + label
	}
	_, _ = fmt.Fprint(w, label)
}

// ── List delegates ────────────────────────────────────────────────

type vmItemDelegate struct{ th theme }

func (d vmItemDelegate) Height() int                               { return 2 }
func (d vmItemDelegate) Spacing() int                              { return 0 }
func (d vmItemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d vmItemDelegate) Render(w io.Writer, m list.Model, index int, li list.Item) {
	// The list mixes vmItem and ctItem (both satisfy list.Item) — render via
	// the shared shape, or CTs silently occupy invisible rows.
	i, ok := li.(interface {
		Title() string
		Description() string
	})
	if !ok {
		return
	}

	// The run state drives the dot. A row now says whether the thing is up
	// before you have read its name.
	state := stateStopped
	switch v := li.(type) {
	case vmItem:
		state = v.state
	case ctItem:
		state = v.state
	}

	name := i.Title()
	selected := index == m.Index()

	var marker, nameStyled string
	if selected {
		marker = lipgloss.NewStyle().Foreground(d.th.p.primary).Bold(true).Render("▌")
		nameStyled = d.th.nameSelected.Render(name)
	} else {
		marker = " "
		nameStyled = d.th.name.Render(name)
	}

	line1 := fmt.Sprintf("%s %s %s", marker, d.th.statusDot(state), nameStyled)
	line2 := fmt.Sprintf("%s   %s", marker, i.Description())

	// MaxWidth, not a byte slice: the description carries ANSI colour now, and
	// cutting it by length would sever an escape sequence and bleed colour
	// down the rest of the screen.
	if width := m.Width(); width > 4 {
		clamp := lipgloss.NewStyle().MaxWidth(width)
		line1, line2 = clamp.Render(line1), clamp.Render(line2)
	}

	_, _ = fmt.Fprintf(w, "%s\n%s", line1, line2)
}
