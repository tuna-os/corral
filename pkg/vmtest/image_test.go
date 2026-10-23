package vmtest

import (
	"errors"
	"testing"

	"github.com/tuna-os/corral/pkg/shell"
)

// fakeRunner installs a scriptable runner and restores the real one after.
func fakeRunner(t *testing.T) *shell.Fake {
	t.Helper()
	fake := shell.NewFake()
	SetRunner(fake)
	t.Cleanup(func() { SetRunner(shell.Real{}) })
	return fake
}

// fakeLookPath answers binary lookups for the length of one test.
func fakeLookPath(t *testing.T, found map[string]bool) {
	t.Helper()
	original := lookPath
	t.Cleanup(func() { SetLookPath(original) })
	SetLookPath(func(name string) (string, error) {
		if found[name] {
			return "/fake/bin/" + name, nil
		}
		return "", errors.New("not installed: " + name)
	})
}
