package kubevirt

import (
	"errors"
	"slices"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

func TestValidateKargs(t *testing.T) {
	ok := [][]string{nil, {"tunaos.countme=0"}, {"console=ttyS0,115200n8", "quiet", `x="a"`}}
	for _, kargs := range ok {
		if err := ValidateKargs(kargs); err != nil {
			t.Errorf("ValidateKargs(%q) = %v, want nil", kargs, err)
		}
	}
	bad := [][]string{{""}, {"two words"}, {"a=1", "tab\there"}, {"nl\n"}, {" lead"}}
	for _, kargs := range bad {
		if err := ValidateKargs(kargs); err == nil {
			t.Errorf("ValidateKargs(%q) = nil, want an error", kargs)
		}
	}
}

func TestBootcKargFlags(t *testing.T) {
	if got := BootcKargFlags(nil); got != "" {
		t.Errorf("BootcKargFlags(nil) = %q, want empty", got)
	}
	got := BootcKargFlags([]string{"tunaos.countme=0", "console=ttyS0,115200n8", "it's=$HOME"})
	want := ` --karg 'tunaos.countme=0' --karg 'console=ttyS0,115200n8' --karg 'it'\''s=$HOME'`
	if got != want {
		t.Errorf("BootcKargFlags = %s, want %s", got, want)
	}
}

func TestMissingKargs(t *testing.T) {
	for _, tc := range []struct {
		requested, installed, want []string
	}{
		{nil, nil, nil},
		{[]string{"a=1"}, []string{"a=1", "b"}, nil},
		{[]string{"a=1", "b"}, []string{"b"}, []string{"a=1"}},
		{[]string{"a=1"}, []string{"a=2"}, []string{"a=1"}},
		{[]string{"tunaos.countme=0"}, nil, []string{"tunaos.countme=0"}},
	} {
		if got := MissingKargs(tc.requested, tc.installed); !slices.Equal(got, tc.want) {
			t.Errorf("MissingKargs(%q, %q) = %q, want %q", tc.requested, tc.installed, got, tc.want)
		}
	}
}

func TestBootcBuiltKargs(t *testing.T) {
	args := []string{"get", "vm", "gate-bootc-builder", "-n", "ci",
		"-o", `jsonpath={.metadata.annotations.corral\.bootc/kargs}`}
	for _, tc := range []struct {
		name   string
		stdout string
		err    error
		want   []string
	}{
		{"recorded", "tunaos.countme=0 console=ttyS0,115200n8", nil, []string{"tunaos.countme=0", "console=ttyS0,115200n8"}},
		{"build from before the record", "", nil, nil},
		{"no builder", "", errors.New("NotFound"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := shell.NewFake()
			fake.AddResponseKV("kubectl", args, tc.stdout, tc.err)
			SetPackageRunner(fake)
			t.Cleanup(func() { SetPackageRunner(shell.DefaultKubectl) })
			if got := BootcBuiltKargs("gate", "ci"); !slices.Equal(got, tc.want) {
				t.Errorf("BootcBuiltKargs = %q, want %q", got, tc.want)
			}
		})
	}
}
