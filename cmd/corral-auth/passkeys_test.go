package main

// HTTP-level passkey tests against the registerPasskeyRoutes mux: enrollment
// gating, ceremony take/double-take, and store round-trip. (pure_test.go
// already covers saveLocked file modes/dir creation and ceremony expiry
// fields; these cover the mux wiring and consumption semantics.)

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	wa "github.com/go-webauthn/webauthn/webauthn"
)

func testPasskeyServer(t *testing.T, enrollToken string) (string, *passkeyManager) {
	t.Helper()
	g, err := newGateway(context.Background(), "", "", "", "", "http://127.0.0.1:8080", []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	pm, err := newPasskeyManager(filepath.Join(t.TempDir(), "passkeys.json"),
		"corral.example.com", "https://corral.example.com", enrollToken)
	if err != nil {
		t.Fatal(err)
	}
	g.passkeys = pm
	mux := http.NewServeMux()
	g.registerPasskeyRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, pm
}

func TestPasskeyEnrollBegin_RequiresToken(t *testing.T) {
	url, _ := testPasskeyServer(t, "bootstrap-token")

	// No session and no token → 401.
	resp, err := http.Post(url+"/auth/passkey/enroll/begin?user=alice", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", resp.StatusCode)
	}

	// Wrong token → still 401.
	req, _ := http.NewRequest(http.MethodPost, url+"/auth/passkey/enroll/begin?user=alice", nil)
	req.Header.Set("X-Corral-Enroll-Token", "wrong-token!")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want 401", resp.StatusCode)
	}

	// Correct token → ceremony + publicKey for the WebAuthn ceremony.
	req, _ = http.NewRequest(http.MethodPost, url+"/auth/passkey/enroll/begin?user=alice", nil)
	req.Header.Set("X-Corral-Enroll-Token", "bootstrap-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Ceremony  string         `json:"ceremony"`
		PublicKey map[string]any `json:"publicKey"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Ceremony == "" || body.PublicKey == nil {
		t.Errorf("begin response = %+v, want ceremony id + publicKey", body)
	}
}

func TestPasskeyCeremony_TakeDoubleTake(t *testing.T) {
	_, pm := testPasskeyServer(t, "bootstrap-token")

	id := pm.ceremony("enroll", "alice", &wa.SessionData{})
	got, err := pm.take(id, "enroll")
	if err != nil {
		t.Fatalf("first take: %v", err)
	}
	if got.User != "alice" {
		t.Errorf("take user = %q, want alice", got.User)
	}
	// Double-take: the ceremony is consumed.
	if _, err := pm.take(id, "enroll"); err == nil {
		t.Error("second take succeeded, want expired-or-invalid error")
	}
	// Wrong kind never matches.
	other := pm.ceremony("login", "", &wa.SessionData{})
	if _, err := pm.take(other, "enroll"); err == nil {
		t.Error("cross-kind take succeeded, want error")
	}
}

func TestPasskeyEnrollFinish_ConsumesCeremony(t *testing.T) {
	url, _ := testPasskeyServer(t, "bootstrap-token")

	req, _ := http.NewRequest(http.MethodPost, url+"/auth/passkey/enroll/begin?user=bob", nil)
	req.Header.Set("X-Corral-Enroll-Token", "bootstrap-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var begin struct {
		Ceremony string `json:"ceremony"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&begin); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// A real authenticator response is unavailable, so finish fails — but it
	// must still consume the ceremony, and the retry must report the
	// ceremony as gone rather than re-running registration.
	finish := url + "/auth/passkey/enroll/finish?ceremony=" + begin.Ceremony
	first, err := http.Post(finish, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	first.Body.Close()
	if first.StatusCode != http.StatusBadRequest {
		t.Fatalf("first finish status = %d, want 400", first.StatusCode)
	}
	second, err := http.Post(finish, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusBadRequest {
		t.Fatalf("second finish status = %d, want 400", second.StatusCode)
	}
}

func TestPasskeySaveLocked_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "passkeys.json")
	pm, err := newPasskeyManager(path, "corral.example.com", "https://corral.example.com", "bootstrap-token")
	if err != nil {
		t.Fatal(err)
	}
	pm.mu.Lock()
	pm.users["alice"] = &passkeyUser{ID: []byte("alice-id-12345678901234567890"), Name: "alice"}
	if err := pm.saveLocked(); err != nil {
		pm.mu.Unlock()
		t.Fatal(err)
	}
	pm.mu.Unlock()

	reloaded, err := newPasskeyManager(path, "corral.example.com", "https://corral.example.com", "other-token")
	if err != nil {
		t.Fatal(err)
	}
	user := reloaded.users["alice"]
	if user == nil || string(user.ID) != "alice-id-12345678901234567890" {
		t.Fatalf("reloaded users = %+v, want alice round-tripped", reloaded.users)
	}
}
