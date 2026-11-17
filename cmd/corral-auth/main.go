// corral-auth is an optional OIDC reverse-proxy plugin for corral web.
// It keeps identity/session dependencies out of the main corral binary.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"github.com/spf13/cobra"
	"github.com/tg123/go-htpasswd"
	"github.com/tuna-os/corral/pkg/plugin/sdk"
	"golang.org/x/oauth2"
)

type gateway struct {
	oauth     *oauth2.Config
	verifier  *oidc.IDTokenVerifier
	sessions  *sessions.CookieStore
	proxy     *httputil.ReverseProxy
	secure    bool
	basic     *htpasswd.File
	passkeys  *passkeyManager
	peerToken string

	revokedMu sync.Mutex
	revoked   map[string]time.Time // session id -> when its cookie expires anyway

	basicFailures  *failures
	enrollFailures *failures
}

// sessionTTL is how long a signed-in session lasts, in seconds. It is both
// the cookie's Max-Age and the server-side limit on the signed timestamp.
const sessionTTL = 8 * 60 * 60

func (g *gateway) sessionOptions() *sessions.Options {
	return &sessions.Options{Path: "/", MaxAge: sessionTTL, HttpOnly: true, Secure: g.secure, SameSite: http.SameSiteLaxMode}
}

// signedIn is the only place a signed-in session's values are minted. The
// random sid is what logout revokes.
func signedIn(login, name, email string) map[interface{}]interface{} {
	return map[interface{}]interface{}{"login": login, "name": name, "email": email, "sid": randomToken()}
}

func (g *gateway) revoke(sid string) {
	g.revokedMu.Lock()
	defer g.revokedMu.Unlock()
	now := time.Now()
	for id, until := range g.revoked {
		if now.After(until) {
			delete(g.revoked, id)
		}
	}
	if g.revoked == nil {
		g.revoked = map[string]time.Time{}
	}
	g.revoked[sid] = now.Add(sessionTTL * time.Second)
}

func (g *gateway) isRevoked(sid string) bool {
	g.revokedMu.Lock()
	defer g.revokedMu.Unlock()
	_, ok := g.revoked[sid]
	return ok
}

// failures counts failed guesses per key (a Basic user name, or the
// enrollment token) in a fixed window. A key with failureLimit failures is
// refused until the window ends. Keys are what is being guessed, not client
// addresses: behind the edge proxy every client has the proxy's address.
type failures struct {
	mu    sync.Mutex
	count map[string]int
	reset time.Time
}

const (
	failureLimit  = 10
	failureWindow = 5 * time.Minute
	failureKeys   = 10000 // beyond this many distinct keys in a window, new keys are refused
)

func (f *failures) rollLocked() {
	if now := time.Now(); f.count == nil || now.After(f.reset) {
		f.count, f.reset = map[string]int{}, now.Add(failureWindow)
	}
}

func (f *failures) blocked(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollLocked()
	n, seen := f.count[key]
	return n >= failureLimit || (!seen && len(f.count) >= failureKeys)
}

func (f *failures) fail(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollLocked()
	if _, seen := f.count[key]; seen || len(f.count) < failureKeys {
		f.count[key]++
	}
}

type identityKey struct{}

// forwardedIdentity is what serve hands the proxy, through the request
// context, to set on the outbound request.
type forwardedIdentity struct {
	login, name, email string
	peer               bool
}

// identityHeaders are set only by the gateway. They are removed from the
// outbound request after hop-by-hop processing, so a client Connection
// header cannot strip the gateway's own values.
var identityHeaders = []string{"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-User-Email", "Corral-Service-Identity"}

func newProxy(u *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(u)
		pr.Out.Host = pr.In.Host // corral web pins Host and compares the WebSocket Origin with it
		pr.SetXForwarded()
		for _, h := range identityHeaders {
			pr.Out.Header.Del(h)
		}
		id, _ := pr.In.Context().Value(identityKey{}).(forwardedIdentity)
		if id.peer {
			return // Authorization: Bearer <peer token> goes through for corral web to verify
		}
		pr.Out.Header.Del("Authorization")
		if id.login == "" {
			return
		}
		pr.Out.Header.Set("Tailscale-User-Login", id.login)
		pr.Out.Header.Set("Tailscale-User-Name", id.name)
		if id.email != "" {
			pr.Out.Header.Set("Tailscale-User-Email", id.email)
		}
	}}
}

func (g *gateway) forward(w http.ResponseWriter, r *http.Request, id forwardedIdentity) {
	g.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func newGateway(ctx context.Context, issuer, clientID, clientSecret, redirectURL, upstream string, cookieKey []byte) (*gateway, error) {
	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("upstream must be an http(s) URL")
	}
	store := sessions.NewCookieStore(cookieKey)
	// The codec rejects a cookie whose signed timestamp is older than this,
	// so a copied cookie stops working after 8 hours, not the library's 30 days.
	store.MaxAge(sessionTTL)
	secure := strings.HasPrefix(redirectURL, "https://")
	g := &gateway{sessions: store, proxy: newProxy(u), secure: secure, basicFailures: &failures{}, enrollFailures: &failures{}}
	store.Options = g.sessionOptions()
	if issuer != "" {
		if clientID == "" || redirectURL == "" {
			return nil, fmt.Errorf("OIDC client ID and redirect URL are required with an issuer")
		}
		provider, err := oidc.NewProvider(ctx, issuer)
		if err != nil {
			return nil, fmt.Errorf("OIDC discovery: %w", err)
		}
		g.oauth = &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: provider.Endpoint(), RedirectURL: redirectURL, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
		g.verifier = provider.Verifier(&oidc.Config{ClientID: clientID})
	}
	return g, nil
}

func (g *gateway) session(r *http.Request) (*sessions.Session, error) {
	s, err := g.sessions.Get(r, "corral_oidc")
	// Get returns a fresh session even when the presented cookie does not
	// decode, and the sign-in handlers save it, so it always gets these
	// options rather than the library defaults (30 days, SameSite=None, no
	// HttpOnly).
	if s != nil {
		s.Options = g.sessionOptions()
	}
	return s, err
}
func (g *gateway) login(w http.ResponseWriter, r *http.Request) {
	if g.oauth == nil {
		http.Error(w, "OIDC is not configured", http.StatusNotFound)
		return
	}
	s, _ := g.session(r) // an undecodable cookie is replaced by a fresh session
	if s == nil {
		http.Error(w, "invalid session", 400)
		return
	}
	state, nonce, verifier := randomToken(), randomToken(), oauth2.GenerateVerifier()
	s.Values["state"], s.Values["nonce"], s.Values["verifier"] = state, nonce, verifier
	if err := s.Save(r, w); err != nil {
		http.Error(w, "saving session", 500)
		return
	}
	http.Redirect(w, r, g.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}
func (g *gateway) callback(w http.ResponseWriter, r *http.Request) {
	s, err := g.session(r)
	if err != nil {
		http.Error(w, "invalid session", 400)
		return
	}
	state, _ := s.Values["state"].(string)
	nonce, _ := s.Values["nonce"].(string)
	verifier, _ := s.Values["verifier"].(string)
	if state == "" || r.URL.Query().Get("state") != state {
		http.Error(w, "invalid OIDC state", 400)
		return
	}
	tok, err := g.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		http.Error(w, "OIDC exchange failed", http.StatusUnauthorized)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	id, err := g.verifier.Verify(r.Context(), raw)
	if err != nil {
		http.Error(w, "invalid ID token", http.StatusUnauthorized)
		return
	}
	var claims map[string]any
	if err := id.Claims(&claims); err != nil {
		http.Error(w, "invalid claims", http.StatusUnauthorized)
		return
	}
	gotNonce, _ := claims["nonce"].(string)
	if gotNonce != nonce {
		http.Error(w, "invalid OIDC nonce", http.StatusUnauthorized)
		return
	}
	subject := claim(claims, "sub")
	issuer := claim(claims, "iss")
	login := subject
	if issuer != "" {
		login = issuer + "|" + subject
	}
	name := claim(claims, "name", "email", "preferred_username")
	if login == "" {
		http.Error(w, "OIDC identity has no usable subject", http.StatusUnauthorized)
		return
	}
	// The email is forwarded as Tailscale-User-Email, and corral web grants
	// admin by email, so keep it only when the provider says it is verified.
	email := ""
	if verified(claims["email_verified"]) {
		email = claim(claims, "email")
	}
	s.Values = signedIn(login, name, email)
	if err := s.Save(r, w); err != nil {
		http.Error(w, "saving session", 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}
func verified(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		return v == "true" // some providers send the claim as a string
	}
	return false
}
func claim(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
func (g *gateway) logout(w http.ResponseWriter, r *http.Request) {
	s, err := g.session(r)
	if err == nil {
		if sid, _ := s.Values["sid"].(string); sid != "" {
			g.revoke(sid) // copies of the cookie stop working too
		}
	}
	s.Options.MaxAge = -1
	_ = s.Save(r, w)
	http.Redirect(w, r, "/auth/login", http.StatusFound)
}
func (g *gateway) serve(w http.ResponseWriter, r *http.Request) {
	provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if g.peerToken != "" && len(provided) == len(g.peerToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(g.peerToken)) == 1 {
		g.forward(w, r, forwardedIdentity{peer: true})
		return
	}
	login, name, email := g.identity(r)
	if login == "" {
		if g.basicThrottled(r) {
			http.Error(w, "too many failed sign-in attempts; try again later", http.StatusTooManyRequests)
		} else if g.basic != nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="Corral", charset="UTF-8"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
		} else if g.passkeys != nil {
			http.Redirect(w, r, "/auth/passkey", http.StatusFound)
		} else {
			http.Redirect(w, r, "/auth/login", http.StatusFound)
		}
		return
	}
	g.forward(w, r, forwardedIdentity{login: login, name: name, email: email})
}

func (g *gateway) basicThrottled(r *http.Request) bool {
	user, _, ok := r.BasicAuth()
	return ok && g.basic != nil && g.basicFailures != nil && g.basicFailures.blocked("basic|"+user)
}
func (g *gateway) identity(r *http.Request) (login, name, email string) {
	if s, err := g.session(r); err == nil {
		login, _ = s.Values["login"].(string)
		name, _ = s.Values["name"].(string)
		email, _ = s.Values["email"].(string)
		// A session without a sid predates revocation, and a revoked one
		// was logged out: neither is a signed-in session.
		if sid, _ := s.Values["sid"].(string); sid == "" || g.isRevoked(sid) {
			login, name, email = "", "", ""
		}
		if login != "" {
			return
		}
	}
	if g.basic != nil {
		if user, password, ok := r.BasicAuth(); ok {
			key := "basic|" + user
			if g.basicFailures != nil && g.basicFailures.blocked(key) {
				return "", "", ""
			}
			if g.basic.Match(user, password) {
				return key, user, ""
			}
			if g.basicFailures != nil {
				g.basicFailures.fail(key)
			}
		}
	}
	return "", "", ""
}
func (g *gateway) handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /auth/login", g.login)
	m.HandleFunc("GET /auth/callback", g.callback)
	m.HandleFunc("POST /auth/logout", g.logout)
	if g.passkeys != nil {
		g.registerPasskeyRoutes(m)
	}
	m.HandleFunc("/", g.serve)
	// Refuse state-changing requests a browser marks as cross-origin (CSRF
	// through cached Basic credentials, login CSRF on the passkey finish).
	// Clients that send neither Sec-Fetch-Site nor Origin are unaffected.
	return securityHeaders(http.NewCrossOriginProtection().Handler(m))
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func cookieKey(raw string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(raw); err == nil && len(b) >= 32 {
		return b, nil
	}
	if len(raw) >= 32 {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("cookie secret must be at least 32 bytes (literal or base64url)")
}

// checkEnrollToken refuses a bootstrap token short enough to guess online.
func checkEnrollToken(token string) error {
	if token != "" && len(token) < 16 {
		return fmt.Errorf("--passkey-enroll-token must be at least 16 characters")
	}
	return nil
}
func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	if sdk.HandleMetadata(sdk.Metadata{Name: "auth", Version: "0.2.0", Description: "OIDC, Basic Auth, and passkey gateway", Capabilities: []string{"auth-gateway", "oidc", "basic-auth", "passkeys"}, Permissions: []string{"listen on network", "proxy Corral HTTP traffic", "store signed sessions and passkeys"}, SupportedBackends: []string{"all"}}) {
		return
	}
	var addr, upstream, issuer, clientID, clientSecret, redirectURL, secret, basicFile, passkeyFile, passkeyRPID, passkeyOrigin, enrollToken, peerToken string
	c := &cobra.Command{Use: "corral-auth", Short: "OIDC authentication gateway for corral web", RunE: func(*cobra.Command, []string) error {
		k, err := cookieKey(secret)
		if err != nil {
			return err
		}
		g, err := newGateway(context.Background(), issuer, clientID, clientSecret, redirectURL, upstream, k)
		if err != nil {
			return err
		}
		if basicFile != "" {
			g.basic, err = htpasswd.New(basicFile, htpasswd.DefaultSystems, nil)
			if err != nil {
				return fmt.Errorf("basic auth file: %w", err)
			}
		}
		if passkeyFile != "" {
			if err := checkEnrollToken(enrollToken); err != nil {
				return err
			}
			g.passkeys, err = newPasskeyManager(passkeyFile, passkeyRPID, passkeyOrigin, enrollToken)
			if err != nil {
				return err
			}
			if strings.HasPrefix(passkeyOrigin, "https://") {
				g.secure = true
			}
		}
		g.peerToken = peerToken
		if g.oauth == nil && g.basic == nil && g.passkeys == nil {
			return fmt.Errorf("configure at least one of OIDC, --basic-htpasswd, or --passkey-store")
		}
		srv := &http.Server{Addr: addr, Handler: g.handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 90 * time.Second}
		fmt.Fprintf(os.Stderr, "corral-auth: %s -> %s\n", addr, upstream)
		return srv.ListenAndServe()
	}}
	f := c.Flags()
	f.StringVar(&addr, "addr", env("CORRAL_AUTH_ADDR", ":8443"), "listen address")
	f.StringVar(&upstream, "upstream", env("CORRAL_AUTH_UPSTREAM", "http://127.0.0.1:8006"), "corral web URL")
	f.StringVar(&issuer, "issuer", env("CORRAL_OIDC_ISSUER", ""), "OIDC issuer URL")
	f.StringVar(&clientID, "client-id", env("CORRAL_OIDC_CLIENT_ID", ""), "OIDC client ID")
	f.StringVar(&clientSecret, "client-secret", env("CORRAL_OIDC_CLIENT_SECRET", ""), "OIDC client secret")
	f.StringVar(&redirectURL, "redirect-url", env("CORRAL_OIDC_REDIRECT_URL", ""), "OIDC callback URL")
	f.StringVar(&secret, "cookie-secret", env("CORRAL_AUTH_COOKIE_SECRET", ""), "32+ byte session signing key")
	f.StringVar(&basicFile, "basic-htpasswd", env("CORRAL_BASIC_HTPASSWD", ""), "Apache htpasswd file for HTTP Basic authentication")
	f.StringVar(&passkeyFile, "passkey-store", env("CORRAL_PASSKEY_STORE", ""), "Passkey credential JSON store")
	f.StringVar(&passkeyRPID, "passkey-rp-id", env("CORRAL_PASSKEY_RP_ID", ""), "WebAuthn relying-party domain")
	f.StringVar(&passkeyOrigin, "passkey-origin", env("CORRAL_PASSKEY_ORIGIN", ""), "WebAuthn HTTPS origin")
	f.StringVar(&enrollToken, "passkey-enroll-token", env("CORRAL_PASSKEY_ENROLL_TOKEN", ""), "Bootstrap token for first passkey enrollment")
	f.StringVar(&peerToken, "peer-token", env("CORRAL_PEER_TOKEN", ""), "Scoped Corral-to-Corral service token passed to core")
	_ = c.MarkFlagRequired("cookie-secret")
	if err := c.Execute(); err != nil {
		b, _ := json.Marshal(err.Error())
		fmt.Fprintln(os.Stderr, string(b))
		os.Exit(1)
	}
}
