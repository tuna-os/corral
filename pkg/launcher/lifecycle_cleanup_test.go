package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
)

// directSleepVM isolates qemu state to temp dirs in direct (no-systemd)
// mode and starts a sleep VM standing in for QEMU. Cleanup stops the VM
// and restores the seams.
func directSleepVM(t *testing.T, name string) qemu.StartToken {
	t.Helper()
	home := t.TempDir()
	qemu.SetStateDirs(home, t.TempDir())
	qemu.SetUserSystemd(func() bool { return false })
	t.Cleanup(func() {
		_ = qemu.Stop(name)
		qemu.SetStateDirs("", "")
		qemu.SetUserSystemd(nil)
	})
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// command.json is qemu-internal; tests craft it the way C2's reuse
	// tests do rather than reaching through unexported writers.
	cmd, err := json.Marshal(map[string]any{"binary": sleep, "args": []string{"120"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "command.json"), cmd, 0o644); err != nil {
		t.Fatal(err)
	}
	tok, started, err := qemu.StartOwnedToken(name)
	if err != nil || !started {
		t.Fatalf("start: started=%v err=%v", started, err)
	}
	if !tok.Resolved {
		t.Fatal("start token unresolved")
	}
	return tok
}

// stuckServer starts an owned server with one console connection that
// ignores shutdown (returns only when release closes). The caller must
// close release and shut the server down.
func stuckServer(t *testing.T) (*OwnedServer, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/stuck", func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		c, _, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		<-release
	})
	srv, err := ServeOwned(mux, 0)
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := fmt.Fprintf(c, "GET /stuck HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\n\r\n", srv.Addr); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.openConns() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.openConns() != 1 {
		t.Fatal("stuck connection was not tracked")
	}
	return srv, release
}

func TestShutdownCtx_WebFailureStillStopsVM(t *testing.T) {
	tok := directSleepVM(t, "gate")
	srv, release := stuckServer(t)
	defer func() {
		close(release)
		_ = srv.Shutdown(context.Background())
	}()
	// A failed web shutdown never skips an explicitly requested VM stop:
	// the error names the unretired web side AND the VM ends stopped.
	res := &LaunchResult{VMName: "gate", Server: srv}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := ShutdownCtx(ctx, res, true)
	if err == nil || !strings.Contains(err.Error(), "still open") {
		t.Errorf("Shutdown must report the unretired web side, got %v", err)
	}
	// Without stop binding the VM stop refuses (named alongside the web
	// failure) and the child is retired by hand.
	if !qemu.DirectStopBound() {
		if err == nil || !strings.Contains(err.Error(), "cannot bind a signal target") {
			t.Errorf("shutdown stop without binding must name the unbound target, got %v", err)
		}
		killStartedChild(t, tok.PID)
		return
	}
	if qemu.IsRunning("gate") {
		t.Error("VM still running after Shutdown with stopVM despite web failure")
	}
}

func TestShutdownCtx_VMSurvivesDefault(t *testing.T) {
	tok := directSleepVM(t, "gate")
	// Hygiene where stops refuse: the VM survives shutdown by design
	// (asserted below), and is retired after the test, not stranded.
	t.Cleanup(func() {
		if !qemu.DirectStopBound() {
			killStartedChild(t, tok.PID)
		}
	})
	srv, release := stuckServer(t)
	defer func() {
		close(release)
		_ = srv.Shutdown(context.Background())
	}()
	res := &LaunchResult{VMName: "gate", Server: srv}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := ShutdownCtx(ctx, res, false); err == nil {
		t.Error("Shutdown must still report the web failure")
	}
	if !qemu.IsRunning("gate") {
		t.Error("VM must survive Shutdown without stopVM")
	}
}

func TestCleanupOwnedStart(t *testing.T) {
	// Subtests: qemu state is process-global, so each case owns it alone.
	t.Run("owned stops what this run started", func(t *testing.T) {
		tok := directSleepVM(t, "owned")
		cerr := CleanupOwnedStart("owned", true, tok)
		if !qemu.DirectStopBound() {
			requireUnboundStopRefusal(t, cerr, "cleanup of an owned VM")
			killStartedChild(t, tok.PID)
			return
		}
		if cerr != nil {
			t.Errorf("cleanup of owned VM: %v", cerr)
		}
		if qemu.IsRunning("owned") {
			t.Error("owned VM still running after cleanup")
		}
	})
	t.Run("reused left as found", func(t *testing.T) {
		tok := directSleepVM(t, "reused")
		t.Cleanup(func() {
			if !qemu.DirectStopBound() {
				killStartedChild(t, tok.PID)
			}
		})
		CleanupOwnedStart("reused", false, qemu.StartToken{})
		if !qemu.IsRunning("reused") {
			t.Error("reused VM disturbed by cleanup")
		}
	})
	t.Run("replaced stranger refused", func(t *testing.T) {
		tok1 := directSleepVM(t, "replaced")
		// Without stop binding the live stop refuses and the
		// replacement flow below (which needs bound stops) cannot run.
		if err := qemu.Stop("replaced"); !qemu.DirectStopBound() {
			requireUnboundStopRefusal(t, err, "stop of a live direct VM")
			killStartedChild(t, tok1.PID)
			return
		} else if err != nil {
			t.Fatalf("replacement stop: %v", err)
		}
		tok2, started, err := qemu.StartOwnedToken("replaced")
		if err != nil || !started {
			t.Fatalf("replacement start: %v", err)
		}
		_ = tok2
		CleanupOwnedStart("replaced", true, tok1)
		if !qemu.IsRunning("replaced") {
			t.Error("replacement VM stopped by a stale owned cleanup")
		}
	})
}

func TestServeAndOpenCtx_ReadinessFailureShutsDown(t *testing.T) {
	// A server that starts but never becomes ready (no /healthz): the
	// failure shuts the server down on a fresh context instead of
	// abandoning it behind the readiness error.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	res, err := ServeAndOpenCtx(context.Background(), "gate", LaunchOpts{
		WebHandler: mux,
		WebPort:    port,
		WebTimeout: 300 * time.Millisecond,
	})
	if res != nil {
		t.Error("failed serve must return no result")
		_ = res.Server.Shutdown(context.Background())
	}
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("unready web must fail readiness, got %v", err)
	}
	// The listener is gone: the server was actually shut down.
	c, derr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if derr == nil {
		_ = c.Close()
		t.Error("web listener still accepting after readiness failure — server abandoned")
	}
}
