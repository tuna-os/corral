package launcher

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestConsoleURL(t *testing.T) {
	got := ConsoleURL("127.0.0.1:8006", "tuna-v1")
	// Canonical local key is //local/<name>, URL-encoded.
	if !strings.HasPrefix(got, "http://127.0.0.1:8006/?console=") {
		t.Fatalf("console URL wrong: %q", got)
	}
	if !strings.Contains(got, "local") || !strings.Contains(got, "tuna-v1") {
		t.Errorf("console URL must name the VM: %q", got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("console URL must be encoded: %q", got)
	}
	want := "http://127.0.0.1:8006/?console=" + "%2F%2Flocal%2Ftuna-v1"
	if got != want {
		t.Errorf("console URL = %q, want %q", got, want)
	}
}

func TestServeOwned_LoopbackOnly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok\n"))
	})
	srv, err := ServeOwned(mux, 0)
	if err != nil {
		t.Fatalf("ServeOwned: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()
	if !strings.HasPrefix(srv.Addr, "127.0.0.1:") {
		t.Errorf("owned server must bind loopback, got %q", srv.Addr)
	}
	if err := srv.WaitReady(5 * time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	_, port, err := net.SplitHostPort(srv.Addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", srv.Addr, err)
	}
	req, err := http.NewRequest(http.MethodGet, "http://"+srv.Addr+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = net.JoinHostPort("attacker.example", port)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request with unapproved Host: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("unapproved Host status = %d, want %d", resp.StatusCode, http.StatusMisdirectedRequest)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestOwnedServer_ShutdownRetiresHijacked(t *testing.T) {
	returned := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	// A noVNC-shaped bridge: hijack, serve until the peer goes away, then
	// return (mirroring the WebSocket server's deferred close).
	mux.HandleFunc("/console", func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("handler must see a hijacker")
			return
		}
		c, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = c.Close() }()
		var b [64]byte
		_, _ = c.Read(b[:])
	})
	srv, err := ServeOwned(mux, 0)
	if err != nil {
		t.Fatalf("ServeOwned: %v", err)
	}
	// Open a console connection and prove it is tracked.
	c, err := net.Dial("tcp", srv.Addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := fmt.Fprintf(c, "GET /console HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\n\r\n", srv.Addr); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for srv.openConns() != want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if srv.openConns() != want {
			t.Fatalf("tracked conns = %d, want %d", srv.openConns(), want)
		}
	}
	waitFor(1)

	// Shutdown must close the bridge AND observe the handler's return —
	// http.Server.Shutdown alone does neither for hijacked connections.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Error("Shutdown returned but the console handler never retired")
	}
	if n := srv.openConns(); n != 0 {
		t.Errorf("tracked conns after Shutdown = %d, want 0", n)
	}
}

func TestOwnedServer_ShutdownReportsUnretired(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	// A stuck bridge: hijacks and ignores the close, returning only when
	// the test releases it.
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
		t.Fatalf("ServeOwned: %v", err)
	}
	// Teardown releases the handler first, then shuts down: the reverse
	// order would wait forever on the stuck bridge.
	defer func() {
		close(release)
		_ = srv.Shutdown(context.Background())
	}()
	c, err := net.Dial("tcp", srv.Addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := fmt.Fprintf(c, "GET /stuck HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\n\r\n", srv.Addr); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.openConns() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.openConns() != 1 {
		t.Fatal("stuck connection was not tracked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = srv.Shutdown(ctx)
	if err == nil || !strings.Contains(err.Error(), "still open") {
		t.Errorf("stuck handler must fail Shutdown naming unretired conns, got %v", err)
	}
}

func TestOwnedServer_EarlyCloseStillTracked(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	// A handler that closes its connection and THEN blocks: the Close is
	// not a retirement, so Shutdown must still report it unretired. (The
	// old Close-unregisters registry claimed success here.)
	mux.HandleFunc("/early", func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		c, _, err := hj.Hijack()
		if err != nil {
			return
		}
		_ = c.Close()
		<-release
	})
	srv, err := ServeOwned(mux, 0)
	if err != nil {
		t.Fatalf("ServeOwned: %v", err)
	}
	defer func() {
		close(release)
		_ = srv.Shutdown(context.Background())
	}()
	c, err := net.Dial("tcp", srv.Addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := fmt.Fprintf(c, "GET /early HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\n\r\n", srv.Addr); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for srv.openConns() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if srv.openConns() != 1 {
		t.Fatal("early-closed connection was not tracked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil || !strings.Contains(err.Error(), "still open") {
		t.Errorf("close-then-stuck handler must fail Shutdown naming unretired conns, got %v", err)
	}
}

func TestRefuseNonLoopback(t *testing.T) {
	if err := RefuseNonLoopback("127.0.0.1:8006"); err != nil {
		t.Errorf("loopback must pass: %v", err)
	}
	if err := RefuseNonLoopback("0.0.0.0:8006"); err == nil {
		t.Error("0.0.0.0 must be refused")
	}
	if err := RefuseNonLoopback("100.64.0.1:8006"); err == nil {
		t.Error("tailscale IP must be refused for the launcher")
	}
}
