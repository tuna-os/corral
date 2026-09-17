package plugin

import "testing"

// The version CI stamps has to be a version.
//
// `publish-plugins` tags every push to main as `plugins-<sha>`, and those tags
// came to outnumber the release tags 106 to 9. A bare `git describe --tags`
// therefore always landed on one, and the release job stamped
// "plugins-45109e27...+2.gb48539b" into every published binary.
//
// That is not a semver, so Compatible() takes its unparseable-version escape
// hatch and returns nil — which means every plugin's minimum-corral constraint
// was silently not enforced on exactly the builds users install. The failure
// mode is the dangerous one: not an error, but a check that quietly stops
// checking. `--match 'v*'` in the release job is what keeps this true.
func TestCompatible_StampedReleaseVersionIsEnforceable(t *testing.T) {
	old := CurrentVersion
	defer func() { CurrentVersion = old }()

	// A plugin that the running build does NOT satisfy, so "no error" can only
	// mean the check was skipped.
	e := &Entry{Name: "bootc", Version: "0.2.0", Corral: ">=9.0.0"}

	// What `git describe --tags --match 'v*'` produces, through the job's
	// sed: a release tag plus commit-count build metadata.
	CurrentVersion = "v0.6.0+102.gc73785b"
	if err := e.Compatible(); err == nil {
		t.Error("a stamped release version must be parsed and gated, but the constraint was skipped")
	}

	// And it must compare as its release version, not as something else:
	// build metadata is ignored by semver precedence.
	ok := &Entry{Name: "bootc", Version: "0.2.0", Corral: ">=0.6.0"}
	if err := ok.Compatible(); err != nil {
		t.Errorf("v0.6.0+102.gc73785b should satisfy >=0.6.0, got %v", err)
	}

	// The regression itself: a plugins-<sha> stamp parses as nothing, so the
	// constraint above silently passes.
	CurrentVersion = "plugins-45109e27d153f50cd5d3638c53a199a94c421bcd+2.gb48539b"
	if err := e.Compatible(); err != nil {
		t.Skip("semver now parses a plugins-<sha> stamp; the --match guard is still required")
	}
	t.Log("confirmed: a plugins-<sha> stamp disables constraint checking entirely")
}
