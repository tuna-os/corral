package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllowedHostPolicyRejectsDNSRebindingHost(t *testing.T) {
	called := false
	handler, err := enforceAllowedHosts(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}), []string{"127.0.0.1:8006"})
	if err != nil {
		t.Fatal(err)
	}

	// A browser can resolve attacker.example to loopback while preserving this
	// Host header. The loopback listener's pinned Host policy must reject it.
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8006/", nil)
	r.Host = "attacker.example:8006"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("DNS-rebinding Host status = %d, want %d", w.Code, http.StatusMisdirectedRequest)
	}
	if called {
		t.Fatal("handler ran for a Host outside the allowlist")
	}
}

func TestAllowedHostPolicyAcceptsPinnedLoopbackHost(t *testing.T) {
	handler, err := enforceAllowedHosts(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), []string{"127.0.0.1:8006"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8006/", nil)
	r.Host = "127.0.0.1:8006"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("pinned loopback Host status = %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestLoopbackAllowedHostsUsesBoundPort(t *testing.T) {
	listener := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43127}
	got := loopbackAllowedHosts("127.0.0.1:0", listener)
	if len(got) != 1 || got[0] != "127.0.0.1:43127" {
		t.Fatalf("loopbackAllowedHosts = %#v, want [127.0.0.1:43127]", got)
	}
}
