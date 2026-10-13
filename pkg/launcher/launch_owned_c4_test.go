package launcher

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tuna-os/corral/pkg/qemu"
)

// Start/stop ownership across the launcher boundary: the started/token
// outcome survives errors, failed cleanup joins the primary error, and
// Shutdown's --stop-vm stop is token-owned.

func launchedSleepVM(t *testing.T) (*LaunchResult, qemu.StartToken) {
	t.Helper()
	directSleepVM(t, "gate")
	tok, started, err := qemu.StartOwnedToken("gate")
	if err != nil || !started {
		t.Fatalf("start: started=%v err=%v", started, err)
	}
	if !tok.Resolved {
		t.Fatal("start left the token unresolved")
	}
	// Defensive stop: if the test fails before its own shutdown, no VM
	// may leak. Runs before directSleepVM's cleanup (LIFO).
	t.Cleanup(func() { _ = qemu.Stop("gate") })
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv, err := ServeOwned(mux, 0)
	if err != nil {
		t.Fatalf("ServeOwned: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return &LaunchResult{VMName: "gate", Server: srv, Started: started, Token: tok}, tok
}

// TestStartAndWaitCtx_PreservesOutcomeOnError: a start that launches but
// cannot verify reports its outcome WITH the error — started stays true
// and the error is the activation uncertainty from the start phase, not a
// downstream readiness error over a zeroed outcome.
func TestStartAndWaitCtx_PreservesOutcomeOnError(t *testing.T) {
	home := t.TempDir()
	units := t.TempDir()
	qemu.SetStateDirs(home, units)
	t.Cleanup(func() { qemu.SetStateDirs("", "") })
	qemu.SetUserSystemd(func() bool { return true })
	t.Cleanup(func() { qemu.SetUserSystemd(nil) })
	if err := os.MkdirAll(filepath.Join(home, "gate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(units, "corral-gate.service"), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	qemu.SetSystemctl(func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "show" {
			return []byte("MainPID=0\nActiveEnterTimestamp=n/a\n"), nil
		}
		return []byte{}, nil
	})
	t.Cleanup(func() { qemu.SetSystemctl(nil) })
	started, tok, err := StartAndWaitCtx(context.Background(), "gate", time.Second)
	if err == nil || !strings.Contains(err.Error(), "activation cannot be verified") {
		t.Errorf("unverifiable start must fail naming the activation uncertainty, got %v", err)
	}
	if !started {
		t.Error("launched-but-unverifiable start must still report started=true")
	}
	if tok.Resolved {
		t.Error("unverifiable start must leave the token unresolved")
	}
}

// TestCleanupOwnedStart_ReportsFailedRetirement: cleanup returns its
// failure for joining with the primary error (a refused retirement must
// surface, not hide behind a warning), nil when there is nothing to stop
// or the owned stop succeeds.
func TestCleanupOwnedStart_ReportsFailedRetirement(t *testing.T) {
	if err := CleanupOwnedStart("gate", false, qemu.StartToken{}); err != nil {
		t.Errorf("cleanup of an unstarted VM must be nil, got %v", err)
	}
	if err := CleanupOwnedStart("gate", true, qemu.StartToken{}); err == nil ||
		!strings.Contains(err.Error(), "never resolved") {
		t.Errorf("cleanup with an unresolved token must fail naming it, got %v", err)
	}
	res, tok := launchedSleepVM(t)
	if err := CleanupOwnedStart(res.VMName, true, tok); !qemu.DirectStopBound() {
		requireUnboundStopRefusal(t, err, "cleanup of an owned VM")
		killStartedChild(t, tok.PID)
		return
	} else if err != nil {
		t.Errorf("cleanup of a stoppable owned VM must be nil, got %v", err)
	} else if qemu.IsRunning(res.VMName) {
		t.Error("VM should be stopped after successful cleanup")
	}
}

// TestShutdown_TokenBoundStopRefusesReplacement: a VM replaced between
// launch and Shutdown-with---stop-vm is refused — the stranger survives,
// and the error names the ownership change.
func TestShutdown_TokenBoundStopRefusesReplacement(t *testing.T) {
	res, tok := launchedSleepVM(t)
	_ = tok
	// Replace the VM: stop ours by token, start a stranger. Without
	// stop binding the live stop refuses and the replacement flow
	// below (which needs bound stops) cannot run.
	if err := qemu.StopOwned(res.VMName, res.Token); !qemu.DirectStopBound() {
		requireUnboundStopRefusal(t, err, "stop of a live direct VM")
		killStartedChild(t, res.Token.PID)
		return
	} else if err != nil {
		t.Fatalf("stop own: %v", err)
	}
	stok, sstarted, err := qemu.StartOwnedToken(res.VMName)
	if err != nil || !sstarted {
		t.Fatalf("start stranger: started=%v err=%v", sstarted, err)
	}
	t.Cleanup(func() { _ = qemu.StopOwned(res.VMName, stok) })
	if err := ShutdownCtx(context.Background(), res, true); err == nil ||
		!strings.Contains(err.Error(), "ownership changed") {
		t.Errorf("shutdown over a replaced VM must refuse naming the ownership change, got %v", err)
	}
	if !qemu.IsRunning(res.VMName) {
		t.Error("the replacement VM must survive a refused shutdown stop")
	}
}

// TestShutdown_TokenBoundStopStopsOwnVM: without a replacement the
// token-owned shutdown stop stops exactly this invocation's VM.
func TestShutdown_TokenBoundStopStopsOwnVM(t *testing.T) {
	res, _ := launchedSleepVM(t)
	if err := ShutdownCtx(context.Background(), res, true); !qemu.DirectStopBound() {
		requireUnboundStopRefusal(t, err, "shutdown stop of our own VM")
		killStartedChild(t, res.Token.PID)
		return
	} else if err != nil {
		t.Fatalf("shutdown stop of our own VM: %v", err)
	} else if qemu.IsRunning(res.VMName) {
		t.Error("our VM should be stopped after shutdown with --stop-vm")
	}
}
