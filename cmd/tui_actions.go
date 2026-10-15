package cmd

import (
	"context"
	"fmt"
	"os"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/export"
	"github.com/tuna-os/corral/pkg/types"
)

// runSelectedAction runs whatever the actions menu has highlighted. Enter and a
// double click both come through here, so a mouse can never reach an action a
// keyboard cannot — including the destructive ones, which still land on their
// confirmation rather than firing.
func (m *tuiModel) runSelectedAction() tea.Cmd {
	item, ok := m.actionsList.SelectedItem().(actionItem)
	if !ok {
		return nil
	}
	switch item.id {
	case "ports":
		m.mode = ModeEdit
		// The form shows once the VM's current ports have loaded.
		m.edit = editModel{vm: m.selected}
		return m.portsLoadCmd()
	case "hardware":
		m.mode = ModeHWEdit
		if m.isCT {
			m.hwEdit = newCTHWEditModel(m.selectedCT)
		} else {
			m.hwEdit = newHWEditModel(m.selected)
		}
		return m.hwEdit.Init()
	case "clone":
		m.mode = ModeCloneInput
		m.cloneErr = ""
		m.cloneInput = newCloneInput(m.selected.Name)
		return m.cloneInput.Focus()
	case "snapshot":
		// Parity with the web UI's Snapshots tab: the action opens the captures
		// the VM already has rather than silently taking one more.
		m.snapshots = newSnapshotsState(m.selected)
		m.mode = ModeSnapshots
		return m.snapshotsCmd("Loading snapshots", nil)
	case "events":
		m.events = eventsState{vmName: m.selected.Name}
		m.mode = ModeEvents
		return m.eventsCmd()
	case "pool":
		m.pools = newPoolAssignState(m.selected)
		m.mode = ModePools
	case "move":
		// Deliberately not "migrate": that verb is the live, same-backend one,
		// and the two must not be confusable in a menu (ADR-0010).
		m.pools = newMoveDestinationState(m.selected)
		m.mode = ModePools
	case "template", "start", "stop", "restart", "pause", "unpause", "migrate":
		m.mode = ModeList
		return m.runActionCmd(item.id)
	case "ssh", "viewer", "export":
		actionID, t := item.id, m.target()
		postQuitAction = func() { t.run(actionID) }
		m.quitting = true
		return tea.Quit
	case "console":
		name, ns := m.selectedCT.Name, m.selectedCT.Namespace
		postQuitAction = func() { _ = ct.Console(name, ns) }
		m.quitting = true
		return tea.Quit
	case "delete":
		m.mode = ModeConfirmDelete
	}
	return nil
}

// run performs action on the target and returns the outcome for the status
// bar. It calls backends directly, so the TUI runs it from a command (see
// cmd/tui_ops.go) or after the program has quit, never inside Update.
func (t actionTarget) run(action string) (notice, kind string) {
	if t.isCT {
		ctAction(t.ct, action)
		return "", ""
	}
	// Power, pause, and migrate go through the operation contract
	// (pkg/backend): one adapter per backend, asserted for the family the
	// action needs. This replaced a per-backend if/else ladder per action —
	// the shape that made every backend but KubeVirt best-effort, because
	// adding an action meant remembering to add four more branches.
	//
	// The refusals matter as much as the calls: an action a backend cannot
	// perform now reports why, where the ladder silently did nothing.
	switch action {
	case "start", "stop", "restart", "delete", "pause", "unpause", "migrate":
		notice, kind = backendAction(t.vm, action)
		if action == "delete" && registryStore != nil {
			if err := registryStore.RemoveRef(t.vm.Ref()); err != nil && kind == "ok" {
				return notice + " (registry entry kept: " + err.Error() + ")", "warn"
			}
		}
		return notice, kind
	case "template":
		return toggleTemplate(t.vm)
	case "export":
		if path, err := exportVM(t.vm); err != nil {
			fmt.Fprintln(os.Stderr, "export failed:", err)
		} else {
			fmt.Println("Exported to", path)
		}
	case "viewer":
		if err := viewSelected(t.vm.Ref()); err != nil {
			fmt.Fprintln(os.Stderr, "viewer failed:", err)
		}
	case "ssh":
		if err := sshSelected(t.vm); err != nil {
			fmt.Fprintln(os.Stderr, "ssh failed:", err)
		}
	}
	return "", ""
}

// sshSelected opens a shell in the VM through its backend's Sheller, the same
// path `corral ssh` takes, with the credentials the registry remembers.
func sshSelected(vm types.VM) error {
	user, password := "", ""
	if registryStore != nil {
		if entry, ok := registryStore.GetRef(vm.Ref()); ok {
			user, password = entry.Username, entry.Password
		}
	}
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "root"
	}
	adapter, err := backend.For(vm.Ref())
	if err != nil {
		return err
	}
	sheller, ok := adapter.(backend.Sheller)
	if !ok {
		return unsupported(adapter, "ssh")
	}
	return sheller.SSH(vm.Name, backend.SSHOptions{User: user, Port: 22, Password: password})
}

// exportVM writes the VM's disk to the working directory in its backend's
// native export format, through pkg/export — the adapter `corral backup`
// uses.
func exportVM(vm types.VM) (string, error) { return exportRef(vm.Ref(), "") }

// exportRef exports through the backend's export adapter in its native
// format. An empty dest means <name> plus the format's extension.
func exportRef(ref types.InstanceRef, dest string) (string, error) {
	adapter, err := export.For(ref)
	if err != nil {
		return "", err
	}
	formats := adapter.Formats()
	if len(formats) == 0 {
		return "", fmt.Errorf("the %s backend has no export format", ref.Backend)
	}
	if dest == "" {
		dest = ref.Name + exportSuffix(formats[0])
	}
	res, err := adapter.Export(context.Background(), export.Request{Ref: ref, Dest: dest}, nil)
	if err != nil {
		return "", err
	}
	return res.Path, nil
}

// exportSuffix names the file for a format the way the old KubeVirt-only
// export did (<name>.img.gz for a gzip image).
func exportSuffix(f export.Format) string {
	switch f {
	case export.RawGz:
		return ".img.gz"
	case export.IncusTar:
		return ".tar.gz"
	default:
		return "." + string(f)
	}
}

// viewSelected opens the graphical console of the instance through its
// backend's Viewer, the same path `corral viewer` takes.
func viewSelected(ref types.InstanceRef) error {
	adapter, err := backend.For(ref)
	if err != nil {
		return err
	}
	viewer, ok := adapter.(backend.Viewer)
	if !ok {
		return unsupported(adapter, "viewer")
	}
	return viewer.View(ref.Name)
}

// ctAction is run's CT counterpart — a much smaller surface (no backend
// split, no migrate/snapshot/pause) since a CT is always a plain pod, never a
// hypervisor guest. "console" isn't handled here — it's a quit-then-exec
// action (see the actions screen's enter handler), same as ssh/viewer/export,
// since it needs the real terminal back after Bubble Tea releases it.
func ctAction(c ct.CT, action string) {
	name, ns := c.Name, c.Namespace

	switch action {
	case "start":
		_ = ct.Start(name, ns)
	case "stop":
		_ = ct.Stop(name, ns)
	case "delete":
		_ = ct.Delete(name, ns)
	}
}

// newCloneInput sets up the target-name text input for the Clone action,
// pre-filled with a "-clone" suggestion off the source VM's name.
func newCloneInput(sourceName string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "target VM name"
	ti.SetValue(sourceName + "-clone")
	ti.CharLimit = 63
	ti.SetWidth(30)
	return ti
}

// runClone mirrors cmd/clone.go's logic (the backend's Cloner, registers the
// clone in the local registry) so the TUI's Clone action behaves identically to
// `corral clone` — same checks, same errors, just driven by a text input
// instead of positional args.
func runClone(src types.VM, dst string) error {
	adapter, err := backend.For(src.Ref())
	if err != nil {
		return err
	}
	cloner, ok := adapter.(backend.Cloner)
	if !ok {
		return unsupported(adapter, "clone")
	}
	if existing := resolveBackend(dst); existing != "" {
		return fmt.Errorf("target VM %q already exists (backend: %s)", dst, existing)
	}
	if err := cloner.Clone(src.Name, dst); err != nil {
		return err
	}
	if registryStore != nil {
		ref := src.Ref()
		ref.Peer, ref.Name = "", dst
		if err := registryStore.SetRef(ref, types.RegistryEntry{Backend: ref.Backend, Context: ref.Context, Namespace: ref.Namespace}); err != nil {
			return fmt.Errorf("saving registry entry: %w", err)
		}
	}
	return nil
}
