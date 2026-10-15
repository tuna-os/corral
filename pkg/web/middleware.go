package web

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/logging"
)

// middleware wraps a handler with behaviour that applies to every request,
// independent of the route.
type middleware func(http.Handler) http.Handler

// requestPipeline returns the middleware every routed request passes through,
// outermost first. The order is part of the security model:
//
//  1. requestTracer gives the request an ID (the client's X-Request-ID when
//     it is well formed) and logs its outcome. It runs first, so a refused
//     request is traced too.
//  2. peerServiceAuth strips client-supplied service identity headers and
//     marks requests that carry the peer token as trusted.
//  3. adminGate refuses unauthenticated /api calls (when auth is required)
//     and mutations from non-admins, except the VDI self-service routes,
//     which authorize against the desktop's owner (selfServiceMutation).
//  4. activityTracker records accepted, non-machinery requests as activity.
//  5. peerGate proxies VM subresources on a peer Corral before the route
//     table can resolve them against a local backend.
//
// Add new cross-cutting request behaviour here, not in individual handlers.
func requestPipeline() []middleware {
	return []middleware{requestTracer, peerServiceAuth, adminGate, activityTracker, peerGate}
}

// chain wraps h in mws so that mws[0] sees the request first.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// requestTracer carries a request ID in the request context, where the
// handlers bind it into backend calls, commands, peer requests, and tasks.
// It echoes the ID in the X-Request-ID response header, so an operator can
// quote it from the browser. The record of the outcome is debug level,
// except a server error, which is a warning.
func requestTracer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(logging.RequestIDHeader)
		if !logging.ValidRequestID(id) {
			id = logging.NewRequestID()
		}
		ctx := logging.WithRequestID(r.Context(), id)
		w.Header().Set(logging.RequestIDHeader, id)
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(sw, r.WithContext(ctx))

		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		level := slog.LevelDebug
		if status >= http.StatusInternalServerError {
			level = slog.LevelWarn
		}
		// The path only: a query string can carry a console ticket.
		slog.LogAttrs(ctx, level, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", status),
			slog.Duration("duration", time.Since(start)))
	})
}

// statusWriter records the status a handler sent. It passes through the
// optional interfaces that handlers rely on: Hijack for the console
// websockets, Flush for streamed output, and ReadFrom for file transfers.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return io.Copy(w.ResponseWriter, r)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("the response writer cannot be hijacked")
	}
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach the server's writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// loopbackAllowedHosts pins the HTTP Host header to the local listener address
// when Corral binds only to loopback. This closes DNS rebinding against the
// default local web UI without constraining tailnet or explicitly public binds.
func loopbackAllowedHosts(bindAddr string, listenerAddr net.Addr) []string {
	bindHost, _, err := net.SplitHostPort(bindAddr)
	if err != nil || listenerAddr == nil {
		return nil
	}
	if ip := net.ParseIP(strings.Trim(bindHost, "[]")); ip != nil && ip.IsLoopback() {
		_, port, err := net.SplitHostPort(listenerAddr.String())
		if err == nil {
			return []string{net.JoinHostPort(bindHost, port), net.JoinHostPort("localhost", port)}
		}
	}
	if strings.EqualFold(bindHost, "localhost") {
		_, port, err := net.SplitHostPort(listenerAddr.String())
		if err == nil {
			return []string{net.JoinHostPort(bindHost, port)}
		}
	}
	return nil
}

func enforceAllowedHosts(next http.Handler, hosts []string) (http.Handler, error) {
	allowed := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" || strings.ContainsAny(host, "*/\\") {
			return nil, fmt.Errorf("invalid allowed host %q: expected an exact host[:port]", host)
		}
		allowed[strings.ToLower(host)] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("allowed-host policy must include at least one exact host")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := allowed[strings.ToLower(r.Host)]; !ok {
			http.Error(w, "request Host is not allowed", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}
