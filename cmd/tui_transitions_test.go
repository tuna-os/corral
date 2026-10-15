package cmd

// Characterization tests for the TUI's screen state machine (#305): every
// transition the root model makes, driven through tuiModel.Update one key at a
// time against the in-memory demo cluster. They pin today's behaviour, quirks
// included (the ports and hardware editors close to the list, the pool screens
// close to the list, the snapshots and events views close to the menu), so the
// state machine can be restructured without a flow quietly moving.

import (
	"testing"

	"github.com/tuna-os/corral/pkg/folder"
)

// screenOf is the screen the model is showing, as a TUIMode.
func screenOf(m tuiModel) TUIMode { return FromString(m.state) }

// chooseAction moves the actions-menu cursor onto the action id. It fails the
// test when the menu does not offer that action, so gating changes surface here
// instead of looping forever.
func chooseAction(t *testing.T, m tuiModel, id string) tuiModel {
	t.Helper()
	for range m.actionsList.Items() {
		if item, ok := m.actionsList.SelectedItem().(actionItem); ok && item.id == id {
			return m
		}
		m = press(m, "down")
	}
	t.Fatalf("actions menu does not offer %q (has %v)", id, actionIDs(m))
	return m
}

// step is one input and the screen it must leave the model on. A key that
// starts with "@" picks that action from the menu and presses enter.
type step struct {
	key  string
	want TUIMode
	quit bool
}

func runSteps(t *testing.T, m tuiModel, steps []step) tuiModel {
	t.Helper()
	for i, s := range steps {
		from := screenOf(m)
		if len(s.key) > 1 && s.key[0] == '@' {
			m = chooseAction(t, m, s.key[1:])
			m = press(m, "enter")
		} else {
			m = press(m, s.key)
		}
		if s.quit {
			if !m.quitting {
				t.Fatalf("step %d: %q on %v did not quit", i, s.key, from)
			}
			return m
		}
		if m.quitting {
			t.Fatalf("step %d: %q on %v quit, want %v", i, s.key, from, s.want)
		}
		if got := screenOf(m); got != s.want {
			t.Fatalf("step %d: %q on %v went to %v, want %v", i, s.key, from, got, s.want)
		}
	}
	return m
}

func TestTUITransitions(t *testing.T) {
	onVM := func(name string) func(*testing.T) tuiModel {
		return func(t *testing.T) tuiModel { return selectVM(t, demoModel(t), name) }
	}
	onCT := func(name string) func(*testing.T) tuiModel {
		return func(t *testing.T) tuiModel { return selectCT(t, demoModel(t), name) }
	}
	quit := step{quit: true}
	q := func(k string) step { s := quit; s.key = k; return s }

	cases := []struct {
		name  string
		setup func(*testing.T) tuiModel
		steps []step
	}{
		// ── the fleet list ──
		{"list stays on list keys", onVM("web-prod"), []step{
			{key: "r", want: ModeList}, {key: "tab", want: ModeList}, {key: "shift+tab", want: ModeList},
			{key: "j", want: ModeList}, {key: "k", want: ModeList},
		}},
		{"list quick start", onVM("dev-fedora"), []step{{key: "s", want: ModeList}}},
		{"list quick stop", onVM("web-prod"), []step{{key: "x", want: ModeList}}},
		{"list help overlay keeps the list", onVM("web-prod"), []step{{key: "?", want: ModeList}, {key: "enter", want: ModeList}}},
		{"list q quits", onVM("web-prod"), []step{q("q")}},
		{"list ctrl+c quits", onVM("web-prod"), []step{q("ctrl+c")}},

		// ── list → actions ──
		{"VM actions and back", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "esc", want: ModeList}}},
		{"CT actions and back", onCT("dev-shell"), []step{{key: "enter", want: ModeActions}, {key: "esc", want: ModeList}}},
		{"actions menu moves", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "down", want: ModeActions}, {key: "up", want: ModeActions}}},
		{"actions q quits", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, q("q")}},
		{"actions ctrl+c quits", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, q("ctrl+c")}},

		// ── list → create ──
		{"create esc", onVM("web-prod"), []step{{key: "c", want: ModeCreate}, {key: "down", want: ModeCreate}, {key: "esc", want: ModeList}}},
		{"create ctrl+c quits", onVM("web-prod"), []step{{key: "c", want: ModeCreate}, q("ctrl+c")}},
		// up from the first row wraps to submit; an empty name keeps the form.
		{"create refuses an empty name", onVM("web-prod"), []step{{key: "c", want: ModeCreate}, {key: "up", want: ModeCreate}, {key: "enter", want: ModeCreate}}},

		// ── list → plugins ──
		{"plugins esc", onVM("web-prod"), []step{{key: "p", want: ModePlugins}, {key: "esc", want: ModeList}}},
		{"plugins q", onVM("web-prod"), []step{{key: "p", want: ModePlugins}, {key: "q", want: ModeList}}},
		{"plugins ctrl+c quits", onVM("web-prod"), []step{{key: "p", want: ModePlugins}, q("ctrl+c")}},

		// ── list → doctor ──
		{"doctor fix stays, esc leaves", onVM("web-prod"), []step{{key: "d", want: ModeDoctor}, {key: "f", want: ModeDoctor}, {key: "esc", want: ModeList}}},
		{"doctor q", onVM("web-prod"), []step{{key: "d", want: ModeDoctor}, {key: "q", want: ModeList}}},
		{"doctor enter", onVM("web-prod"), []step{{key: "d", want: ModeDoctor}, {key: "enter", want: ModeList}}},
		{"doctor ctrl+c quits", onVM("web-prod"), []step{{key: "d", want: ModeDoctor}, q("ctrl+c")}},

		// ── actions → ports editor ──
		{"ports esc closes to the list", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@ports", want: ModeEdit}, {key: "down", want: ModeEdit}, {key: "esc", want: ModeList}}},
		{"ports q closes to the list", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@ports", want: ModeEdit}, {key: "q", want: ModeList}}},

		// ── actions → hardware editor ──
		{"VM hardware esc closes to the list", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@hardware", want: ModeHWEdit}, {key: "tab", want: ModeHWEdit}, {key: "esc", want: ModeList}}},
		{"VM hardware enter applies and closes", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@hardware", want: ModeHWEdit}, {key: "enter", want: ModeList}}},
		{"CT hardware esc closes to the list", onCT("dev-shell"), []step{{key: "enter", want: ModeActions}, {key: "@hardware", want: ModeHWEdit}, {key: "esc", want: ModeList}}},

		// ── actions → clone ──
		{"clone esc returns to the menu", onVM("golden-ubuntu"), []step{{key: "enter", want: ModeActions}, {key: "@clone", want: ModeCloneInput}, {key: "x", want: ModeCloneInput}, {key: "esc", want: ModeActions}}},
		{"clone succeeds to the list", onVM("golden-ubuntu"), []step{{key: "enter", want: ModeActions}, {key: "@clone", want: ModeCloneInput}, {key: "enter", want: ModeList}}},
		{"clone of an existing name stays", onVM("golden-ubuntu"), []step{
			{key: "enter", want: ModeActions}, {key: "@clone", want: ModeCloneInput},
			{key: "backspace", want: ModeCloneInput}, {key: "backspace", want: ModeCloneInput}, {key: "backspace", want: ModeCloneInput},
			{key: "backspace", want: ModeCloneInput}, {key: "backspace", want: ModeCloneInput}, {key: "backspace", want: ModeCloneInput},
			{key: "enter", want: ModeCloneInput},
		}},
		{"clone ctrl+c quits", onVM("golden-ubuntu"), []step{{key: "enter", want: ModeActions}, {key: "@clone", want: ModeCloneInput}, q("ctrl+c")}},

		// ── actions → snapshots ──
		{"snapshots esc returns to the menu", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@snapshot", want: ModeSnapshots}, {key: "j", want: ModeSnapshots}, {key: "esc", want: ModeActions}}},
		{"snapshots q returns to the menu", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@snapshot", want: ModeSnapshots}, {key: "q", want: ModeActions}}},
		{"snapshots naming esc stays", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@snapshot", want: ModeSnapshots}, {key: "n", want: ModeSnapshots}, {key: "esc", want: ModeSnapshots}}},
		{"snapshots ctrl+c quits", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@snapshot", want: ModeSnapshots}, q("ctrl+c")}},

		// ── actions → events ──
		{"events refresh stays, esc returns", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@events", want: ModeEvents}, {key: "r", want: ModeEvents}, {key: "esc", want: ModeActions}}},
		{"events q returns", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@events", want: ModeEvents}, {key: "q", want: ModeActions}}},
		{"events enter returns", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@events", want: ModeEvents}, {key: "enter", want: ModeActions}}},
		{"events ctrl+c quits", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@events", want: ModeEvents}, q("ctrl+c")}},

		// ── actions → pools ──
		{"pool esc closes to the list", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@pool", want: ModePools}, {key: "j", want: ModePools}, {key: "esc", want: ModeList}}},
		{"pool pick assigns and closes", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@pool", want: ModePools}, {key: "j", want: ModePools}, {key: "enter", want: ModeList}}},
		{"pool ctrl+c quits", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@pool", want: ModePools}, q("ctrl+c")}},
		{"move q closes to the list", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@move", want: ModePools}, {key: "q", want: ModeList}}},

		// ── actions that run and return to the list ──
		{"template", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@template", want: ModeList}}},
		{"start", onVM("dev-fedora"), []step{{key: "enter", want: ModeActions}, {key: "@start", want: ModeList}}},
		{"stop", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@stop", want: ModeList}}},
		{"restart", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@restart", want: ModeList}}},
		{"pause", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, {key: "@pause", want: ModeList}}},
		{"unpause", onVM("win11-desktop"), []step{{key: "enter", want: ModeActions}, {key: "@unpause", want: ModeList}}},
		{"CT start", onCT("files"), []step{{key: "enter", want: ModeActions}, {key: "@start", want: ModeList}}},
		{"CT stop", onCT("dev-shell"), []step{{key: "enter", want: ModeActions}, {key: "@stop", want: ModeList}}},

		// ── actions → delete confirmation ──
		{"delete cancel returns to the menu", onVM("scratch"), []step{{key: "enter", want: ModeActions}, {key: "@delete", want: ModeConfirmDelete}, {key: "n", want: ModeActions}}},
		{"delete confirm closes to the list", onVM("scratch"), []step{{key: "enter", want: ModeActions}, {key: "@delete", want: ModeConfirmDelete}, {key: "y", want: ModeList}}},
		{"CT delete confirm closes to the list", onCT("files"), []step{{key: "enter", want: ModeActions}, {key: "@delete", want: ModeConfirmDelete}, {key: "Y", want: ModeList}}},
		{"delete ctrl+c quits", onVM("scratch"), []step{{key: "enter", want: ModeActions}, {key: "@delete", want: ModeConfirmDelete}, q("ctrl+c")}},

		// ── actions that hand the terminal over ──
		{"ssh quits first", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, q("@ssh")}},
		{"viewer quits first", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, q("@viewer")}},
		{"export quits first", onVM("web-prod"), []step{{key: "enter", want: ModeActions}, q("@export")}},
		{"CT console quits first", onCT("dev-shell"), []step{{key: "enter", want: ModeActions}, q("@console")}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scratchPools(t, folder.Folder{Path: "/prod"})
			postQuitAction = nil
			t.Cleanup(func() { postQuitAction = nil })
			runSteps(t, tc.setup(t), tc.steps)
		})
	}
}
