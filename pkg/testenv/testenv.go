// Package testenv makes a skipped integration test visible.
//
// Corral's integration tests skip when the tool or daemon they drive is
// absent, which is right on a laptop and wrong in CI: a runner that stops
// installing `incus`, or an image that drops `virsh`, turns a suite green by
// running nothing at all, and nobody notices for months.
//
// So the decision is moved out of the test. CORRAL_REQUIRE_TOOLS lists what
// the environment promises to provide; a test that would skip for something on
// that list fails instead. CI sets it to exactly what its setup step
// installed, so the promise and the check cannot drift apart.
package testenv

import (
	"os"
	"strings"
	"testing"
)

// RequireEnv is the environment variable naming the tools that must be
// present. Comma- or space-separated; "all" means every requirement is
// mandatory.
const RequireEnv = "CORRAL_REQUIRE_TOOLS"

// Skip reports that a test cannot run because tool is unavailable.
//
// It skips, unless the environment promised that tool through
// CORRAL_REQUIRE_TOOLS — then it fails, because a promised tool that is
// missing is a broken environment, not a test that does not apply here.
//
// The tool name is what an operator would install ("incus", "virsh",
// "qemu-img"), not the name of whatever probe failed: a daemon that will not
// answer and a binary that is not there are the same absence to a CI setup
// step.
func Skip(t *testing.T, tool, reason string) {
	t.Helper()
	if required(tool) {
		t.Fatalf("%s: %s — this environment declared %s in %s, so it must be there",
			tool, reason, tool, RequireEnv)
	}
	t.Skipf("%s: %s", tool, reason)
}

// Required reports whether the environment promised this tool. Use it when a
// test needs to decide something other than skip-or-fail.
func Required(tool string) bool { return required(tool) }

func required(tool string) bool {
	raw := strings.TrimSpace(os.Getenv(RequireEnv))
	if raw == "" {
		return false
	}
	for _, want := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '\n' || r == '\t'
	}) {
		if want == "all" || strings.EqualFold(want, tool) {
			return true
		}
	}
	return false
}
