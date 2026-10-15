package cmd

import (
	"testing"
)

func TestTUIModeString(t *testing.T) {
	tests := []struct {
		mode     TUIMode
		expected string
	}{
		{ModeList, "list"},
		{ModeActions, "actions"},
		{ModeEdit, "edit"},
		{ModeHWEdit, "hwedit"},
		{ModeConfirmDelete, "confirmDelete"},
		{ModeDoctor, "doctor"},
		{ModeCloneInput, "cloneInput"},
		{ModeSnapshots, "snapshots"},
		{ModeEvents, "events"},
		{ModePools, "pools"},
		{ModePlugins, "plugins"},
		{ModeCreate, "create"},
		{modeCount, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.expected {
				t.Errorf("String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from     TUIMode
		to       TUIMode
		expected bool
	}{
		{ModeList, ModeActions, true},
		{ModeList, ModeDoctor, true},
		{ModeActions, ModeList, true},
		{ModeActions, ModeEdit, true},
		{ModeEdit, ModeList, true},
		{ModeConfirmDelete, ModeActions, true},
		{ModeSnapshots, ModeActions, true},

		// The pool screen has no list key, and the forms that close to the
		// list never go back to the menu.
		{ModeList, ModePools, false},
		{ModeEdit, ModeActions, false},
		{ModeHWEdit, ModeActions, false},
		{ModePools, ModeActions, false},
		{ModeDoctor, ModeActions, false},
		{ModeEdit, ModeEdit, false},
	}

	for _, tt := range tests {
		t.Run(tt.from.String()+"->"+tt.to.String(), func(t *testing.T) {
			got := CanTransition(tt.from, tt.to)
			if got != tt.expected {
				t.Errorf("CanTransition(%v, %v) = %v, want %v", tt.from, tt.to, got, tt.expected)
			}
		})
	}
}

// Every screen but the list is entered from somewhere and left to somewhere,
// so no mode is a dead end or unreachable.
func TestValidTransitionsReachEveryMode(t *testing.T) {
	in, out := map[TUIMode]bool{}, map[TUIMode]bool{}
	for _, trans := range ValidTransitions {
		out[trans.From] = true
		in[trans.To] = true
	}
	for mode := ModeList; mode < modeCount; mode++ {
		if !in[mode] {
			t.Errorf("mode %v is never entered", mode)
		}
		if !out[mode] {
			t.Errorf("mode %v is never left", mode)
		}
	}
}

func TestTUIScreens_EveryModeIsWired(t *testing.T) {
	for mode := ModeList; mode < modeCount; mode++ {
		s := screens[mode]
		if s.update == nil || s.view == nil {
			t.Errorf("mode %v has no screen (update %v, view %v)", mode, s.update != nil, s.view != nil)
		}
	}
}
