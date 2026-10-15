package main

// End-to-end passkey ceremonies against the real go-webauthn verifier, driven
// by a software authenticator (P-256, "none" attestation). passkeys_test.go
// covers gating and ceremony bookkeeping; these prove that a correctly signed
// login produces a session the gateway forwards as an identity, and that a
// forged or replayed one does not.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wa "github.com/go-webauthn/webauthn/webauthn"
)

const (
	testRPID   = "corral.example.com"
	testOrigin = "https://corral.example.com"
)

var b64 = base64.RawURLEncoding

// ── minimal CBOR encoder: just what attestation objects and COSE keys use ──

func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n < 1<<8:
		return []byte{major<<5 | 24, byte(n)}
	case n < 1<<16:
		return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(n))
	default:
		return binary.BigEndian.AppendUint32([]byte{major<<5 | 26}, uint32(n))
	}
}

func cborInt(v int) []byte {
	if v >= 0 {
		return cborHead(0, uint64(v))
	}
	return cborHead(1, uint64(-1-v))
}
func cborBytes(b []byte) []byte { return append(cborHead(2, uint64(len(b))), b...) }
func cborText(s string) []byte  { return append(cborHead(3, uint64(len(s))), s...) }
func cborMap(kv ...[]byte) []byte {
	return append(cborHead(5, uint64(len(kv)/2)), bytes.Join(kv, nil)...)
}

// softAuthenticator is one resident ES256 credential.
type softAuthenticator struct {
	key    *ecdsa.PrivateKey
	credID []byte
	userID []byte
	count  uint32
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softAuthenticator{key: key, credID: id}
}

func (a *softAuthenticator) authData(flags byte, attested []byte) []byte {
	rp := sha256.Sum256([]byte(testRPID))
	a.count++
	out := append(rp[:], flags)
	out = binary.BigEndian.AppendUint32(out, a.count)
	return append(out, attested...)
}

func clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]string{"type": typ, "challenge": challenge, "origin": testOrigin})
	return b
}

// create answers navigator.credentials.create for the begin response.
func (a *softAuthenticator) create(t *testing.T, publicKey map[string]any) []byte {
	t.Helper()
	challenge, _ := publicKey["challenge"].(string)
	user, _ := publicKey["user"].(map[string]any)
	uid, _ := user["id"].(string)
	var err error
	if a.userID, err = b64.DecodeString(uid); err != nil {
		t.Fatalf("user.id %q: %v", uid, err)
	}
	pub := a.key.PublicKey
	x, y := make([]byte, 32), make([]byte, 32)
	pub.X.FillBytes(x)
	pub.Y.FillBytes(y)
	cose := cborMap(cborInt(1), cborInt(2), cborInt(3), cborInt(-7), cborInt(-1), cborInt(1), cborInt(-2), cborBytes(x), cborInt(-3), cborBytes(y))
	attested := append(make([]byte, 16), byte(len(a.credID)>>8), byte(len(a.credID)))
	attested = append(append(attested, a.credID...), cose...)
	const up, uv, at = 0x01, 0x04, 0x40
	att := cborMap(cborText("fmt"), cborText("none"), cborText("attStmt"), cborMap(), cborText("authData"), cborBytes(a.authData(up|uv|at, attested)))
	body, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]string{
			"clientDataJSON":    b64.EncodeToString(clientData("webauthn.create", challenge)),
			"attestationObject": b64.EncodeToString(att),
		},
		"clientExtensionResults": map[string]any{},
	})
	return body
}

// get answers navigator.credentials.get; tamper flips a signature bit.
func (a *softAuthenticator) get(t *testing.T, publicKey map[string]any, tamper bool) []byte {
	t.Helper()
	challenge, _ := publicKey["challenge"].(string)
	cd := clientData("webauthn.get", challenge)
	const up, uv = 0x01, 0x04
	ad := a.authData(up|uv, nil)
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if tamper {
		sig[len(sig)-1] ^= 0x01
	}
	body, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]string{
			"clientDataJSON":    b64.EncodeToString(cd),
			"authenticatorData": b64.EncodeToString(ad),
			"signature":         b64.EncodeToString(sig),
			"userHandle":        b64.EncodeToString(a.userID),
		},
		"clientExtensionResults": map[string]any{},
	})
	return body
}

// passkeyGateway is the full gateway handler (passkey routes + proxy) in
// front of an upstream that echoes the identity headers it receives.
type passkeyGateway struct {
	url    string
	store  string
	pm     *passkeyManager
	client *http.Client
}

func newPasskeyGateway(t *testing.T) *passkeyGateway {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Tailscale-User-Login")+"\n"+r.Header.Get("Tailscale-User-Name"))
	}))
	t.Cleanup(upstream.Close)
	g, err := newGateway(context.Background(), "", "", "", "", upstream.URL, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "passkeys.json")
	if g.passkeys, err = newPasskeyManager(store, testRPID, testOrigin, "bootstrap-token"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g.handler())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &passkeyGateway{url: srv.URL, store: store, pm: g.passkeys, client: client}
}

type beginResp struct {
	Ceremony  string         `json:"ceremony"`
	PublicKey map[string]any `json:"publicKey"`
}

func (p *passkeyGateway) post(t *testing.T, path string, hdr map[string]string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, p.url+path, bytes.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func (p *passkeyGateway) begin(t *testing.T, path string, hdr map[string]string) beginResp {
	t.Helper()
	resp, out := p.post(t, path, hdr, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %s", path, resp.StatusCode, out)
	}
	var b beginResp
	if err := json.Unmarshal(out, &b); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return b
}

func (p *passkeyGateway) enroll(t *testing.T, a *softAuthenticator, user string) {
	t.Helper()
	b := p.begin(t, "/auth/passkey/enroll/begin?user="+user, map[string]string{"X-Corral-Enroll-Token": "bootstrap-token"})
	resp, out := p.post(t, "/auth/passkey/enroll/finish?ceremony="+b.Ceremony, nil, a.create(t, b.PublicKey))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll finish: %d %s", resp.StatusCode, out)
	}
}

func (p *passkeyGateway) whoami(t *testing.T) (int, string) {
	t.Helper()
	resp, err := p.client.Get(p.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func TestPasskey_EnrollThenLoginForwardsIdentity(t *testing.T) {
	p := newPasskeyGateway(t)
	a := newSoftAuthenticator(t)

	if code, _ := p.whoami(t); code != http.StatusFound {
		t.Fatalf("anonymous request = %d, want a redirect to the passkey page", code)
	}
	p.enroll(t, a, "alice")

	// Enrollment persists the credential: a restarted gateway still knows it.
	reloaded, err := newPasskeyManager(p.store, testRPID, testOrigin, "")
	if err != nil {
		t.Fatal(err)
	}
	if u := reloaded.users["alice"]; u == nil || len(u.Credentials) != 1 || !bytes.Equal(u.Credentials[0].ID, a.credID) {
		t.Fatalf("store after enroll = %+v, want alice with the new credential", reloaded.users["alice"])
	}
	if code, _ := p.whoami(t); code != http.StatusFound {
		t.Fatal("enrolling with the bootstrap token must not sign the browser in")
	}

	b := p.begin(t, "/auth/passkey/login/begin", nil)
	resp, out := p.post(t, "/auth/passkey/login/finish?ceremony="+b.Ceremony, nil, a.get(t, b.PublicKey, false))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login finish: %d %s", resp.StatusCode, out)
	}
	code, body := p.whoami(t)
	want := "passkey|" + b64.EncodeToString(a.userID) + "\nalice"
	if code != http.StatusOK || body != want {
		t.Fatalf("after login: %d %q, want 200 with identity %q", code, body, want)
	}

	// The sign counter is saved, so a cloned authenticator replaying an old
	// counter can be told apart.
	reloaded, err = newPasskeyManager(p.store, testRPID, testOrigin, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.users["alice"].Credentials[0].Authenticator.SignCount; got != a.count {
		t.Errorf("stored sign count = %d, want %d", got, a.count)
	}
}

func TestPasskey_ForgedSignatureIsRejected(t *testing.T) {
	p := newPasskeyGateway(t)
	a := newSoftAuthenticator(t)
	p.enroll(t, a, "alice")

	b := p.begin(t, "/auth/passkey/login/begin", nil)
	resp, _ := p.post(t, "/auth/passkey/login/finish?ceremony="+b.Ceremony, nil, a.get(t, b.PublicKey, true))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged assertion = %d, want 401", resp.StatusCode)
	}
	if code, _ := p.whoami(t); code != http.StatusFound {
		t.Fatal("a forged assertion produced a session")
	}
}

func TestPasskey_UnknownCredentialIsRejected(t *testing.T) {
	p := newPasskeyGateway(t)
	enrolled := newSoftAuthenticator(t)
	p.enroll(t, enrolled, "alice")

	stranger := newSoftAuthenticator(t)
	stranger.userID = []byte("not-a-user-handle")
	b := p.begin(t, "/auth/passkey/login/begin", nil)
	resp, _ := p.post(t, "/auth/passkey/login/finish?ceremony="+b.Ceremony, nil, stranger.get(t, b.PublicKey, false))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown credential = %d, want 401", resp.StatusCode)
	}
}

func TestPasskey_LoginAssertionCannotBeReplayed(t *testing.T) {
	p := newPasskeyGateway(t)
	a := newSoftAuthenticator(t)
	p.enroll(t, a, "alice")

	b := p.begin(t, "/auth/passkey/login/begin", nil)
	assertion := a.get(t, b.PublicKey, false)
	if resp, out := p.post(t, "/auth/passkey/login/finish?ceremony="+b.Ceremony, nil, assertion); resp.StatusCode != http.StatusOK {
		t.Fatalf("first finish: %d %s", resp.StatusCode, out)
	}
	// A captured assertion replayed by another client: the ceremony is gone.
	other := &http.Client{}
	resp, err := other.Post(p.url+"/auth/passkey/login/finish?ceremony="+b.Ceremony, "application/json", bytes.NewReader(assertion))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("replayed assertion = %d, want 400", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("replay set cookies: %v", resp.Cookies())
	}
}

func TestPasskey_EnrollFromSignedInSessionUsesSessionName(t *testing.T) {
	p := newPasskeyGateway(t)
	a := newSoftAuthenticator(t)
	p.enroll(t, a, "alice")
	b := p.begin(t, "/auth/passkey/login/begin", nil)
	if resp, out := p.post(t, "/auth/passkey/login/finish?ceremony="+b.Ceremony, nil, a.get(t, b.PublicKey, false)); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, out)
	}

	// A signed-in user adds a second passkey with no token, and ?user= cannot
	// enroll it under someone else's name.
	second := newSoftAuthenticator(t)
	b = p.begin(t, "/auth/passkey/enroll/begin?user=mallory", nil)
	if resp, out := p.post(t, "/auth/passkey/enroll/finish?ceremony="+b.Ceremony, nil, second.create(t, b.PublicKey)); resp.StatusCode != http.StatusOK {
		t.Fatalf("second enroll: %d %s", resp.StatusCode, out)
	}
	p.pm.mu.Lock()
	mallory, aliceKeys := p.pm.users["mallory"], len(p.pm.users["alice"].Credentials)
	p.pm.mu.Unlock()
	if mallory != nil {
		t.Fatal("?user= overrode the signed-in identity during enrollment")
	}
	if aliceKeys != 2 {
		t.Errorf("alice has %d credentials, want 2", aliceKeys)
	}
}

func TestPasskeyPage_ServesHTML(t *testing.T) {
	p := newPasskeyGateway(t)
	resp, err := http.Get(p.url + "/auth/passkey")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
		!strings.Contains(string(out), "/auth/passkey/") {
		t.Errorf("passkey page = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestNewPasskeyManager_RejectsCorruptStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passkeys.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newPasskeyManager(path, testRPID, testOrigin, ""); err == nil || !strings.Contains(err.Error(), "passkey store") {
		t.Errorf("corrupt store = %v, want a passkey store error (never silently drop enrolled keys)", err)
	}
	if _, err := newPasskeyManager(path, "", testOrigin, ""); err == nil {
		t.Error("missing RP ID must be refused")
	}
}

// Anyone can call login/begin without signing in. Each call stores a
// ceremony, so expired ones must be dropped, or the map grows without bound.
func TestPasskeyCeremonies_ExpiredOnesArePruned(t *testing.T) {
	p := newPasskeyGateway(t)
	p.pm.mu.Lock()
	for range 100 {
		id := p.pm.ceremony("login", "", &wa.SessionData{})
		c := p.pm.ceremonies[id]
		c.Expires = time.Now().Add(-time.Second)
		p.pm.ceremonies[id] = c
	}
	p.pm.mu.Unlock()

	b := p.begin(t, "/auth/passkey/login/begin", nil)
	p.pm.mu.Lock()
	n := len(p.pm.ceremonies)
	_, live := p.pm.ceremonies[b.Ceremony]
	p.pm.mu.Unlock()
	if n != 1 || !live {
		t.Fatalf("ceremonies after a new begin = %d (new one present: %v), want only the live one", n, live)
	}
}
