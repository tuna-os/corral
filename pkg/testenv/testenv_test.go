package testenv

import (
	"os"
	"testing"
)

func TestRequired(t *testing.T) {
	cases := []struct {
		env  string
		tool string
		want bool
	}{
		{"", "incus", false},
		{"incus", "incus", true},
		{"incus,virsh", "virsh", true},
		{"incus virsh qemu-img", "qemu-img", true},
		{"incus", "virsh", false},
		{"all", "anything", true},
		{"INCUS", "incus", true}, // an operator typing it in caps means the same tool
		{"  incus  ", "incus", true},
	}
	for _, tc := range cases {
		t.Setenv(RequireEnv, tc.env)
		if got := Required(tc.tool); got != tc.want {
			t.Errorf("Required(%q) with %s=%q = %v, want %v", tc.tool, RequireEnv, tc.env, got, tc.want)
		}
	}
}

// The point of the package: an unlisted tool skips, a listed one fails. The
// failing half is checked through a sub-test so this test can observe it
// without dying itself.
func TestSkip_FailsOnlyForAPromisedTool(t *testing.T) {
	os.Unsetenv(RequireEnv)

	skipped := testing.RunTests(func(string, string) (bool, error) { return true, nil }, []testing.InternalTest{{
		Name: "unpromised",
		F:    func(t *testing.T) { Skip(t, "incus", "not installed") },
	}})
	if !skipped {
		t.Error("a tool the environment never promised should skip, not fail")
	}

	t.Setenv(RequireEnv, "incus")
	passed := testing.RunTests(func(string, string) (bool, error) { return true, nil }, []testing.InternalTest{{
		Name: "promised",
		F:    func(t *testing.T) { Skip(t, "incus", "not installed") },
	}})
	if passed {
		t.Errorf("a tool listed in %s must fail when it is missing, not skip", RequireEnv)
	}
}
