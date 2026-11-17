package main

// Regression tests for how the gateway binds passkeys to identities, how long
// a session lasts, and what reaches corral web.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"github.com/tg123/go-htpasswd"
	"golang.org/x/crypto/bcrypt"
)

const testCookieKey = "01234567890123456789012345678901"

// sessionCookie signs a gateway session for an identity that did not come
// from a passkey, the way callback stores an OIDC one.
func sessionCookie(t *testing.T, login, name string) *http.Cookie {
	t.Helper()
	store := sessions.NewCookieStore([]byte(testCookieKey))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	s, _ := store.Get(req, "corral_oidc")
	s.Values = signedIn(login, name, "")
	rec := httptest.NewRecorder()
	if err := s.Save(req, rec); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "corral_oidc", Value: rec.Result().Cookies()[0].Value, Path: "/"}
}

func TestPasskey_SessionDisplayNameCannotEnrollOntoAnotherUser(t *testing.T) {
	p := newPasskeyGateway(t)
	alice := newSoftAuthenticator(t)
	p.enroll(t, alice, "alice")
	aliceID := append([]byte{}, alice.userID...)

	// Mallory is another identity (a different OIDC subject) whose display
	// name claim reads "alice".
	u, _ := url.Parse(p.url)
	p.client.Jar.SetCookies(u, []*http.Cookie{sessionCookie(t, "https://idp.example|mallory", "alice")})
	mallory := newSoftAuthenticator(t)
	b := p.begin(t, "/auth/passkey/enroll/begin", nil)
	if resp, out := p.post(t, "/auth/passkey/enroll/finish?ceremony="+b.Ceremony, nil, mallory.create(t, b.PublicKey)); resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll finish: %d %s", resp.StatusCode, out)
	}
	p.pm.mu.Lock()
	keys := len(p.pm.users["alice"].Credentials)
	own := p.pm.users["https://idp.example|mallory"]
	p.pm.mu.Unlock()
	if keys != 1 {
		t.Errorf("alice has %d credentials after another identity enrolled, want 1", keys)
	}
	if own == nil || len(own.Credentials) != 1 || own.Display != "alice" {
		t.Errorf("mallory's passkey user = %+v, want one keyed on her login with display name alice", own)
	}

	// Mallory's authenticator must not sign in as alice either.
	p.client.Jar, _ = cookiejar.New(nil)
	b = p.begin(t, "/auth/passkey/login/begin", nil)
	p.post(t, "/auth/passkey/login/finish?ceremony="+b.Ceremony, nil, mallory.get(t, b.PublicKey, false))
	if _, body := p.whoami(t); strings.HasPrefix(body, "passkey|"+b64.EncodeToString(aliceID)) {
		t.Errorf("another identity's authenticator signed in as alice: %q", body)
	}
}

func TestPasskey_BootstrapTokenCannotAddCredentialToEnrolledUser(t *testing.T) {
	p := newPasskeyGateway(t)
	alice := newSoftAuthenticator(t)
	p.enroll(t, alice, "alice")

	intruder := newSoftAuthenticator(t)
	resp, out := p.post(t, "/auth/passkey/enroll/begin?user=alice", map[string]string{"X-Corral-Enroll-Token": "bootstrap-token"}, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("token enroll for an enrolled user = %d, want 409", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusOK {
		var b beginResp
		if err := json.Unmarshal(out, &b); err != nil {
			t.Fatal(err)
		}
		p.post(t, "/auth/passkey/enroll/finish?ceremony="+b.Ceremony, nil, intruder.create(t, b.PublicKey))
	}
	p.pm.mu.Lock()
	keys := len(p.pm.users["alice"].Credentials)
	p.pm.mu.Unlock()
	if keys != 1 {
		t.Fatalf("the bootstrap token added a credential to an enrolled user: alice has %d", keys)
	}
}

// Two token ceremonies for the same new user: only the first may finish.
func TestPasskey_BootstrapTokenRaceEnrollsOnce(t *testing.T) {
	p := newPasskeyGateway(t)
	hdr := map[string]string{"X-Corral-Enroll-Token": "bootstrap-token"}
	first := p.begin(t, "/auth/passkey/enroll/begin?user=bob", hdr)
	second := p.begin(t, "/auth/passkey/enroll/begin?user=bob", hdr)
	a, b := newSoftAuthenticator(t), newSoftAuthenticator(t)
	if resp, out := p.post(t, "/auth/passkey/enroll/finish?ceremony="+first.Ceremony, nil, a.create(t, first.PublicKey)); resp.StatusCode != http.StatusOK {
		t.Fatalf("first finish: %d %s", resp.StatusCode, out)
	}
	if resp, _ := p.post(t, "/auth/passkey/enroll/finish?ceremony="+second.Ceremony, nil, b.create(t, second.PublicKey)); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second token finish = %d, want 409", resp.StatusCode)
	}
}

func TestPasskey_TokenNameCannotSpellALogin(t *testing.T) {
	p := newPasskeyGateway(t)
	resp, _ := p.post(t, "/auth/passkey/enroll/begin?user="+url.QueryEscape("basic|alice"), map[string]string{"X-Corral-Enroll-Token": "bootstrap-token"}, nil)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("the token enrolled a name in a login namespace")
	}
}

func TestPasskey_LoginOverUndecodableCookieKeepsCookieAttributes(t *testing.T) {
	p := newPasskeyGateway(t)
	a := newSoftAuthenticator(t)
	p.enroll(t, a, "alice")

	// The browser still holds a cookie this gateway cannot decode: the
	// cookie secret changed across a restart, or the value is damaged.
	u, _ := url.Parse(p.url)
	p.client.Jar.SetCookies(u, []*http.Cookie{{Name: "corral_oidc", Value: "undecodable", Path: "/"}})

	b := p.begin(t, "/auth/passkey/login/begin", nil)
	resp, out := p.post(t, "/auth/passkey/login/finish?ceremony="+b.Ceremony, nil, a.get(t, b.PublicKey, false))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login finish: %d %s", resp.StatusCode, out)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "corral_oidc" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("login set no session cookie")
	}
	if !session.HttpOnly {
		t.Error("session cookie lacks HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", session.SameSite)
	}
	if session.MaxAge != sessionTTL {
		t.Errorf("session cookie Max-Age = %d, want %d", session.MaxAge, sessionTTL)
	}
}

// agedSession builds a corral_oidc cookie the way gorilla/securecookie does
// for a hash-key-only store (gob, base64url, HMAC-SHA256 over
// "name|issued|payload"), but with an issue time of the caller's choosing.
func agedSession(t *testing.T, issued time.Time) *http.Cookie {
	t.Helper()
	var buf bytes.Buffer
	values := map[interface{}]interface{}{"login": "passkey|fixture", "name": "alice", "sid": "fixture"}
	if err := gob.NewEncoder(&buf).Encode(values); err != nil {
		t.Fatal(err)
	}
	payload := base64.URLEncoding.EncodeToString(buf.Bytes())
	stamp := strconv.FormatInt(issued.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(testCookieKey))
	mac.Write([]byte("corral_oidc|" + stamp + "|" + payload))
	raw := append([]byte(stamp+"|"+payload+"|"), mac.Sum(nil)...)
	return &http.Cookie{Name: "corral_oidc", Value: base64.URLEncoding.EncodeToString(raw)}
}

func TestSession_ServerRejectsCookieOlderThanEightHours(t *testing.T) {
	p := newPasskeyGateway(t)
	status := func(c *http.Cookie) int {
		req, _ := http.NewRequest(http.MethodGet, p.url+"/", nil)
		req.AddCookie(c)
		resp, err := p.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := status(agedSession(t, time.Now())); code != http.StatusOK {
		t.Fatalf("a session issued now = %d, want 200 (the fixture no longer matches the cookie format)", code)
	}
	if code := status(agedSession(t, time.Now().Add(-9*time.Hour))); code != http.StatusFound {
		t.Fatalf("a session issued 9h ago = %d, want a redirect to sign in", code)
	}
}

func TestLogout_InvalidatesTheSessionServerSide(t *testing.T) {
	p := newPasskeyGateway(t)
	a := newSoftAuthenticator(t)
	p.enroll(t, a, "alice")
	b := p.begin(t, "/auth/passkey/login/begin", nil)
	resp, out := p.post(t, "/auth/passkey/login/finish?ceremony="+b.Ceremony, nil, a.get(t, b.PublicKey, false))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, out)
	}
	var copied *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "corral_oidc" {
			copied = &http.Cookie{Name: c.Name, Value: c.Value}
		}
	}
	if copied == nil {
		t.Fatal("login set no session cookie")
	}
	if resp, _ := p.post(t, "/auth/logout", nil, nil); resp.StatusCode != http.StatusFound {
		t.Fatalf("logout = %d, want 302", resp.StatusCode)
	}

	// A copy of the cookie taken before logout: another device, a proxy log.
	req, _ := http.NewRequest(http.MethodGet, p.url+"/", nil)
	req.AddCookie(copied)
	other := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	replay, err := other.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = replay.Body.Close()
	if replay.StatusCode != http.StatusFound {
		t.Fatalf("a cookie copied before logout still authenticates after it: %d", replay.StatusCode)
	}
}

// oidcCallback drives the real callback against an httptest token endpoint,
// with the ID token's signature check switched off (that is go-oidc's job);
// the claim handling after it is what these tests are about.
func oidcCallback(t *testing.T, extra map[string]any) (login, email string) {
	t.Helper()
	var idToken string
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "a", "token_type": "Bearer", "id_token": idToken})
	}))
	defer tokens.Close()

	g := testGateway()
	g.oauth.Endpoint.TokenURL = tokens.URL
	g.verifier = oidc.NewVerifier("https://issuer.invalid", nil, &oidc.Config{ClientID: "test-client", InsecureSkipSignatureCheck: true})

	rec := httptest.NewRecorder()
	g.login(rec, httptest.NewRequest("GET", "/auth/login", nil))
	cb := httptest.NewRequest("GET", "/auth/callback", nil)
	for _, c := range rec.Result().Cookies() {
		cb.AddCookie(c)
	}
	s, err := g.sessions.Get(cb, "corral_oidc")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	state, _ := s.Values["state"].(string)
	nonce, _ := s.Values["nonce"].(string)
	c := map[string]any{
		"iss": "https://issuer.invalid", "sub": "mallory", "aud": "test-client",
		"exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce,
	}
	for k, v := range extra {
		c[k] = v
	}
	claims, _ := json.Marshal(c)
	enc := base64.RawURLEncoding.EncodeToString
	idToken = enc([]byte(`{"alg":"RS256"}`)) + "." + enc(claims) + "." + enc([]byte("unsigned"))
	cb.URL.RawQuery = "state=" + url.QueryEscape(state) + "&code=c"

	rec2 := httptest.NewRecorder()
	g.callback(rec2, cb)
	if rec2.Code != http.StatusFound {
		t.Fatalf("callback = %d %s, want 302", rec2.Code, rec2.Body.String())
	}
	req := httptest.NewRequest("GET", "/api/whoami", nil)
	for _, c := range rec2.Result().Cookies() {
		req.AddCookie(c)
	}
	login, _, email = g.identity(req)
	return login, email
}

func TestCallback_UnverifiedEmailIsNotKept(t *testing.T) {
	login, email := oidcCallback(t, map[string]any{"email": "admin@example.com", "email_verified": false})
	if login != "https://issuer.invalid|mallory" {
		t.Fatalf("login = %q, want the issuer|subject pair", login)
	}
	if email != "" {
		t.Fatalf("an unverified email claim became the session's email (forwarded as Tailscale-User-Email): %q", email)
	}
	if _, email := oidcCallback(t, map[string]any{"email": "admin@example.com"}); email != "" {
		t.Fatalf("an email with no email_verified claim was kept: %q", email)
	}
	if _, email := oidcCallback(t, map[string]any{"email": "admin@example.com", "email_verified": true}); email != "admin@example.com" {
		t.Fatalf("a verified email was dropped: %q", email)
	}
}

// basicGateway is the gateway of TestBasicAuthUsesLibraryAndStripsCredential:
// one htpasswd user, alice / "correct horse".
func basicGateway(t *testing.T, upstream string) *gateway {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "users.htpasswd")
	if err := os.WriteFile(path, []byte("alice:"+string(hash)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := newGateway(context.Background(), "", "", "", "", upstream, []byte(testCookieKey))
	if err != nil {
		t.Fatal(err)
	}
	if g.basic, err = htpasswd.New(path, htpasswd.DefaultSystems, nil); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestIdentityHeadersSurviveClientConnectionHeader(t *testing.T) {
	var login, host string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login, host = r.Header.Get("Tailscale-User-Login"), r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	g := basicGateway(t, upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "http://corral.example.com/api/whoami", nil)
	req.SetBasicAuth("alice", "correct horse")
	req.Header.Set("Connection", "Tailscale-User-Login, Tailscale-User-Name, Tailscale-User-Email")
	rec := httptest.NewRecorder()
	g.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
	if login != "basic|alice" {
		t.Fatalf("upstream saw identity %q, want basic|alice: the client removed the gateway's header", login)
	}
	if host != "corral.example.com" {
		t.Errorf("upstream Host = %q, want the public host passed through", host)
	}
}

func TestCrossSiteMutationIsRefused(t *testing.T) {
	reached := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer upstream.Close()
	g := basicGateway(t, upstream.URL)

	req := httptest.NewRequest(http.MethodPost, "/api/cts/default/web/stop", nil)
	req.SetBasicAuth("alice", "correct horse") // what a browser attaches from its Basic cache
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	g.handler().ServeHTTP(rec, req)
	if reached || rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: status=%d, reached upstream=%v; want 403 and no upstream call", rec.Code, reached)
	}
}

func TestBasicAuthFailuresAreThrottled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	h := basicGateway(t, upstream.URL).handler()
	do := func(password string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
		req.SetBasicAuth("alice", password)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	last := 0
	for i := 0; i < 20; i++ {
		last = do("wrong password")
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("20th consecutive wrong password = %d, want 429", last)
	}
	if code := do("correct horse"); code != http.StatusTooManyRequests {
		t.Fatalf("right password while throttled = %d, want 429 (or guessing continues)", code)
	}
}

func TestEnrollTokenGuessesAreThrottled(t *testing.T) {
	p := newPasskeyGateway(t)
	last := 0
	for i := 0; i < 20; i++ {
		resp, _ := p.post(t, "/auth/passkey/enroll/begin?user=bob", map[string]string{"X-Corral-Enroll-Token": "wrong-token-xyz"}, nil)
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("20th wrong enrollment token = %d, want 429", last)
	}
}

func TestPasskeyCeremonies_AreCapped(t *testing.T) {
	p := newPasskeyGateway(t)
	p.pm.mu.Lock()
	for range maxCeremonies {
		p.pm.ceremonies[randomToken()] = passkeyCeremony{Kind: "login", Expires: time.Now().Add(time.Minute)}
	}
	p.pm.mu.Unlock()
	if resp, _ := p.post(t, "/auth/passkey/login/begin", nil, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("login/begin with the ceremony map full = %d, want 429", resp.StatusCode)
	}
}

func TestCheckEnrollToken(t *testing.T) {
	if err := checkEnrollToken("short"); err == nil {
		t.Error("a 5-character enrollment token was accepted")
	}
	if err := checkEnrollToken(""); err != nil {
		t.Errorf("no token: %v", err)
	}
	if err := checkEnrollToken(strings.Repeat("x", 16)); err != nil {
		t.Errorf("16-character token: %v", err)
	}
}
