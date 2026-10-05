package cmd

// TUIMode represents the current screen/state of the terminal UI.
//
// This enum replaces the previous string-based state field in tuiModel,
// providing compile-time exhaustiveness checking and clearer state transitions.
// See corral#305 for the refactoring roadmap.
type TUIMode int

const (
	// ModeList is the main VM/CT listing and navigation screen.
	ModeList TUIMode = iota

	// ModeActions shows the context menu for a selected VM/CT.
	ModeActions

	// ModeEdit is the basic VM property editor.
	ModeEdit

	// ModeHWEdit is the hardware editor for CPU, memory, disks.
	ModeHWEdit

	// ModeConfirmDelete is the delete confirmation dialog.
	ModeConfirmDelete

	// ModeDoctor runs the diagnostics/doctor screen.
	ModeDoctor

	// ModeCloneInput is the clone name input field.
	ModeCloneInput

	// ModeSnapshots shows the per-VM snapshots list (formerly a tab).
	ModeSnapshots

	// ModeEvents shows the per-VM events log (formerly a tab).
	ModeEvents

	// ModePools shows the resource pool manager.
	ModePools

	// ModePlugins is the plugin creation/configuration flow.
	ModePlugins

	// ModeCreate is the VM creation flow.
	ModeCreate
)

// String returns the string representation of the mode.
// This is used for comparison with existing string-based logic during migration.
// Phase 2 will replace all string comparisons with enum checks.
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

// FromString converts a string (from existing code) to a TUIMode.
// This is a temporary bridge during migration. Phase 2 will remove all string usage.
func FromString(s string) TUIMode {
	switch s {
	case "list":
		return ModeList
	case "actions":
		return ModeActions
	case "edit":
		return ModeEdit
	case "hwedit":
		return ModeHWEdit
	case "confirmDelete":
		return ModeConfirmDelete
	case "doctor":
		return ModeDoctor
	case "cloneInput":
		return ModeCloneInput
	case "snapshots":
		return ModeSnapshots
	case "events":
		return ModeEvents
	case "pools":
		return ModePools
	case "plugins":
		return ModePlugins
	case "create":
		return ModeCreate
	default:
		return ModeList // fallback to safe default
	}
}

// Transition represents a valid mode transition.
// This table test structure allows exhaustive checking of state machine logic.
// See tests/tui_transitions_test.go for the comprehensive table-driven test.
type Transition struct {
	From TUIMode
	To   TUIMode
	When string // brief description of the condition
}

// ValidTransitions is the allowlist of legal mode changes.
// Any transition not listed here should either:
//   1. Be added here if it's a legitimate flow
//   2. Be removed from the code if it's a leftover bug
//
// Phase 2 will enforce this with a transition guard in tuiModel.setMode().
var ValidTransitions = []Transition{
	// Main navigation
	{ModeList, ModeActions, "user selects a VM/CT"},
	{ModeList, ModeDoctor, "user runs diagnostics"},
	{ModeList, ModePools, "user opens pool manager"},
	{ModeList, ModePlugins, "user opens plugin menu"},
	{ModeList, ModeCreate, "user starts VM creation"},

	// Actions menu exits
	{ModeActions, ModeList, "user presses Escape or Back"},
	{ModeActions, ModeEdit, "user selects Edit"},
	{ModeActions, ModeHWEdit, "user selects Edit Hardware"},
	{ModeActions, ModeConfirmDelete, "user selects Delete"},
	{ModeActions, ModeCloneInput, "user selects Clone"},
	{ModeActions, ModeSnapshots, "user selects Snapshots"},
	{ModeActions, ModeEvents, "user selects Events"},

	// Editors exit to actions or list
	{ModeEdit, ModeActions, "user saves or cancels edit"},
	{ModeHWEdit, ModeActions, "user saves or cancels hardware edit"},
	{ModeConfirmDelete, ModeList, "user confirms or cancels delete"},
	{ModeCloneInput, ModeList, "user confirms or cancels clone"},

	// Tabs return to actions
	{ModeSnapshots, ModeActions, "user closes snapshots tab"},
	{ModeEvents, ModeActions, "user closes events tab"},

	// Doctor and pools return to list
	{ModeDoctor, ModeList, "user closes doctor screen"},
	{ModePools, ModeList, "user closes pool manager"},

	// Plugin and create flows
	{ModePlugins, ModeList, "user exits plugin menu"},
	{ModeCreate, ModeList, "user completes or cancels creation"},
}

// CanTransition checks if a transition from→to is valid.
// Phase 2 will be called from tuiModel.setMode() to prevent invalid state changes.
func CanTransition(from, to TUIMode) bool {
	for _, t := range ValidTransitions {
		if t.From == from && t.To == to {
			return true
		}
	}
	return false
}
