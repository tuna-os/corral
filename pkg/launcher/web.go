package launcher

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/tuna-os/corral/pkg/web"
)

// ConsoleURL builds the concrete noVNC console URL for a local VM:
// http://<webAddr>/?console=<URL-encoded canonical VM key>. The canonical
// key for a local VM is "//local/<name>" (peer/context empty, namespace
// local) — the same vmKey() the dashboard's console popout uses, so the
// launcher opens exactly what ?console= deep linking renders after the
// initial inventory load.
func ConsoleURL(webAddr, vmName string) string {
	key := "//local/" + vmName
	return fmt.Sprintf("http://%s/?console=%s", webAddr, url.QueryEscape(key))
}

// connRegistry tracks hijacked (WebSocket) connections the owned server
// upgraded. http.Server.Shutdown stops listeners and waits for plain
// requests, but explicitly does not close or wait for hijacked connections
// (https://pkg.go.dev/net/http#Server.Shutdown) — without this registry a
// noVNC bridge and its two copy goroutines would outlive "shutdown".
// Shutdown seals admissions, closes every tracked connection, and waits
// until each entered handler has returned (observed via ServeHTTP return,
// never via socket Close) or the context expires.
type connRegistry struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
	// handlers counts ServeHTTP invocations currently inside the wrapped
	// handler, hijacked or not. Registered at ENTRY — before any hijack
	// can run — so a hijack racing shutdown is always covered: either it
	// entered (and the wait observes it) or it is refused at the seal.
	handlers int
	// sealed, once set by Shutdown, refuses new entries and new hijacks.
	// A refused hijack never reaches the server, so its connection stays
	// in the server's active set and plain Shutdown waits for it.
	sealed bool
}

func newConnRegistry() *connRegistry {
	return &connRegistry{conns: map[net.Conn]struct{}{}}
}

// enter registers one handler lifetime. It reports false when the registry
// is sealed: the handler must not run (the wrapper answers 503 instead).
func (r *connRegistry) enter() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return false
	}
	r.handlers++
	return true
}

// exit retires one entered handler, untracking its hijacked connection if
// it hijacked one. Every enter is paired with exactly one exit — including
// on handler panic, via the wrapper's defer.
func (r *connRegistry) exit(c net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c != nil {
		delete(r.conns, c)
	}
	r.handlers--
}

// seal refuses all future entries and hijacks. Idempotent.
func (r *connRegistry) seal() {
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()
}

// hijackRefused reports whether new hijacks must be refused (shutdown
// sealed the registry). Checked BEFORE the underlying Hijack, so a racing
// hijack never slips between the server's StateHijacked and our track with
// count 0.
func (r *connRegistry) hijackRefused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sealed
}

// sealedForTest reports whether Shutdown sealed the registry. Tests use it
// to stage a hijack that provably races shutdown (see web_seal_test.go);
// production code observes retirement via Shutdown.
func (r *connRegistry) sealedForTest() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sealed
}

func (r *connRegistry) track(c net.Conn) {
	r.mu.Lock()
	r.conns[c] = struct{}{}
	r.mu.Unlock()
}

func (r *connRegistry) closeAll() {
	r.mu.Lock()
	conns := make([]net.Conn, 0, len(r.conns))
	for c := range r.conns {
		conns = append(conns, c)
	}
	r.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (r *connRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conns)
}

func (r *connRegistry) idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.handlers == 0 && len(r.conns) == 0
}

func (r *connRegistry) inflight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.handlers
}

// wait blocks until every entered handler has returned or ctx expires. A
// connection hijacked after closeAll ran (racing shutdown) is still
// tracked: its handler was entered before it could hijack, so the wait
// observes it, and the re-sweep below closes its socket so the handler
// retires instead of stalling on a close the first sweep missed. An
// unretired handler fails the wait naming it — never a silent abandon.
func (r *connRegistry) wait(ctx context.Context) error {
	t := time.NewTicker(25 * time.Millisecond)
	defer t.Stop()
	for {
		// Re-sweep every iteration: a hijack that won its underlying
		// upgrade after the seal (between the server's StateHijacked
		// and our track) is closed here.
		r.closeAll()
		if r.idle() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%d console connection(s) still open after shutdown: %w", r.inflight(), ctx.Err())
		case <-t.C:
		}
	}
}

// hijackTracker wraps the ResponseWriter so upgraded connections register
// themselves. Plain requests pass through untouched; only Hijack is
// intercepted, which is exactly the set Shutdown would otherwise abandon.
type hijackTracker struct {
	http.ResponseWriter
	reg  *connRegistry
	conn net.Conn
}

func (w *hijackTracker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.reg.hijackRefused() {
		return nil, nil, fmt.Errorf("server is shutting down — hijack refused")
	}
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("web server does not support connection hijacking")
	}
	c, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.reg.track(c)
	w.conn = c
	return c, rw, nil
}

func (w *hijackTracker) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func trackHijacks(h http.Handler, reg *connRegistry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Lifetime is registered at ENTRY, before the handler can
		// hijack: the wait below observes every entered handler, so a
		// hijack racing shutdown can never slip through uncounted. A
		// sealed registry refuses the entry outright (503).
		if !reg.enter() {
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		tw := &hijackTracker{ResponseWriter: w, reg: reg}
		// Handler lifetime, not socket lifetime, retires the entry: a
		// handler that closes its connection and then blocks has NOT
		// retired, and unregistering on Close would let Shutdown claim a
		// retirement it never observed. Deferred (not trailing) so the
		// exit runs even when the handler panics — the http server
		// recovers per-connection, and the registry must observe the
		// return either way. The close reaps a connection a returned
		// handler leaked (closing an already-closed socket is a
		// harmless error); the exit observes the return.
		defer func() {
			if tw.conn != nil {
				_ = tw.conn.Close()
			}
			reg.exit(tw.conn)
		}()
		h.ServeHTTP(tw, r)
	})
}

// OwnedServer is a web UI server the launcher started and therefore owns:
// it serves on loopback, its listener address is recorded, and Shutdown
// verifies it stops. It never outlives the launcher process.
type OwnedServer struct {
	Server   *http.Server
	Listener net.Listener
	Addr     string
	reg      *connRegistry
}

// ServeOwned starts handler on 127.0.0.1 (port 0 = any free loopback port)
// and returns the owned server. Non-loopback binds are refused — the
// launcher is local-only by construction. Upgraded (WebSocket) connections
// are tracked for verified retirement at Shutdown.
func ServeOwned(handler http.Handler, port int) (*OwnedServer, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port %d", port)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, fmt.Errorf("binding loopback web server: %w", err)
	}
	handler, err = web.EnforceLoopbackHostPolicy(handler, ln.Addr())
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("applying owned loopback Host policy: %w", err)
	}
	reg := newConnRegistry()
	srv := &http.Server{
		Handler:           trackHijacks(handler, reg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	owned := &OwnedServer{Server: srv, Listener: ln, Addr: ln.Addr().String(), reg: reg}
	go func() { _ = srv.Serve(ln) }()
	return owned, nil
}

// WaitReady blocks until GET /healthz on the owned server returns 200.
func (s *OwnedServer) WaitReady(timeout time.Duration) error {
	return s.WaitReadyCtx(context.Background(), timeout)
}

// WaitReadyCtx is WaitReady under ctx (launcher shutdown cancels the wait).
func (s *OwnedServer) WaitReadyCtx(ctx context.Context, timeout time.Duration) error {
	return WaitWebReady("http://"+s.Addr+"/healthz", ReadinessOpts{Timeout: timeout, Ctx: ctx})
}

// openConns reports how many upgraded connections are currently tracked.
// Exported for tests; production code observes retirement via Shutdown.
func (s *OwnedServer) openConns() int {
	if s.reg == nil {
		return 0
	}
	return s.reg.count()
}

// Shutdown stops the owned server and verifies retirement: admissions are
// sealed first (a racing hijack is refused instead of slipping between
// the server's StateHijacked and our track with count 0), then plain
// requests retire via http.Server.Shutdown, every tracked console
// connection is closed, and each entered handler's return is observed. A
// context timeout bounds the wait; the error names an unverified shutdown
// rather than claiming one.
func (s *OwnedServer) Shutdown(ctx context.Context) error {
	if s.reg != nil {
		s.reg.seal()
	}
	httpErr := s.Server.Shutdown(ctx)
	if s.reg == nil {
		if httpErr != nil {
			return fmt.Errorf("shutting down web server on %s: %w", s.Addr, httpErr)
		}
		return nil
	}
	s.reg.closeAll()
	if err := s.reg.wait(ctx); err != nil {
		if httpErr != nil {
			return fmt.Errorf("shutting down web server on %s: %v; %v", s.Addr, httpErr, err)
		}
		return fmt.Errorf("shutting down web server on %s: %w", s.Addr, err)
	}
	if httpErr != nil {
		return fmt.Errorf("shutting down web server on %s: %w", s.Addr, httpErr)
	}
	return nil
}

// RefuseNonLoopback rejects an explicit --addr that is not loopback. The
// launcher takes a port, not an address, but the guard documents the
// invariant where a future flag might widen it.
func RefuseNonLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid addr %q: %w", addr, err)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("refusing non-loopback addr %q (launcher is loopback-only)", addr)
	}
	return nil
}
