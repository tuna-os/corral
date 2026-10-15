//go:build bootc

package main

import (
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/bootc"
)

func createSubcommand(t *testing.T, root *cobra.Command) *cobra.Command {
	t.Helper()
	create, _, err := root.Find([]string{"create"})
	if err != nil || create.Name() != "create" {
		t.Fatalf("corral-bootc has no create command: %v", err)
	}
	return create
}

// tunaOS's boot-gate.sh greps `corral bootc create --help` for --karg.
func TestCreateHelpListsKarg(t *testing.T) {
	create := createSubcommand(t, newRootCmd())
	if !strings.Contains(create.Flags().FlagUsages(), "--karg") {
		t.Errorf("corral bootc create --help must list --karg:\n%s", create.Flags().FlagUsages())
	}
}

// Every --karg reaches the local build's BuildRequest, in order, with a
// comma kept inside its argument.
func TestCreateKargFlagReachesBuildRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CORRAL_DEFAULT_BACKEND", "qemu")
	fb, ft := &fakeBuilder{}, &fakeTarget{}
	oldBuilder, oldTarget := newBuilder, targetFor
	newBuilder = func(bool) bootc.Builder { return fb }
	targetFor = func(string) (bootc.Target, error) { return ft, nil }
	t.Cleanup(func() { newBuilder, targetFor = oldBuilder, oldTarget })

	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"create", "gate", "--image", "quay.io/fedora/fedora-bootc:41",
		"--ssh-key", "ssh-ed25519 AAAAtest", "--karg", "tunaos.countme=0", "--karg", "console=ttyS0,115200n8"})
	if err := root.Execute(); err != nil {
		t.Fatalf("create: %v", err)
	}
	if want := []string{"tunaos.countme=0", "console=ttyS0,115200n8"}; !slices.Equal(fb.reqSeen.Kargs, want) {
		t.Errorf("BuildRequest.Kargs = %q, want %q", fb.reqSeen.Kargs, want)
	}
	if ft.imports != 1 {
		t.Errorf("imports = %d, want 1", ft.imports)
	}
}

// A karg that is not one token fails before any build or cluster call.
func TestCreateRejectsBadKarg(t *testing.T) {
	fb := &fakeBuilder{}
	oldBuilder := newBuilder
	newBuilder = func(bool) bootc.Builder { return fb }
	t.Cleanup(func() { newBuilder = oldBuilder })

	root := newRootCmd()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"create", "gate", "--image", "quay.io/fedora/fedora-bootc:41", "--karg", "two words"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "not one kernel argument") {
		t.Fatalf("err = %v, want a kernel-argument error", err)
	}
	if fb.reqSeen.Image != "" {
		t.Error("the build ran despite an invalid --karg")
	}
}

// --resume reuses a finished disk, so it accepts --karg only when that
// build installed it. A build from before kargs were recorded counts as
// having installed none.
func TestCheckResumeKargs(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		requested, installed []string
		refuse               string
	}{
		{"no kargs requested", nil, nil, ""},
		{"no kargs requested, some installed", nil, []string{"tunaos.countme=0"}, ""},
		{"all installed", []string{"tunaos.countme=0"}, []string{"console=ttyS0", "tunaos.countme=0"}, ""},
		{"build installed another", []string{"tunaos.countme=0"}, []string{"tunaos.countme=1"}, "--karg tunaos.countme=0"},
		{"build from before the record", []string{"tunaos.countme=0"}, nil, "--karg tunaos.countme=0"},
		{"one of two missing", []string{"a=1", "b=2"}, []string{"a=1"}, "--karg b=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkResumeKargs("gate", tc.requested, tc.installed)
			if tc.refuse == "" {
				if err != nil {
					t.Fatalf("checkResumeKargs = %v, want accept", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refuse) || !strings.Contains(err.Error(), "rerun without --resume") {
				t.Fatalf("checkResumeKargs = %v, want a refusal naming %q", err, tc.refuse)
			}
		})
	}
}
