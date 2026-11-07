package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
	"unicode/utf8"
)

const consoleReadyQuery = "ready_nonce"

// consoleReadiness belongs to one bound Server, not to the global route state.
// A binding is immutable; the mutex only protects installation and consumption.
type consoleReadiness struct {
	mu      sync.Mutex
	binding *consoleReadyBinding
	sent    bool
	retired bool
}

type consoleReadyBinding struct {
	ctx                        context.Context
	origin, host               string
	namespace, name, vmContext string
	nonce, pageURL             string
	ready                      chan struct{}
}

// NotifyConsoleReady enables a one-shot observation of the standalone page's
// real noVNC connect event. Call it before Serve. The returned URL is the only
// page given the nonce; the channel closes only after an authentic POST for
// this exact VM, namespace and backend context. It is not VNC authorization.
// Cancellation retires the observation without closing the success channel.
func (s *Server) NotifyConsoleReady(ctx context.Context, namespace, name, vmContext string) (string, <-chan struct{}, error) {
	if s.readiness == nil {
		return "", nil, fmt.Errorf("console readiness requires a bound web server")
	}
	return s.readiness.configure(ctx, s.URL(), namespace, name, vmContext, rand.Reader)
}

func (c *consoleReadiness) configure(ctx context.Context, base, namespace, name, vmContext string, entropy io.Reader) (string, <-chan struct{}, error) {
	if ctx == nil || ctx.Err() != nil {
		return "", nil, fmt.Errorf("console readiness requires an active context")
	}
	if !consolePathSegment.MatchString(namespace) || !consolePathSegment.MatchString(name) || len(vmContext) > 1024 || !utf8.ValidString(vmContext) {
		return "", nil, fmt.Errorf("invalid console readiness identity")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", nil, fmt.Errorf("invalid console readiness origin")
	}
	var secret [32]byte
	if _, err := io.ReadFull(entropy, secret[:]); err != nil {
		return "", nil, fmt.Errorf("generate console readiness nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(secret[:])
	page := *u
	page.Path = "/console/" + namespace + "/" + name
	query := url.Values{consoleReadyQuery: {nonce}}
	if vmContext != "" {
		query.Set("context", vmContext)
	}
	page.RawQuery = query.Encode()
	binding := &consoleReadyBinding{
		ctx: ctx, origin: base, host: u.Host,
		namespace: namespace, name: name, vmContext: vmContext,
		nonce: nonce, pageURL: page.String(), ready: make(chan struct{}),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.binding != nil || c.retired {
		return "", nil, fmt.Errorf("console readiness is already configured or retired")
	}
	c.binding = binding
	return binding.pageURL, binding.ready, nil
}

func (c *consoleReadiness) current() (*consoleReadyBinding, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.binding, c.retired
}

func (c *consoleReadiness) retire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.retired = true
}

func (b *consoleReadyBinding) matches(r *http.Request, vmContext, nonce string) bool {
	return r.PathValue("ns") == b.namespace && r.PathValue("name") == b.name &&
		vmContext == b.vmContext && subtle.ConstantTimeCompare([]byte(nonce), []byte(b.nonce)) == 1
}

func (c *consoleReadiness) page(w http.ResponseWriter, r *http.Request) {
	b, retired := c.current()
	if b == nil {
		handleConsolePage(w, r)
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		http.Error(w, "console query is too large", http.StatusRequestURITooLong)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "invalid console query", http.StatusBadRequest)
		return
	}
	nonces, present := query[consoleReadyQuery]
	if !present {
		handleConsolePage(w, r)
		return
	}
	if r.Host != b.host || len(nonces) != 1 || len(query["context"]) > 1 || !b.matches(r, query.Get("context"), query.Get(consoleReadyQuery)) {
		http.Error(w, "invalid console readiness page", http.StatusForbidden)
		return
	}
	if retired || b.ctx.Err() != nil {
		http.Error(w, "console readiness is retired", http.StatusGone)
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	serveConsolePage(w, r, b.nonce)
}

func (c *consoleReadiness) connected(w http.ResponseWriter, r *http.Request) {
	b, retired := c.current()
	if b == nil {
		http.NotFound(w, r)
		return
	}
	if r.Host != b.host || len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != b.origin {
		http.Error(w, "console readiness requires its bound origin and Host", http.StatusForbidden)
		return
	}
	if retired || b.ctx.Err() != nil {
		http.Error(w, "console readiness is retired", http.StatusGone)
		return
	}
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "console readiness requires a JSON body without a query", http.StatusBadRequest)
		return
	}
	// A stalled or oversized browser request cannot retain a handler forever.
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		http.Error(w, "cannot bound console readiness request", http.StatusInternalServerError)
		return
	}
	defer func() { _ = http.NewResponseController(w).SetReadDeadline(time.Time{}) }()
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	defer func() { _ = r.Body.Close() }()
	var report struct {
		Nonce   string  `json:"nonce"`
		Context *string `json:"context"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		http.Error(w, "invalid console readiness report", http.StatusBadRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF || report.Context == nil || !b.matches(r, *report.Context, report.Nonce) {
		http.Error(w, "invalid console readiness identity or nonce", http.StatusForbidden)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired || b.ctx.Err() != nil || r.Context().Err() != nil {
		http.Error(w, "console readiness is retired", http.StatusGone)
		return
	}
	if c.sent {
		http.Error(w, "console readiness was already reported", http.StatusConflict)
		return
	}
	c.sent = true
	close(b.ready) // Never waits for the CLI's stdout or for a consumer.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
