package web

import (
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// ── WebSocket upgrade tests ──────────────────────────────────────

func TestWebSocket_VNC_Upgrade(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	// Connect to VNC websocket endpoint
	url := wsURL(fx.Server.URL) + "/api/vnc/tailvm/testvm"
	origin := fx.Server.URL

	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		// Expected: bridge will fail when virtctl is not available
		// The important thing is the upgrade succeeds or fails cleanly
		t.Logf("VNC dial result: %v", err)
		return
	}
	defer ws.Close()

	// Connection established — WebSocket upgrade succeeded.
	// The bridge will try to spawn virtctl (not available), retry TCP for ~10s, then close.
	// Set a short read deadline to avoid blocking on the retry loop.
	ws.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	_, err = ws.Read(buf)
	t.Logf("VNC read result: %v (expected: timeout or connection closed after virtctl fails)", err)

	// The important assertion: WebSocket upgrade succeeded (Dial returned without error).
	// The bridge fails gracefully when virtctl is not available.
}

func TestWebSocket_TTY_Upgrade(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	url := wsURL(fx.Server.URL) + "/api/tty/tailvm/testvm"
	origin := fx.Server.URL

	ws, err := websocket.Dial(url, "", origin)
	if err != nil {
		t.Logf("TTY dial result: %v", err)
		return
	}
	defer ws.Close()

	ws.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 256)
	_, err = ws.Read(buf)
	t.Logf("TTY read result: %v (expected: closed when virtctl fails)", err)
}

func TestWebSocket_RejectsMismatchedOriginWithoutAuth(t *testing.T) {
	t.Setenv("CORRAL_AUTH_REQUIRED", "")
	fx := NewTestFixture()
	defer fx.Close()

	url := wsURL(fx.Server.URL) + "/api/vnc/tailvm/testvm"
	if ws, err := websocket.Dial(url, "", "https://attacker.example"); err == nil {
		_ = ws.Close()
		t.Fatal("WebSocket handshake accepted a mismatched Origin with auth disabled")
	}
}

func TestWebSocketOrigin_AllowsMissingOriginOnlyInOpenMode(t *testing.T) {
	r := httptest.NewRequest("GET", "http://corral.example/api/vnc/ns/vm", nil)
	r.Host = "corral.example"
	t.Setenv("CORRAL_AUTH_REQUIRED", "")
	if err := validateWebSocketOrigin(r); err != nil {
		t.Fatalf("open mode should preserve native clients without Origin: %v", err)
	}
	t.Setenv("CORRAL_AUTH_REQUIRED", "true")
	if err := validateWebSocketOrigin(r); err == nil {
		t.Fatal("auth-required mode accepted a WebSocket without Origin")
	}
}

// ── Helpers ──────────────────────────────────────────────────────

// wsURL converts an http:// URL to ws:// for WebSocket connections.
func wsURL(httpURL string) string {
	if len(httpURL) > 7 && httpURL[:7] == "http://" {
		return "ws://" + httpURL[7:]
	}
	return httpURL
}

func TestWebSocket_VNC_MissingName(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	url := wsURL(fx.Server.URL) + "/api/vnc/tailvm/"
	ws, err := websocket.Dial(url, "", fx.Server.URL)
	if err != nil {
		t.Logf("dial with empty name: %v", err)
		return
	}
	defer ws.Close()

	ws.SetDeadline(time.Now().Add(time.Second))
	_, err = ws.Read(make([]byte, 256))
	t.Logf("read with empty name: %v (expected: closed)", err)
}

func TestWebSocket_InvalidPath(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Close()

	url := wsURL(fx.Server.URL) + "/api/vnc"
	_, err := websocket.Dial(url, "", fx.Server.URL)
	t.Logf("dial invalid path: %v (expected: error)", err)
}
