package web

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func readyConsoleServer(t *testing.T, vmContext string) (*Server, string, <-chan struct{}, context.CancelFunc) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORRAL_AUTH_REQUIRED", "")
	t.Setenv("CORRAL_ADMINS", "")
	srv, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	page, ready, err := srv.NotifyConsoleReady(ctx, "local", "desk", vmContext)
	if err != nil {
		cancel()
		_ = srv.Close()
		t.Fatal(err)
	}
	// Exercise the real listener, Host policy, request pipeline and route table,
	// without starting unrelated metric samplers in these HTTP boundary tests.
	httpServer := &http.Server{Handler: srv.handler}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = httpServer.Serve(srv.listener)
	}()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		_ = httpServer.Close()
		<-stopped
	})
	return srv, page, ready, cancel
}

func readinessBody(t *testing.T, page, vmContext string) string {
	t.Helper()
	u, err := url.Parse(page)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"nonce": u.Query().Get(consoleReadyQuery), "context": vmContext})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func readinessRequest(t *testing.T, srv *Server, method, path, origin, host, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if host != "" {
		req.Host = host
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

func assertNotConsoleReady(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
		t.Fatal("readiness was reported without an authentic RFB report")
	default:
	}
}

func TestConsoleReadiness_HTTPRejectsForeignIdentityAndMalformedReports(t *testing.T) {
	srv, page, ready, _ := readyConsoleServer(t, "lab")
	body := readinessBody(t, page, "lab")
	u, _ := url.Parse(srv.URL())
	const path = "/api/console-ready/local/desk"
	for _, tc := range []struct {
		name, method, path, origin, host, body string
		want                                   int
	}{
		{"missing origin", "POST", path, "", "", body, http.StatusForbidden},
		{"foreign origin", "POST", path, "https://attacker.example", "", body, http.StatusForbidden},
		{"null origin", "POST", path, "null", "", body, http.StatusForbidden},
		{"wrong origin port", "POST", path, "http://127.0.0.1:1", "", body, http.StatusForbidden},
		{"foreign Host", "POST", path, srv.URL(), "attacker.example", body, http.StatusMisdirectedRequest},
		{"allowed alias is not bound origin", "POST", path, srv.URL(), "localhost:" + u.Port(), body, http.StatusForbidden},
		{"wrong nonce", "POST", path, srv.URL(), "", `{"nonce":"wrong","context":"lab"}`, http.StatusForbidden},
		{"wrong VM", "POST", "/api/console-ready/local/other", srv.URL(), "", body, http.StatusForbidden},
		{"wrong namespace", "POST", "/api/console-ready/other/desk", srv.URL(), "", body, http.StatusForbidden},
		{"wrong context", "POST", path, srv.URL(), "", readinessBody(t, page, "other"), http.StatusForbidden},
		{"missing context", "POST", path, srv.URL(), "", `{"nonce":"` + mustReadyNonce(t, page) + `"}`, http.StatusForbidden},
		{"query not body", "POST", path + "?nonce=x", srv.URL(), "", body, http.StatusBadRequest},
		{"unknown field", "POST", path, srv.URL(), "", strings.TrimSuffix(body, "}") + `,"callback":"https://attacker.example"}`, http.StatusBadRequest},
		{"trailing JSON", "POST", path, srv.URL(), "", body + `{}`, http.StatusForbidden},
		{"oversized body", "POST", path, srv.URL(), "", `{"nonce":"` + strings.Repeat("a", 9000) + `","context":"lab"}`, http.StatusBadRequest},
		{"malformed JSON", "POST", path, srv.URL(), "", `{`, http.StatusBadRequest},
		{"GET cannot report", "GET", path, srv.URL(), "", body, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, response := readinessRequest(t, srv, tc.method, tc.path, tc.origin, tc.host, tc.body)
			if status != tc.want {
				t.Fatalf("status = %d, want %d: %s", status, tc.want, response)
			}
			assertNotConsoleReady(t, ready)
		})
	}
	status, response := readinessRequest(t, srv, "POST", path, srv.URL(), "", body)
	if status != http.StatusNoContent {
		t.Fatalf("authentic report = %d: %s", status, response)
	}
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("authentic report did not signal readiness")
	}
	if status, _ := readinessRequest(t, srv, "POST", path, srv.URL(), "", body); status != http.StatusConflict {
		t.Fatalf("duplicate report = %d, want 409", status)
	}
}

func mustReadyNonce(t *testing.T, page string) string {
	t.Helper()
	u, err := url.Parse(page)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(consoleReadyQuery)
}

func TestConsoleReadiness_PageIsBoundAndDoesNotSignal(t *testing.T) {
	srv, page, ready, _ := readyConsoleServer(t, "lab")
	u, _ := url.Parse(page)
	nonce := mustReadyNonce(t, page)
	decoded, err := base64.RawURLEncoding.DecodeString(nonce)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("nonce is not 256 bits of base64url: %q (%v)", nonce, err)
	}
	for _, tc := range []struct {
		name, path string
		want       int
		withNonce  bool
	}{
		{"owned page", u.RequestURI(), http.StatusOK, true},
		{"ordinary page", "/console/local/desk?context=lab", http.StatusOK, false},
		{"other ordinary VM", "/console/local/other?context=lab", http.StatusOK, false},
		{"wrong VM", strings.Replace(u.RequestURI(), "/desk?", "/other?", 1), http.StatusForbidden, false},
		{"wrong namespace", strings.Replace(u.RequestURI(), "/local/", "/other/", 1), http.StatusForbidden, false},
		{"wrong context", strings.Replace(u.RequestURI(), "context=lab", "context=other", 1), http.StatusForbidden, false},
		{"wrong nonce", strings.Replace(u.RequestURI(), nonce, "wrong", 1), http.StatusForbidden, false},
		{"duplicate nonce", u.RequestURI() + "&ready_nonce=" + nonce, http.StatusForbidden, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := readinessRequest(t, srv, "GET", tc.path, "", "", "")
			if status != tc.want || strings.Contains(body, `data-ready-nonce="`+nonce+`"`) != tc.withNonce {
				t.Fatalf("page = %d, want %d (nonce %v): %s", status, tc.want, tc.withNonce, body)
			}
			assertNotConsoleReady(t, ready)
		})
	}
}

func TestConsoleReadiness_CancellationRetiresPageAndReport(t *testing.T) {
	srv, page, ready, cancel := readyConsoleServer(t, "")
	cancel()
	u, _ := url.Parse(page)
	if status, _ := readinessRequest(t, srv, "GET", u.RequestURI(), "", "", ""); status != http.StatusGone {
		t.Fatalf("late page = %d, want 410", status)
	}
	if status, _ := readinessRequest(t, srv, "POST", "/api/console-ready/local/desk", srv.URL(), "", readinessBody(t, page, "")); status != http.StatusGone {
		t.Fatalf("late report = %d, want 410", status)
	}
	assertNotConsoleReady(t, ready)
}

func TestConsoleReadiness_NonceIsPerListener(t *testing.T) {
	_, first, firstReady, _ := readyConsoleServer(t, "")
	secondServer, second, secondReady, _ := readyConsoleServer(t, "")
	if mustReadyNonce(t, first) == mustReadyNonce(t, second) {
		t.Fatal("listeners reused a readiness nonce")
	}
	if status, _ := readinessRequest(t, secondServer, "POST", "/api/console-ready/local/desk", secondServer.URL(), "", readinessBody(t, first, "")); status != http.StatusForbidden {
		t.Fatalf("another listener's nonce was accepted: %d", status)
	}
	assertNotConsoleReady(t, firstReady)
	assertNotConsoleReady(t, secondReady)
}

type failingReadyEntropy struct{}

func (failingReadyEntropy) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestConsoleReadiness_ConfigurationFailsClosed(t *testing.T) {
	var readiness consoleReadiness
	if page, ready, err := readiness.configure(context.Background(), "http://127.0.0.1:1234", "local", "desk", "", failingReadyEntropy{}); !errors.Is(err, io.ErrUnexpectedEOF) || page != "" || ready != nil {
		t.Fatalf("entropy failure = %q, %v, %v", page, ready, err)
	}
	if binding, _ := readiness.current(); binding != nil {
		t.Fatal("entropy failure installed a binding")
	}
	for _, identity := range [][3]string{{"", "desk", ""}, {"local", "../desk", ""}, {"local", "desk", strings.Repeat("x", 1025)}, {"local", "desk", string([]byte{0xff})}} {
		if _, _, err := readiness.configure(context.Background(), "http://127.0.0.1:1234", identity[0], identity[1], identity[2], strings.NewReader(strings.Repeat("x", 32))); err == nil {
			t.Fatalf("accepted invalid identity %q", identity)
		}
	}
	readiness.retire()
	if _, _, err := readiness.configure(context.Background(), "http://127.0.0.1:1234", "local", "desk", "", strings.NewReader(strings.Repeat("x", 32))); err == nil {
		t.Fatal("retired readiness was reconfigured")
	}
}

func TestConsoleReadiness_WebSocketUpgradeIsNotRFBReadiness(t *testing.T) {
	previous := demoEnabled
	demoEnabled = true // Existing RFB fixture; deliberately never finish RFB.
	t.Cleanup(func() { demoEnabled = previous })
	srv, _, ready, _ := readyConsoleServer(t, "")
	ws, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL(), "http")+"/api/vnc/local/desk", "", srv.URL())
	if err != nil {
		t.Fatalf("websocket upgrade: %v", err)
	}
	defer func() { _ = ws.Close() }()
	if err := ws.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	version := make([]byte, 12)
	if _, err := io.ReadFull(ws, version); err != nil || string(version) != "RFB 003.008\n" {
		t.Fatalf("initial RFB version = %q: %v", version, err)
	}
	assertNotConsoleReady(t, ready)
}

func TestConsoleReadiness_StalledBodyIsBounded(t *testing.T) {
	srv, _, ready, _ := readyConsoleServer(t, "")
	u, _ := url.Parse(srv.URL())
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Leave the JSON unfinished on a real connection. The handler's five-second
	// deadline, not the client or a fake decoder, must end this request.
	if _, err := fmt.Fprintf(conn, "POST /api/console-ready/local/desk HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nContent-Type: application/json\r\nContent-Length: 128\r\nConnection: close\r\n\r\n{", u.Host, srv.URL()); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("stalled request did not receive a bounded response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("stalled body = %d, want 400", resp.StatusCode)
	}
	assertNotConsoleReady(t, ready)
}
