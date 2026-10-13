package launcher

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Shutdown seals admissions: handler lifetime is registered at entry and
// a hijack racing shutdown is refused — never slipped between the
// server's StateHijacked and our track with count 0.

// TestOwnedServer_RacingHijackRefused stages the exact barrier: a handler
// is inside ServeHTTP (entered, counted) while Shutdown seals, and only
// then attempts its hijack. The hijack must be refused and the shutdown
// must succeed with the handler retired.
func TestOwnedServer_RacingHijackRefused(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	refused := make(chan bool, 1)
	returned := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/console", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("handler is not hijackable")
			close(returned)
			return
		}
		c, _, err := hj.Hijack()
		if err != nil {
			refused <- true
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			close(returned)
			return
		}
		refused <- false
		defer c.Close()
		defer close(returned)
		// Bridge shape: block until the socket dies.
		var buf [1]byte
		_, _ = c.Read(buf[:])
	})
	srv, err := ServeOwned(mux, 0)
	if err != nil {
		t.Fatalf("ServeOwned: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()
	addr := srv.Addr
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /console HTTP/1.1\r\nHost: x\r\nConnection: keep-alive\r\n\r\n")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never entered")
	}
	// Shutdown in the background: it blocks in Server.Shutdown on the
	// entered handler. The seal provably lands first (polled, exact) —
	// only then is the handler released to attempt its hijack.
	shutErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutErr <- srv.Shutdown(ctx)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !srv.reg.sealedForTest() {
		if !time.Now().Before(deadline) {
			t.Fatal("shutdown never sealed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	if err := <-shutErr; err != nil {
		t.Errorf("shutdown with a refused racing hijack must succeed, got %v", err)
	}
	select {
	case r := <-refused:
		if !r {
			t.Error("hijack racing shutdown must be refused")
		}
	case <-time.After(5 * time.Second):
		t.Error("handler never reached its hijack")
	}
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Error("handler never returned")
	}
}

// TestOwnedServer_PanickingHijackedHandlerRetires: a hijacked handler that
// panics still retires — the wrapper's deferred exit runs during unwind
// (the http server recovers per-connection), so Shutdown observes the
// return instead of timing out on a stuck count.
func TestOwnedServer_PanickingHijackedHandlerRetires(t *testing.T) {
	ran := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/console", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(ran) })
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		if _, _, err := hj.Hijack(); err != nil {
			return
		}
		panic("boom")
	})
	srv, err := ServeOwned(mux, 0)
	if err != nil {
		t.Fatalf("ServeOwned: %v", err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()
	addr := srv.Addr
	srv.Server.ErrorLog = log.New(io.Discard, "", 0)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /console HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never ran")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("shutdown after a panicking handler must observe retirement, got %v", err)
	}
}

// TestTrackHijacks_SealedEntryRefused is the seal's unit edge: a sealed
// registry refuses the entry outright (503, handler never runs, nothing
// counted), while an unsealed one runs and retires.
func TestTrackHijacks_SealedEntryRefused(t *testing.T) {
	reg := newConnRegistry()
	ran := false
	h := trackHijacks(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
	}), reg)
	reg.seal()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if ran {
		t.Error("sealed entry must not run the handler")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("sealed entry code = %d, want 503", rec.Code)
	}
	if n := reg.inflight(); n != 0 {
		t.Errorf("sealed entry left %d handlers counted", n)
	}

	reg2 := newConnRegistry()
	ran2 := false
	h2 := trackHijacks(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran2 = true
	}), reg2)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, httptest.NewRequest("GET", "/", nil))
	if !ran2 {
		t.Error("unsealed entry must run the handler")
	}
	if rec2.Code != http.StatusOK {
		t.Errorf("unsealed entry code = %d, want 200", rec2.Code)
	}
	if n := reg2.inflight(); n != 0 {
		t.Errorf("returned handler left %d handlers counted", n)
	}
}
