package qemu

import (
	"strings"
	"testing"

	"github.com/tuna-os/corral/pkg/types"
)

func TestValidateVMName(t *testing.T) {
	for _, ok := range []string{"tuna-v1", "gate", "vm1", "a", "A-9_.z", strings.Repeat("n", 64)} {
		if err := ValidateVMName(ok); err != nil {
			t.Errorf("ValidateVMName(%q) = %v, want nil", ok, err)
		}
	}
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "", want: "required"},
		{name: ".", want: "usable name"},
		{name: "..", want: "usable name"},
		{name: "../escape", want: "invalid"},
		{name: "a/b", want: "invalid"},
		{name: "/abs", want: "invalid"},
		{name: `a\b`, want: "invalid"},
		{name: "has space", want: "invalid"},
		{name: "semi;colon", want: "invalid"},
		{name: "quote\"x", want: "invalid"},
		{name: "dollar$x", want: "invalid"},
		{name: strings.Repeat("n", 65), want: "too long"},
		{name: "locks", want: "reserved"},
	} {
		if err := ValidateVMName(tc.name); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ValidateVMName(%q) = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestCreate_RefusesUnsafeName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := Create(types.CreateOpts{Name: "../escape"}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("Create with traversal name = %v, want refusal", err)
	}
	if err := RecordCreating("a/b", types.CreateOpts{Name: "a/b"}); err == nil {
		t.Error("RecordCreating with a slash name must be refused")
	}
}
