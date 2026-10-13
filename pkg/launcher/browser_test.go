package launcher

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBrowserCommand(t *testing.T) {
	if cmd, err := BrowserCommand("linux"); err != nil || cmd != "xdg-open" {
		t.Errorf("linux = %q, %v", cmd, err)
	}
	if cmd, err := BrowserCommand("darwin"); err != nil || cmd != "open" {
		t.Errorf("darwin = %q, %v", cmd, err)
	}
	if _, err := BrowserCommand("windows"); err == nil {
		t.Error("windows must be refused")
	}
}

// pinOpener stubs the opener lookup and runner so the wiring is pinned
// without depending on the CI image's PATH.
func pinOpener(t *testing.T, lookPath func(string) (string, error), runner func(name string, args ...string) error) {
	t.Helper()
	origLook, origRunner := browserLookPath, browserRunner
	browserLookPath, browserRunner = lookPath, runner
	t.Cleanup(func() { browserLookPath, browserRunner = origLook, origRunner })
}

func TestOpenBrowserOn_UsesRunner(t *testing.T) {
	var gotName, gotArg string
	pinOpener(t,
		func(cmd string) (string, error) { return "/usr/bin/" + cmd, nil },
		func(name string, args ...string) error { gotName, gotArg = name, args[0]; return nil },
	)
	if err := OpenBrowserOn("linux", "http://127.0.0.1:8006/?console=x"); err != nil {
		t.Fatalf("OpenBrowserOn: %v", err)
	}
	if gotName != "xdg-open" || gotArg != "http://127.0.0.1:8006/?console=x" {
		t.Errorf("runner got %q %q", gotName, gotArg)
	}
}

func TestOpenBrowserOn_FailureNamesURL(t *testing.T) {
	// A failing opener is an error naming the URL for manual opening —
	// never a silent success.
	pinOpener(t,
		func(cmd string) (string, error) { return "/usr/bin/" + cmd, nil },
		func(name string, args ...string) error { return fmt.Errorf("no display") },
	)
	err := OpenBrowserOn("linux", "http://127.0.0.1:8006/?console=x")
	if err == nil || !strings.Contains(err.Error(), "http://127.0.0.1:8006/?console=x") {
		t.Errorf("failing opener must name the URL, got %v", err)
	}
	// A missing opener names the URL too.
	pinOpener(t,
		func(cmd string) (string, error) { return "", exec.ErrNotFound },
		func(name string, args ...string) error { return nil },
	)
	err = OpenBrowserOn("darwin", "http://127.0.0.1:8006/?console=x")
	if err == nil || !strings.Contains(err.Error(), "manually") {
		t.Errorf("missing opener must point at manual opening, got %v", err)
	}
}

func TestDefaultBrowserRunner_ExitObserved(t *testing.T) {
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("no true binary: %v", err)
	}
	if err := defaultBrowserRunner(trueBin); err != nil {
		t.Errorf("exit-0 opener must succeed: %v", err)
	}
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skipf("no false binary: %v", err)
	}
	// A nonzero opener exit is failure, not success.
	if err := defaultBrowserRunner(falseBin); err == nil {
		t.Error("exit-1 opener must fail")
	}
}

func TestDefaultBrowserRunner_TimeoutReaps(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	orig := browserWaitTimeout
	browserWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { browserWaitTimeout = orig })
	start := time.Now()
	err = defaultBrowserRunner(sleepBin, "30")
	if err == nil || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("stuck opener must time out, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("timeout took %s — the stuck child was not reaped", elapsed)
	}
}
