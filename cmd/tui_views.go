package cmd

// TUI views that close the gap with the web UI.
//
// The web dashboard grew a tab per concern — Snapshots, Events, the template
// mark, CT hardware — while the TUI kept a single fire-and-forget action list.
// The result was a terminal that could *take* a snapshot but never show you the
// ones you had, and a "Snapshot" action wired straight to pkg/kubevirt even
// though snapshots became backend-neutral in #134.
//
// Parity here means the same operations against the same packages, not the same
// layout: the web UI's tabs become states of the one model, and anything that
// only reads well in a browser (live CPU graphs, the noVNC canvas, image
// uploads) is deliberately left out.

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/snapshot"
	"github.com/tuna-os/corral/pkg/types"
)

// ── Snapshots ─────────────────────────────────────────────────────

// snapshotsState is the Snapshots tab: the VM's captures, plus create,
// restore, and delete. It goes through pkg/snapshot rather than pkg/kubevirt,
// so a libvirt or Incus instance gets its own adapter's semantics — including
// the honest refusals, which are shown verbatim instead of being swallowed.
type snapshotsState struct {
	ref     types.InstanceRef
	vmName  string
	running bool
	snaps   []snapshot.Snapshot
	cursor  int
	err     string
	notice  string

	// naming is the "new snapshot" prompt. An empty value asks the adapter to
	// generate a name, matching the web UI's optional name field.
	naming bool
	name   textinput.Model

	// confirm is "" | "delete" | "restore" — a destructive step never runs
	// off a single keypress, same as the web UI's confirm dialogs.
	confirm string
}

// newSnapshotsState opens the tab empty; snapshotsCmd fills it.
func newSnapshotsState(vm types.VM) snapshotsState {
	ti := textinput.New()
	ti.Placeholder = "name (blank = auto)"
	ti.CharLimit = 63
	ti.SetWidth(32)

	return snapshotsState{ref: vm.Ref(), vmName: vm.Name, running: vm.Running, name: ti}
}

// snapshotOp is one change to an instance's captures. It returns the notice
// to show when it worked.
type snapshotOp func(adapter snapshot.Adapter, ref types.InstanceRef) (string, error)

// snapshotsMsg is the result of snapshotsCmd. opErr is set when the change
// itself failed, in which case nothing was listed again.
type snapshotsMsg struct {
	opErr   string
	notice  string
	snaps   []snapshot.Snapshot
	listErr string
}

// snapshotsCmd runs op (nil only lists) through the instance's snapshot
// adapter, then lists its captures again.
func (m *tuiModel) snapshotsCmd(label string, op snapshotOp) tea.Cmd {
	ref := m.snapshots.ref
	m.busyWith(label)
	return func() tea.Msg {
		adapter, err := snapshot.For(ref)
		if err != nil {
			if op != nil {
				return snapshotsMsg{opErr: err.Error()}
			}
			return snapshotsMsg{listErr: err.Error()}
		}
		var res snapshotsMsg
		if op != nil {
			notice, err := op(adapter, ref)
			if err != nil {
				return snapshotsMsg{opErr: err.Error()}
			}
			res.notice = notice
		}
		snaps, err := adapter.List(ref)
		if err != nil {
			res.listErr = err.Error()
			return res
		}
		res.snaps = snaps
		return res
	}
}

// apply shows a snapshotsCmd result.
func (s *snapshotsState) apply(msg snapshotsMsg) {
	if msg.opErr != "" {
		s.err = msg.opErr
		return
	}
	if msg.notice != "" {
		s.notice = msg.notice
	}
	if msg.listErr != "" {
		s.snaps, s.err = nil, msg.listErr
		return
	}
	s.snaps, s.err = msg.snaps, ""
	if s.cursor >= len(s.snaps) {
		s.cursor = max(0, len(s.snaps)-1)
	}
}

func createSnapshot(name string) snapshotOp {
	return func(adapter snapshot.Adapter, ref types.InstanceRef) (string, error) {
		snap, err := adapter.Create(ref, strings.TrimSpace(name))
		if err != nil {
			return "", err
		}
		// The consistency the adapter actually achieved, not the one that was
		// hoped for — a crash-consistent capture of a database is worth saying
		// out loud, and the web UI says it too.
		return fmt.Sprintf("created %s (%s-consistent)", snap.Name, snap.Consistency), nil
	}
}

func restoreSnapshot(name string) snapshotOp {
	return func(adapter snapshot.Adapter, ref types.InstanceRef) (string, error) {
		if err := adapter.Restore(ref, name); err != nil {
			return "", err
		}
		return "restoring from " + name, nil
	}
}

func deleteSnapshot(name string) snapshotOp {
	return func(adapter snapshot.Adapter, ref types.InstanceRef) (string, error) {
		if err := adapter.Delete(ref, name); err != nil {
			return "", err
		}
		return "deleted " + name, nil
	}
}

func (s *snapshotsState) selected() (snapshot.Snapshot, bool) {
	if s.cursor < 0 || s.cursor >= len(s.snaps) {
		return snapshot.Snapshot{}, false
	}
	return s.snaps[s.cursor], true
}

// updateSnapshots handles the Snapshots view's keys. It returns false when the
// view is done and the caller should go back to the actions menu, and the
// command for a change the key asked for.
func (m *tuiModel) updateSnapshots(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	s := &m.snapshots

	if s.naming {
		switch msg.String() {
		case "enter":
			s.naming = false
			value := s.name.Value()
			s.name.Reset()
			return true, m.snapshotsCmd("Creating snapshot", createSnapshot(value))
		case "esc":
			s.naming = false
			s.name.Reset()
		default:
			s.name, _ = s.name.Update(msg)
		}
		return true, nil
	}

	if s.confirm != "" {
		confirm := s.confirm
		s.confirm = ""
		snap, ok := s.selected()
		if !ok {
			return true, nil
		}
		switch msg.String() {
		case "y", "Y":
			if confirm == "delete" {
				return true, m.snapshotsCmd("Deleting "+snap.Name, deleteSnapshot(snap.Name))
			}
			return true, m.snapshotsCmd("Restoring "+snap.Name, restoreSnapshot(snap.Name))
		}
		return true, nil
	}

	switch msg.String() {
	case "esc", "q":
		return false, nil
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.snaps)-1 {
			s.cursor++
		}
	case "n":
		s.naming = true
		s.err, s.notice = "", ""
		s.name.Focus()
	case "r":
		s.err, s.notice = "", ""
		return true, m.snapshotsCmd("Loading snapshots", nil)
	case "enter":
		if _, ok := s.selected(); ok {
			s.confirm = "restore"
		}
	case "x", "d":
		if _, ok := s.selected(); ok {
			s.confirm = "delete"
		}
	}
	return true, nil
}

func (m tuiModel) snapshotsView() string {
	s := m.snapshots
	th := m.th

	var body string
	switch {
	case s.err != "":
		// An Unsupported refusal carries its own remedy; print it whole rather
		// than reducing it to "failed".
		body = th.danger.Render("✖ " + s.err)
	case len(s.snaps) == 0:
		body = th.muted.Render("No snapshots yet. Press n to take one.")
	default:
		t := table.New().
			Width(tableWidth(m.width)).
			Border(lipgloss.RoundedBorder()).
			BorderStyle(lipgloss.NewStyle().Foreground(th.p.border)).
			Headers("", "NAME", "READY", "CONSISTENCY", "CREATED").
			StyleFunc(func(row, col int) lipgloss.Style {
				base := lipgloss.NewStyle().Padding(0, 1)
				if row == table.HeaderRow {
					return base.Bold(true).Foreground(th.p.muted)
				}
				switch col {
				case 1:
					if row == s.cursor {
						return base.Bold(true).Foreground(th.p.text)
					}
					return base.Foreground(th.p.text)
				case 4:
					return base.Foreground(th.p.muted)
				default:
					return base
				}
			})
		for i, snap := range s.snaps {
			marker := " "
			if i == s.cursor {
				marker = lipgloss.NewStyle().Foreground(th.p.primary).Bold(true).Render("▌")
			}
			ready := th.warn.Render("pending")
			if snap.Ready {
				ready = th.ok.Render("ready")
			}
			t.Row(marker, snap.Name, ready, consistencyLabel(snap.Consistency, th), humanAge(snap.Created))
		}
		body = t.String()
	}

	if s.naming {
		body += "\n\n  " + th.label.Render("NAME") + "  " + s.name.View()
	}

	notice, noticeStyle := s.notice, th.ok
	if s.confirm == "delete" {
		if snap, ok := s.selected(); ok {
			notice, noticeStyle = "Delete "+snap.Name+"? y to confirm, any other key cancels", th.danger
		}
	} else if s.confirm == "restore" {
		if snap, ok := s.selected(); ok {
			notice, noticeStyle = "Restore "+m.snapshots.vmName+" from "+snap.Name+"? y to confirm", th.warn
		}
	} else if notice == "" && s.running {
		// Every adapter that needs the instance stopped says so on refusal,
		// but saying it before the keypress saves the round trip.
		notice, noticeStyle = "VM is running — a restore may be refused until it is stopped", th.muted
	}

	hints := []keyHint{{"n", "new"}, {"enter", "restore"}, {"x", "delete"}, {"r", "reload"}, {"esc", "back"}}
	if s.naming {
		hints = []keyHint{{"enter", "create"}, {"esc", "cancel"}}
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		th.title.Render(fmt.Sprintf(" Snapshots · %s ", s.vmName)),
		"",
		body,
		th.statusBar(m.width, hints, notice, noticeStyle),
	)
}

// consistencyLabel colours what a capture actually caught. Crash-consistent is
// a warning, not a detail: it is the one that can restore into a broken guest.
func consistencyLabel(c snapshot.Consistency, th theme) string {
	switch c {
	case snapshot.Offline:
		return th.ok.Render("offline")
	case snapshot.Filesystem:
		return th.ok.Render("filesystem")
	case snapshot.Crash:
		return th.warn.Render("crash")
	case "":
		return th.muted.Render("—")
	default:
		return th.muted.Render(string(c))
	}
}

// ── Events ────────────────────────────────────────────────────────

// eventsState is the Events tab: the VM's recent activity from its backend's
// Eventer — Kubernetes events for the VM and its virt-launcher pod, the unit's
// journal, PVE tasks. The actions menu offers it only where an Eventer exists.
type eventsState struct {
	vmName string
	events []backend.Event
	err    string
}

// eventsMsg carries freshly loaded events.
type eventsMsg struct{ state eventsState }

// eventsCmd loads the selected VM's events.
func (m *tuiModel) eventsCmd() tea.Cmd {
	vm := m.selected
	m.busyWith("Loading events")
	return func() tea.Msg { return eventsMsg{state: loadEvents(vm)} }
}

// loadEvents fetches recent events for vm and its virt-launcher pod.
func loadEvents(vm types.VM) eventsState {
	s := eventsState{vmName: vm.Name}
	adapter, err := backend.For(vm.Ref())
	if err != nil {
		s.err = err.Error()
		return s
	}
	eventer, ok := adapter.(backend.Eventer)
	if !ok {
		s.err = unsupported(adapter, "events").Error()
		return s
	}
	evs, err := eventer.Events(vm.Name)
	if err != nil {
		s.err = err.Error()
		return s
	}
	s.events = evs
	return s
}

func (m tuiModel) eventsView() string {
	th := m.th
	s := m.events

	var body string
	switch {
	case s.err != "":
		body = th.danger.Render("✖ " + s.err)
	case len(s.events) == 0:
		body = th.muted.Render("No recent events for this VM.")
	default:
		t := table.New().
			Width(tableWidth(m.width)).
			Border(lipgloss.RoundedBorder()).
			BorderStyle(lipgloss.NewStyle().Foreground(th.p.border)).
			Headers("", "AGE", "REASON", "OBJECT", "MESSAGE").
			StyleFunc(func(row, col int) lipgloss.Style {
				base := lipgloss.NewStyle().Padding(0, 1)
				if row == table.HeaderRow {
					return base.Bold(true).Foreground(th.p.muted)
				}
				switch col {
				case 3:
					return base.Foreground(th.p.accent)
				case 4:
					return base.Foreground(th.p.muted).MaxWidth(maxDetail(m.width))
				default:
					return base
				}
			})
		for _, e := range s.events {
			mark := th.muted.Render("·")
			if e.Kind == "Warning" {
				mark = th.warn.Render("!")
			}
			t.Row(mark, humanAge(e.Time), e.Reason, e.Object, oneLine(e.Message))
		}
		body = t.String()
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		th.title.Render(fmt.Sprintf(" Events · %s ", s.vmName)),
		"",
		body,
		th.statusBar(m.width, []keyHint{{"r", "reload"}, {"esc", "back"}}, "", th.muted),
	)
}

// oneLine flattens a multi-line event message. Kubernetes puts whole command
// lines in a Message, and a newline inside a table cell breaks the borders.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// humanAge turns a backend timestamp into "4m" / "3h" / "2d", the way kubectl
// and the web UI both show it. An unparseable stamp is passed through: a raw
// value the operator can still read beats an empty cell.
func humanAge(stamp string) string {
	if stamp == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return stamp
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

// ── Template mark ─────────────────────────────────────────────────

// toggleTemplate marks or unmarks the golden template label — the same
// MarkTemplate call behind the web UI's "Make template" button. Templates are
// what `corral clone` copies from, so a fleet driven from the terminal needs to
// be able to set the mark without opening a browser.
func toggleTemplate(vm types.VM) (notice, kind string) {
	on := !vm.IsTemplate
	err := func() error {
		adapter, err := backend.For(vm.Ref())
		if err != nil {
			return err
		}
		templater, ok := adapter.(backend.Templater)
		if !ok {
			return unsupported(adapter, "template")
		}
		return templater.MarkTemplate(vm.Name, on)
	}()
	if err != nil {
		return "template: " + err.Error(), "error"
	}
	if on {
		return vm.Name + " marked as a template", "ok"
	}
	return vm.Name + " is no longer a template", "ok"
}

// ── CT hardware ───────────────────────────────────────────────────

// newCTHWEditModel is the CT side of the hardware editor. The web UI scales a
// CT from the same form it scales a VM from; ct.Scale resizes the pod in place
// where the cluster allows it and asks for a stop/start where it does not,
// which is why the note differs from a VM's hotplug promise.
func newCTHWEditModel(c ct.CT) hwEditModel {
	m := newHWEditModel(types.VM{Name: c.Name, Namespace: c.Namespace, CPU: c.CPU, Mem: c.Mem})
	m.isCT, m.ct = true, c
	return m
}

// ── the operation contract ────────────────────────────────────────

// backendAction runs a power, pause, or migrate action on vm through
// pkg/backend's contract, and returns the outcome for the status bar.
//
// This is what replaced performAction's per-backend if/else ladder. The win is
// not brevity: it is that a backend which cannot do something now *says so*.
// The ladder's final `else` sent every unknown backend to local QEMU, and its
// pause and migrate branches did nothing at all off KubeVirt — the operator
// pressed a key, the fleet did not change, and nothing explained why.
func backendAction(vm types.VM, action string) (notice, kind string) {
	adapter, err := backend.For(vm.Ref())
	if err != nil {
		return err.Error(), "error"
	}

	var opErr error
	switch action {
	case "start":
		opErr = withPower(adapter, action, func(p backend.Power) error { return p.Start(vm.Name) })
	case "stop":
		opErr = withPower(adapter, action, func(p backend.Power) error { return p.Stop(vm.Name) })
	case "delete":
		opErr = withPower(adapter, action, func(p backend.Power) error { return p.Delete(vm.Name) })
	case "restart":
		restarter, ok := adapter.(backend.Restarter)
		if !ok {
			opErr = unsupported(adapter, "restart")
			break
		}
		opErr = restarter.Restart(vm.Name)
	case "pause", "unpause":
		suspender, ok := adapter.(backend.Suspender)
		if !ok {
			opErr = unsupported(adapter, "pause")
			break
		}
		if action == "pause" {
			opErr = suspender.Pause(vm.Name)
		} else {
			opErr = suspender.Resume(vm.Name)
		}
	case "migrate":
		mover, ok := adapter.(backend.Mover)
		if !ok {
			opErr = unsupported(adapter, "migrate")
			break
		}
		// The backend's own pre-flight, rather than Corral guessing: PVE answers
		// this properly and names the blocking resource.
		if can, why := mover.CanMigrate(vm.Name); !can {
			opErr = fmt.Errorf("%s cannot migrate: %s", vm.Name, why)
			break
		}
		opErr = mover.Migrate(vm.Name, "")
	}

	if opErr != nil {
		return opErr.Error(), "error"
	}
	return fmt.Sprintf("%s: %s", vm.Name, pastTense(action)), "ok"
}

func withPower(adapter backend.Adapter, action string, run func(backend.Power) error) error {
	power, ok := adapter.(backend.Power)
	if !ok {
		return unsupported(adapter, action)
	}
	return run(power)
}

// unsupported is the refusal an operator sees instead of silence. It names the
// backend, because "not supported" without saying by what is a dead end.
func unsupported(adapter backend.Adapter, action string) error {
	return fmt.Errorf("the %s backend does not support %s yet — see docs/backend-parity.md",
		adapter.Backend(), action)
}

func pastTense(action string) string {
	switch action {
	case "pause":
		return "paused"
	case "stop":
		return "stopped"
	case "delete":
		return "deleted"
	case "unpause":
		return "resumed"
	case "migrate":
		return "migrating"
	default:
		return action + "ed"
	}
}
