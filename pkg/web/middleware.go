package web

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// middleware wraps a handler with behaviour that applies to every request,
// independent of the route.
type middleware func(http.Handler) http.Handler

// requestPipeline returns the middleware every routed request passes through,
// outermost first. The order is part of the security model:
//
//  1. peerServiceAuth strips client-supplied service identity headers and
//     marks requests that carry the peer token as trusted.
//  2. adminGate refuses unauthenticated /api calls (when auth is required)
//     and mutations from non-admins.
//  3. activityTracker records accepted, non-machinery requests as activity.
//  4. peerGate proxies VM subresources on a peer Corral before the route
//     table can resolve them against a local backend.
//
// Add new cross-cutting request behaviour here, not in individual handlers.
func requestPipeline() []middleware {
	return []middleware{peerServiceAuth, adminGate, activityTracker, peerGate}
}

// chain wraps h in mws so that mws[0] sees the request first.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

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
