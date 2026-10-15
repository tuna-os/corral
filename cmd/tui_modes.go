package cmd

// TUIMode is the screen the terminal UI is showing. Each mode has a key
// handler and a view in the screens table (cmd/tui_screens.go).
type TUIMode int

const (
	// ModeList is the main VM/CT listing and navigation screen.
	ModeList TUIMode = iota

	// ModeActions shows the context menu for a selected VM/CT.
	ModeActions

	// ModeEdit is the port-forward editor.
	ModeEdit

	// ModeHWEdit is the CPU and memory editor.
	ModeHWEdit

	// ModeConfirmDelete is the delete confirmation dialog.
	ModeConfirmDelete

	// ModeDoctor shows the fleet health checks.
	ModeDoctor

	// ModeCloneInput is the clone name input field.
	ModeCloneInput

	// ModeSnapshots shows the per-VM snapshots list (the web UI's tab).
	ModeSnapshots

	// ModeEvents shows the per-VM events log (the web UI's tab).
	ModeEvents

	// ModePools is the pool assignment or move-destination picker.
	ModePools

	// ModePlugins is the extensions manager.
	ModePlugins

	// ModeCreate is the VM creation form.
	ModeCreate

	// modeCount is the number of modes; it sizes the screens table.
	modeCount
)

// String names the mode, for test failures and debugging.
func (m TUIMode) String() string {
	switch m {
	case ModeList:
		return "list"
	case ModeActions:
		return "actions"
	case ModeEdit:
		return "edit"
	case ModeHWEdit:
		return "hwedit"
	case ModeConfirmDelete:
		return "confirmDelete"
	case ModeDoctor:
		return "doctor"
	case ModeCloneInput:
		return "cloneInput"
	case ModeSnapshots:
		return "snapshots"
	case ModeEvents:
		return "events"
	case ModePools:
		return "pools"
	case ModePlugins:
		return "plugins"
	case ModeCreate:
		return "create"
	default:
		return "unknown"
	}
}

// Transition is one legal change of screen.
type Transition struct {
	From TUIMode
	To   TUIMode
	When string // what the operator does to cause it
}

// ValidTransitions lists every screen change the TUI makes. Quitting is not a
// screen and is not listed. TestTUITransitions drives each entry through
// Update and fails when Update makes a change that is not listed here, or
// when an entry here is never exercised.
var ValidTransitions = []Transition{
	// From the fleet list.
	{ModeList, ModeActions, "enter or a double click on a VM/CT row"},
	{ModeList, ModeCreate, "c"},
	{ModeList, ModePlugins, "p"},
	{ModeList, ModeDoctor, "d"},

	// From the actions menu.
	{ModeActions, ModeList, "esc, or an action that runs in place (power, template)"},
	{ModeActions, ModeEdit, "Ports"},
	{ModeActions, ModeHWEdit, "Edit CPU / RAM"},
	{ModeActions, ModeCloneInput, "Clone"},
	{ModeActions, ModeSnapshots, "Snapshots"},
	{ModeActions, ModeEvents, "Events"},
	{ModeActions, ModePools, "Pool or Move"},
	{ModeActions, ModeConfirmDelete, "Delete"},

	// Forms that close to the list.
	{ModeEdit, ModeList, "esc or q closes the ports editor"},
	{ModeHWEdit, ModeList, "enter applies, esc or q cancels"},
	{ModePools, ModeList, "a pool is chosen, a move is confirmed, or esc"},
	{ModeDoctor, ModeList, "esc, q, or enter"},
	{ModePlugins, ModeList, "esc or q"},
	{ModeCreate, ModeList, "the VM is created, or esc"},

	// Screens that return to the menu they were opened from.
	{ModeCloneInput, ModeActions, "esc"},
	{ModeCloneInput, ModeList, "the clone succeeds"},
	{ModeConfirmDelete, ModeActions, "any key but y"},
	{ModeConfirmDelete, ModeList, "y deletes"},
	{ModeSnapshots, ModeActions, "esc or q"},
	{ModeEvents, ModeActions, "esc, q, or enter"},
}

// CanTransition reports whether from→to is in ValidTransitions.
func CanTransition(from, to TUIMode) bool {
	for _, t := range ValidTransitions {
		if t.From == from && t.To == to {
			return true
		}
	}
	return false
}
