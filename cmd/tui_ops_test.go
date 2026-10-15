package cmd

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A key that needs a backend does not call it from Update: the model goes busy
// and hands back a command, and the fleet only changes when its result lands.
func TestTUIOps_UpdateHandsBackendWorkToACommand(t *testing.T) {
	m := selectVM(t, demoModel(t), "dev-fedora") // stopped

	next, cmd := m.Update(key("s"))
	m = next.(tuiModel)
	if !m.busy || cmd == nil {
		t.Fatalf("'s' should start a command (busy %v, cmd %v)", m.busy, cmd != nil)
	}
	if vmByName(t, m, "dev-fedora").Running {
		t.Fatal("Update started the VM itself instead of leaving it to the command")
	}

	next, _ = m.Update(cmd())
	m = next.(tuiModel)
	if m.busy {
		t.Error("the result should clear busy")
	}
	if !vmByName(t, m, "dev-fedora").Running {
		t.Error("the command's result did not show the started VM")
	}
	if m.noticeKind != "ok" {
		t.Errorf("notice = %q (%s), want the start reported", m.notice, m.noticeKind)
	}
}

// While a command runs, keys and clicks are dropped — they would act on a fleet
// that is about to change — but ctrl+c still quits.
func TestTUIOps_BusyDropsInputButQuits(t *testing.T) {
	m := selectVM(t, demoModel(t), "web-prod")
	next, cmd := m.Update(key("r"))
	m = next.(tuiModel)
	if !m.busy || cmd == nil {
		t.Fatal("'r' should start a refresh command")
	}

	for _, k := range []string{"enter", "c", "d", "q"} {
		next, extra := m.Update(key(k))
		if got := next.(tuiModel); got.mode != ModeList || got.quitting || extra != nil {
			t.Errorf("%q while busy went to %v (quitting %v)", k, got.mode, got.quitting)
		}
	}
	if next, _ := m.Update(click(3, headerHeight)); next.(tuiModel).lastClickRow != m.lastClickRow || !next.(tuiModel).lastClickAt.Equal(m.lastClickAt) {
		t.Error("a click while busy was handled")
	}
	if next, _ := m.Update(key("ctrl+c")); !next.(tuiModel).quitting {
		t.Error("ctrl+c while busy should still quit")
	}

	m = settle(m, cmd)
	if m.busy || m.notice != "Fleet refreshed" {
		t.Errorf("after the refresh: busy %v, notice %q", m.busy, m.notice)
	}
}

// A failed clone keeps its form open with the reason, once the command reports.
func TestTUIOps_CloneFailureKeepsTheForm(t *testing.T) {
	m := selectVM(t, demoModel(t), "golden-ubuntu")
	m = press(m, "enter")
	m = chooseAction(t, m, "clone")
	m = press(m, "enter")
	m.cloneInput.SetValue("web-prod") // already exists

	next, cmd := m.Update(key("enter"))
	m = next.(tuiModel)
	if m.mode != ModeCloneInput || cmd == nil {
		t.Fatalf("enter should start the clone and keep the form (mode %v)", m.mode)
	}
	if view := m.render(); !strings.Contains(view, "Cloning golden-ubuntu") {
		t.Errorf("the clone form does not show the clone running:\n%s", view)
	}
	m = settle(m, cmd)
	if m.mode != ModeCloneInput || !strings.Contains(m.cloneErr, "already exists") {
		t.Errorf("mode %v, cloneErr %q; want the form open with the reason", m.mode, m.cloneErr)
	}
}

// The doctor screen opens at once and fills in when the checks land.
func TestTUIOps_DoctorChecksRunInACommand(t *testing.T) {
	m := demoModel(t)
	next, cmd := m.Update(key("d"))
	m = next.(tuiModel)
	if m.mode != ModeDoctor || len(m.doctorRows) != 0 || cmd == nil {
		t.Fatalf("'d' should open an empty doctor screen and start the checks (mode %v, rows %d)", m.mode, len(m.doctorRows))
	}
	if !strings.Contains(m.render(), "Checking the fleet") {
		t.Errorf("the doctor screen does not say the checks are running:\n%s", m.render())
	}
	m = settle(m, cmd)
	if len(m.doctorRows) == 0 {
		t.Error("the checks never landed")
	}
}

// A double click on an action is the same path as enter, command included —
// a double-clicked SSH has to actually quit the program.
func TestTUIOps_DoubleClickReturnsTheActionsCommand(t *testing.T) {
	postQuitAction = nil
	t.Cleanup(func() { postQuitAction = nil })
	m := selectVM(t, demoModel(t), "web-prod")
	m = press(m, "enter")
	m = chooseAction(t, m, "ssh")
	g := geomOf(m.actionsList, actionItemDelegate{}.Height()+actionItemDelegate{}.Spacing())
	y := g.titleHeight + m.actionsList.Index()*g.stride

	m = mouse(m, click(2, y))
	next, cmd := m.Update(click(2, y))
	if !next.(tuiModel).quitting || cmd == nil || postQuitAction == nil {
		t.Fatal("a double-clicked SSH should quit and hand over the terminal")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("the double click's command is not tea.Quit")
	}
}
