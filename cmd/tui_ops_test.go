package cmd

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/types"
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

// Toggling a port only asks for the change; the proxy is applied by the
// command the root hands back.
func TestTUIOps_PortToggleAppliesThroughACommand(t *testing.T) {
	f := fakeCluster(t)
	f.AddPrefixResponse("kubectl", "", nil)
	vm := kubeVM("web-prod")
	m := testModel(vm)
	m.mode, m.edit = ModeEdit, newEditModel(vm)
	f.Reset()
	f.AddPrefixResponse("kubectl", "", nil)

	next, cmd := m.Update(key("space"))
	m = next.(tuiModel)
	if !m.busy || cmd == nil || m.edit.apply {
		t.Fatalf("space should hand the apply to a command (busy %v, cmd %v)", m.busy, cmd != nil)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("Update reached the cluster itself: %v", f.Calls())
	}
	m = settle(m, cmd)
	if m.busy || m.mode != ModeEdit {
		t.Errorf("after the apply: busy %v, mode %v; want the form idle and open", m.busy, m.mode)
	}
	if !calledWith(f, "ingressclass", "tailscale") {
		t.Errorf("the command never applied the ports, calls: %v", f.Calls())
	}
}

// Enter on the hardware form scales through a command and closes the form
// when it succeeds.
func TestTUIOps_HardwareAppliesThroughACommand(t *testing.T) {
	f := fakeCluster(t)
	f.AddPrefixResponse("kubectl patch", "", nil)
	f.AddPrefixResponse("kubectl get pod", `{"spec":{"containers":[{"name":"ct"}]}}`, nil)
	f.AddPrefixResponse("kubectl annotate", "", nil)
	f.AddPrefixResponse("kubectl get pvc", `{"metadata":{"annotations":{}}}`, nil)
	f.AddPrefixResponse("kubectl", `{"items":[]}`, nil)
	c := ct.CT{Name: "dev-shell", Namespace: "corral-vms", CPU: 1, Mem: "512Mi", Phase: "Running", Ready: true}
	m := testModel(types.VM{})
	m.mode, m.isCT, m.selectedCT = ModeHWEdit, true, c
	m.hwEdit = newCTHWEditModel(c)
	m.hwEdit.cpu.SetValue("4")

	next, cmd := m.Update(key("enter"))
	m = next.(tuiModel)
	if !m.busy || cmd == nil || len(f.Calls()) != 0 {
		t.Fatalf("enter should hand the scale to a command (busy %v, calls %v)", m.busy, f.Calls())
	}
	if !strings.Contains(m.render(), "Applying hardware") {
		t.Errorf("the form does not show the apply running:\n%s", m.render())
	}
	m = settle(m, cmd)
	if m.mode != ModeList || m.hwEdit.status != "" {
		t.Errorf("after a successful scale: mode %v, status %q; want the list", m.mode, m.hwEdit.status)
	}
	if !calledWith(f, "dev-shell") {
		t.Errorf("the command never scaled the CT, calls: %v", f.Calls())
	}
}

// Opening the Snapshots and Events tabs shows them at once and loads them in a
// command; a snapshot change does the same.
func TestTUIOps_SnapshotsAndEventsLoadThroughCommands(t *testing.T) {
	f := fakeCluster(t)
	f.AddPrefixResponse("kubectl get vmsnapshot", snapshotListJSON(
		snapshotItem("web-snap", "web-prod", "2026-06-01T00:00:00Z", true)), nil)
	f.AddPrefixResponse("kubectl get events", `{"items":[]}`, nil)
	f.AddPrefixResponse("kubectl delete", "", nil)

	for _, tab := range []struct {
		action string
		mode   TUIMode
	}{{"snapshot", ModeSnapshots}, {"events", ModeEvents}} {
		m := testModel(kubeVM("web-prod"))
		m.mode = ModeActions
		m = chooseAction(t, m, tab.action)
		f.Reset()
		f.AddPrefixResponse("kubectl get vmsnapshot", snapshotListJSON(
			snapshotItem("web-snap", "web-prod", "2026-06-01T00:00:00Z", true)), nil)
		f.AddPrefixResponse("kubectl get events", `{"items":[]}`, nil)
		f.AddPrefixResponse("kubectl delete", "", nil)

		next, cmd := m.Update(key("enter"))
		m = next.(tuiModel)
		if m.mode != tab.mode || !m.busy || cmd == nil || len(f.Calls()) != 0 {
			t.Fatalf("%s: want the tab open and a load command, got mode %v busy %v calls %v",
				tab.action, m.mode, m.busy, f.Calls())
		}
		m = settle(m, cmd)
		if len(f.Calls()) == 0 {
			t.Errorf("%s: the command loaded nothing", tab.action)
		}
		if tab.mode != ModeSnapshots {
			continue
		}
		if len(m.snapshots.snaps) != 1 {
			t.Fatalf("snapshots = %d, want the one capture", len(m.snapshots.snaps))
		}

		m = press(m, "x")
		next, cmd = m.Update(key("y"))
		m = next.(tuiModel)
		if !m.busy || calledWith(f, "delete") {
			t.Fatalf("y should hand the delete to a command (busy %v)", m.busy)
		}
		m = settle(m, cmd)
		if !calledWith(f, "delete", "web-snap") || m.snapshots.notice != "deleted web-snap" {
			t.Errorf("delete: notice %q, calls %v", m.snapshots.notice, f.Calls())
		}
	}
}
