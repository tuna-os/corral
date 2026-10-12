package main

// Cobra arg-parse/error paths for the incus plugin. Every subcommand shells
// out to the Incus daemon, so unit tests stay on the paths that fail before
// any RunE body (and daemon call) runs. main() builds its command tree
// inline with no exported constructor, so these tests drive the real binary
// as a subprocess with invocations that must fail (or print help) purely in
// cobra's parser — no daemon needed.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var incusBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "corral-incus-test")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "corral-incus")
	if err := exec.Command("go", "build", "-o", bin, ".").Run(); err != nil {
		panic("building corral-incus for tests: " + err.Error())
	}
	incusBin = bin
	os.Exit(m.Run())
}

func runIncus(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(incusBin, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("running %v: %v", args, err)
		}
	}
	return code, string(out)
}

func TestIncusArgErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"create needs a name", []string{"create"}, "accepts 1 arg(s), received 0"},
		{"create rejects extra args", []string{"create", "a", "b"}, "accepts 1 arg(s), received 2"},
		{"start needs a name", []string{"start"}, "accepts 1 arg(s), received 0"},
		{"stop needs a name", []string{"stop"}, "accepts 1 arg(s), received 0"},
		{"delete needs a name", []string{"delete"}, "accepts 1 arg(s), received 0"},
		{"info needs a name", []string{"info"}, "accepts 1 arg(s), received 0"},
		{"create rejects unknown flag", []string{"create", "x", "--bogus-flag"}, `unknown flag: --bogus-flag`},
		{"unknown command", []string{"frobnicate"}, `unknown command "frobnicate"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runIncus(t, tc.args...)
			if code == 0 {
				t.Fatalf("exit 0, want nonzero; output:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output missing %q:\n%s", tc.want, out)
			}
		})
	}
}

func TestIncusHelpExitsZero(t *testing.T) {
	for _, args := range [][]string{{}, {"--help"}, {"create", "--help"}} {
		code, out := runIncus(t, args...)
		if code != 0 {
			t.Errorf("args %v: exit %d, want 0; output:\n%s", args, code, out)
		}
	}
}
