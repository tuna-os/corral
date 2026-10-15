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
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.expected {
				t.Errorf("String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFromString(t *testing.T) {
	tests := []struct {
		input    string
		expected TUIMode
	}{
		{"list", ModeList},
		{"actions", ModeActions},
		{"edit", ModeEdit},
		{"hwedit", ModeHWEdit},
		{"confirmDelete", ModeConfirmDelete},
		{"doctor", ModeDoctor},
		{"cloneInput", ModeCloneInput},
		{"snapshots", ModeSnapshots},
		{"events", ModeEvents},
		{"pools", ModePools},
		{"plugins", ModePlugins},
		{"create", ModeCreate},
		{"unknown", ModeList}, // fallback case
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := FromString(tt.input)
			if got != tt.expected {
				t.Errorf("FromString(%q) = %v, want %v", tt.input, got, tt.expected)
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
		// Valid transitions
		{ModeList, ModeActions, true},
		{ModeList, ModeDoctor, true},
		{ModeList, ModePools, true},
		{ModeActions, ModeList, true},
		{ModeActions, ModeEdit, true},
		{ModeEdit, ModeActions, true},

		// Invalid transitions
		{ModeEdit, ModeEdit, false},
		{ModeConfirmDelete, ModeActions, false},
		{ModeDoctor, ModeActions, false},
		{ModePools, ModeActions, false},
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

func TestValidTransitionsComplete(t *testing.T) {
	// Verify that all defined modes have at least one valid transition
	// (except for terminal modes that are expected to not transition).
	modesWithTransitions := make(map[TUIMode]bool)
	for _, trans := range ValidTransitions {
		modesWithTransitions[trans.From] = true
		modesWithTransitions[trans.To] = true
	}

	// Check that key modes have transitions
	expectedModes := []TUIMode{
		ModeList,
		ModeActions,
		ModeEdit,
		ModeHWEdit,
		ModeConfirmDelete,
		ModeDoctor,
		ModeCloneInput,
		ModeSnapshots,
		ModeEvents,
		ModePools,
		ModePlugins,
		ModeCreate,
	}

	for _, mode := range expectedModes {
		if !modesWithTransitions[mode] {
			t.Errorf("mode %v has no transitions defined", mode)
		}
	}
}
