package launcher

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// browserLookPath finds the platform opener. Sealed so tests pin opener
// presence instead of depending on the CI image's PATH.
var browserLookPath = exec.LookPath

// browserWaitTimeout bounds the platform opener's run: xdg-open/open hand
// off and exit quickly, so anything slower is a stuck helper, not a
// browser session (the browser is the opener's child, not the opener).
var browserWaitTimeout = 30 * time.Second

// browserReapTimeout bounds the reap after a timeout kill. Wait-after-Kill
// usually returns, but that is not a kernel guarantee — an unkillable
// child never exits — so the reap is bounded too, and past it the runner
// reports uncertainty rather than claiming retirement.
var browserReapTimeout = 5 * time.Second

// defaultBrowserRunner starts the opener, waits for it bounded, and reaps
// it. A Start-only opener would report a nonzero exit as success and leave
// a zombie; here the exit is observed — failure names the opener problem
// and the caller prints the URL for manual opening either way. Only the
// opener child is ever signalled: the browser it spawned is the user's
// session and is deliberately left alone.
func defaultBrowserRunner(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(browserWaitTimeout):
		_ = cmd.Process.Kill()
		select {
		case <-done:
			return fmt.Errorf("opener %s did not exit within %s", name, browserWaitTimeout)
		case <-time.After(browserReapTimeout):
			return fmt.Errorf("opener %s did not exit within %s and is still unreaped after kill — its retirement is unverified (open the console URL manually)", name, browserWaitTimeout)
		}
	}
}

// browserRunner opens a URL in the platform browser. Sealed for tests.
var browserRunner = defaultBrowserRunner

// SetBrowserRunner overrides the opener (for tests). Nil restores it.
func SetBrowserRunner(f func(name string, args ...string) error) {
	if f == nil {
		browserRunner = defaultBrowserRunner
		return
	}
	browserRunner = f
}

// BrowserCommand reports the platform opener (xdg-open on Linux, open on
// macOS) for tests and diagnostics.
func BrowserCommand(hostOS string) (string, error) {
	switch hostOS {
	case "linux":
		return "xdg-open", nil
	case "darwin":
		return "open", nil
	default:
		return "", fmt.Errorf("unsupported host OS %q (Linux/macOS only)", hostOS)
	}
}

// OpenBrowser opens url in the platform browser. The error names the real
// failure — the caller prints the URL either way so a headless host still
// gets a usable console address.
func OpenBrowser(url string) error {
	return OpenBrowserOn(runtime.GOOS, url)
}

// OpenBrowserOn opens url on an explicit host OS (for tests).
func OpenBrowserOn(hostOS, url string) error {
	cmd, err := BrowserCommand(hostOS)
	if err != nil {
		return err
	}
	if _, err := browserLookPath(cmd); err != nil {
		return fmt.Errorf("%s not found in PATH — open %s manually", cmd, url)
	}
	if err := browserRunner(cmd, url); err != nil {
		return fmt.Errorf("opening %s with %s failed (%v) — open the URL manually", url, cmd, err)
	}
	return nil
}
