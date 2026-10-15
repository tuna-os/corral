package web

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Characterization tests for the request pipeline (#503): the host allowlist
// Serve wraps around the router, and the middleware chain newMux returns.
// They pin today's behaviour so the routing code can move without changing it.

func TestChain_FirstMiddlewareRunsFirst(t *testing.T) {
	var order []string
	tag := func(name string) middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	}), tag("a"), tag("b"), tag("c"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got := strings.Join(order, ","); got != "a,b,c,handler" {
		t.Fatalf("order = %s", got)
	}
}

func TestRequestPipeline_Order(t *testing.T) {
	want := []middleware{peerServiceAuth, adminGate, activityTracker, peerGate}
	got := requestPipeline()
	if len(got) != len(want) {
		t.Fatalf("pipeline has %d middleware, want %d", len(got), len(want))
	}
	for i := range want {
		if reflect.ValueOf(got[i]).Pointer() != reflect.ValueOf(want[i]).Pointer() {
			t.Errorf("pipeline[%d] is not the expected middleware", i)
		}
	}
}

func TestEnforceAllowedHosts_MatchesCaseInsensitivelyAfterTrimming(t *testing.T) {
	handler, err := enforceAllowedHosts(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), []string{"  LocalHost:8006 ", "corral.example.ts.net"})
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]int{
		"localhost:8006":            http.StatusNoContent,
		"LOCALHOST:8006":            http.StatusNoContent,
		"corral.example.ts.net":     http.StatusNoContent,
		"localhost:8007":            http.StatusMisdirectedRequest,
		"localhost":                 http.StatusMisdirectedRequest,
		"corral.example.ts.net:443": http.StatusMisdirectedRequest,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: status = %d, want %d", host, w.Code, want)
		}
	}
}

func TestEnforceAllowedHosts_RejectionIsPlainText(t *testing.T) {
	handler, err := enforceAllowedHosts(http.NotFoundHandler(), []string{"127.0.0.1:8006"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/vms", nil)
	r.Host = "evil.example"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if got := w.Body.String(); got != "request Host is not allowed\n" {
		t.Errorf("body = %q", got)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestEnforceAllowedHosts_RejectsInvalidPolicy(t *testing.T) {
	for _, hosts := range [][]string{
		nil,
		{},
		{""},
		{"   "},
		{"*.example.com"},
		{"example.com/path"},
		{`example.com\x`},
		{"ok.example", "*"},
	} {
		if h, err := enforceAllowedHosts(http.NotFoundHandler(), hosts); err == nil || h != nil {
			t.Errorf("enforceAllowedHosts(%q) = (%v, %v), want (nil, error)", hosts, h, err)
		}
	}
}

func TestLoopbackAllowedHosts(t *testing.T) {
	v4 := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5555}
	v6 := &net.TCPAddr{IP: net.ParseIP("::1"), Port: 5555}
	for _, tc := range []struct {
		bind     string
		listener net.Addr
		want     []string
	}{
		{"127.0.0.1:0", v4, []string{"127.0.0.1:5555", "localhost:5555"}},
		{"127.0.0.2:8006", v4, []string{"127.0.0.2:5555", "localhost:5555"}},
		{"[::1]:0", v6, []string{"[::1]:5555", "localhost:5555"}},
		{"localhost:0", v4, []string{"localhost:5555"}},
		{"LocalHost:0", v4, []string{"LocalHost:5555"}},
		{"0.0.0.0:8006", v4, nil},
		{":8006", v4, nil},
		{"100.64.0.1:8006", v4, nil},
		{"corral.example:8006", v4, nil},
		{"no-port", v4, nil},
		{"127.0.0.1:0", nil, nil},
	} {
		got := loopbackAllowedHosts(tc.bind, tc.listener)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") || (got == nil) != (tc.want == nil) {
			t.Errorf("loopbackAllowedHosts(%q, %v) = %#v, want %#v", tc.bind, tc.listener, got, tc.want)
		}
	}
}

// Client-supplied service identity headers are stripped before the admin gate
// sees the request, so spoofing one never grants admin rights.
func TestPipeline_SpoofedServiceIdentityIsStripped(t *testing.T) {
	t.Setenv("CORRAL_ADMINS", "alice@example.com")
	fx := NewTestFixture()
	defer fx.Close()

	req, _ := http.NewRequest(http.MethodPost, fx.Server.URL+"/api/vms", strings.NewReader("{}"))
	req.Header.Set("Corral-Service-Identity", "corral-peer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("spoofed identity: status = %d, want 403", resp.StatusCode)
	}
}

// A request carrying the peer token reaches the handler past the admin gate.
func TestPipeline_PeerTokenPassesAdminGate(t *testing.T) {
	t.Setenv("CORRAL_ADMINS", "alice@example.com")
	t.Setenv("CORRAL_PEER_TOKEN", "s3cret")
	fx := NewTestFixture()
	defer fx.Close()

	req, _ := http.NewRequest(http.MethodPost, fx.Server.URL+"/api/vms", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	// handleCreateVM ran: an empty body fails its own validation.
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("peer-token request: status = %d, want 400 from the handler", resp.StatusCode)
	}
}

// With CORRAL_AUTH_REQUIRED, anonymous /api/ calls get 401 — except whoami —
// while probes and static assets stay reachable.
func TestPipeline_AuthenticationRequired(t *testing.T) {
	t.Setenv("CORRAL_AUTH_REQUIRED", "true")
	fx := NewTestFixture()
	defer fx.Close()

	for path, want := range map[string]int{
		"/api/vms":    http.StatusUnauthorized,
		"/api/whoami": http.StatusOK,
		"/healthz":    http.StatusOK,
		"/":           http.StatusOK,
	} {
		resp := mustGet(t, fx.Server.URL+path)
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s: status = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

// The admin gate runs before the activity tracker: a rejected mutation is not
// user activity, an accepted request is.
func TestPipeline_RejectedRequestsAreNotActivity(t *testing.T) {
	t.Setenv("CORRAL_ADMINS", "alice@example.com")
	fx := NewTestFixture()
	defer fx.Close()

	old := time.Now().Add(-5 * time.Hour).UnixNano()
	lastActivity.Store(old)
	resp := mustPost(t, fx.Server.URL+"/api/vms", "{}")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if lastActivity.Load() != old {
		t.Error("a request the admin gate rejected counted as activity")
	}

	resp = mustGet(t, fx.Server.URL+"/api/whoami")
	_ = resp.Body.Close()
	if lastActivity.Load() == old {
		t.Error("an accepted request did not count as activity")
	}
}

// The peer gate runs after the admin gate: a non-admin cannot mutate a peer's
// VM, and a malformed peer namespace is refused before the route table.
func TestPipeline_PeerGateOrdering(t *testing.T) {
	t.Run("admin gate first", func(t *testing.T) {
		t.Setenv("CORRAL_ADMINS", "alice@example.com")
		fx := NewTestFixture()
		defer fx.Close()
		resp := mustPost(t, fx.Server.URL+"/api/vms/peer-bm9wZQ/vm/start", "{}")
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})
	t.Run("invalid peer target", func(t *testing.T) {
		fx := NewTestFixture()
		defer fx.Close()
		for _, path := range []string{
			"/api/vms/peer-bm9wZQ/vm/guestinfo", // decodes, but no NUL separator
			"/api/vms/peer-!!/vm",               // not base64
		} {
			resp := mustGet(t, fx.Server.URL+path)
			body := readBody(t, resp)
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid Corral peer target") {
				t.Errorf("GET %s: %d %s, want 400 invalid Corral peer target", path, resp.StatusCode, body)
			}
		}
	})
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
