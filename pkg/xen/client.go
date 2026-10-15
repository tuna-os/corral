// Package xen drives a XenServer/XCP-ng pool as a Corral backend
// (RFC-0003 §4).
//
// This is the client-only slice (XN-1): a thin hand-rolled XenAPI XML-RPC
// client in the shape of pkg/proxmoxbe, with no wiring into pkg/backend,
// pkg/types, pkg/config, or docs. That wiring follows in a later turn, after
// the vSphere backend lands.
//
// XenAPI is XML-RPC over HTTPS POSTed to https://<master>/ (not a REST API),
// so there is no SDK import: requests are encoded and responses decoded here,
// keeping the subset tiny — session login/logout, VM.get_all, VM.get_record,
// the power operations, snapshot/revert/clone, pool_migrate, and the /export
// HTTP download. Method and field names follow the xapi-project XenAPI object
// model (https://xapi-project.github.io/xen-api/classes/vm.html).
package xen

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config is one XenServer/XCP-ng pool endpoint, as a Corral context carries
// it. The password is a secret: it is resolved from the environment (see
// config.go), never from the config file.
type Config struct {
	// Host is the pool master: "xen.example.com",
	// "xen.example.com:4443", or a full "https://…" URL. The port
	// defaults to 443.
	Host string
	// Username is the XenAPI user, typically "root".
	Username string
	// Password is the user's password, from CORRAL_XEN_PASSWORD.
	Password string
	// Fingerprint pins the server certificate (SHA-256, hex, colons
	// optional). Self-signed certificates are the norm on XenServer, and
	// pinning is how trust is established without disabling verification.
	Fingerprint string
	// Insecure skips verification entirely. Explicit, per-context, and
	// never a default — a silent skip is how a homelab convenience
	// becomes a habit.
	Insecure bool
}

// Client talks to one pool. Like pkg/proxmoxbe it is an HTTP client rather
// than a command runner — XenAPI's supported interface is the XML-RPC API,
// and `xe` only exists on the host itself. So there is no shell.Runner seam
// here; the seam is the http.Client, and tests use httptest.
type Client struct {
	cfg  Config
	base string
	http *http.Client

	mu      sync.Mutex
	session string
}

// Timeout bounds every request. A host that is up but wedged should fail a
// list, not hang a whole fleet aggregation — the same reasoning behind the
// 20s precedent in pkg/proxmoxbe.
const Timeout = 20 * time.Second

// apiVersion is the XenAPI version string sent with
// session.login_with_password.
const apiVersion = "1.0"

// New builds a client. It fails on a configuration that cannot work rather
// than at the first request, so `corral context add` can reject it
// immediately.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("xen: host is required")
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return nil, fmt.Errorf("xen: username is required (usually root)")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("xen: password is required; set %s", PasswordEnv)
	}

	base := cfg.Host
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("xen: host %q: %w", cfg.Host, err)
	}
	parsed.Path = "/"

	transport, err := transportFor(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:  cfg,
		base: parsed.String(),
		http: &http.Client{Timeout: Timeout, Transport: transport},
	}, nil
}

// WithHTTPClient replaces the transport. Tests point this at an httptest
// server; nothing in production does.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.http = h
	return c
}

// Host is the configured pool master.
func (c *Client) Host() string { return c.cfg.Host }

func transportFor(cfg Config) (http.RoundTripper, error) {
	switch {
	case cfg.Fingerprint != "":
		want := normaliseFingerprint(cfg.Fingerprint)
		if len(want) != 64 {
			return nil, fmt.Errorf("xen: fingerprint must be a SHA-256 hex digest, got %d hex chars", len(want))
		}
		// Verification is done by hand against the pin, which is what
		// allows a self-signed certificate to be trusted *specifically*
		// rather than trusting anything a MITM presents.
		return &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // replaced by the pin below
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				for _, raw := range rawCerts {
					sum := sha256.Sum256(raw)
					if hex.EncodeToString(sum[:]) == want {
						return nil
					}
				}
				return fmt.Errorf("xen: server certificate does not match the pinned fingerprint")
			},
		}}, nil
	case cfg.Insecure:
		return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, nil //nolint:gosec // explicit per-context opt-in
	default:
		return http.DefaultTransport, nil
	}
}

func normaliseFingerprint(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(strings.TrimSpace(s)))
}

// APIError is a XenAPI failure response: the method ran, and the pool said
// no. It carries the XenAPI error code (SESSION_INVALID, VM_BAD_POWER_STATE,
// …) so callers can tell a refusal (retrying cannot help) from a failure,
// the same distinction pkg/snapshot draws between Unsupported and a plain
// error.
type APIError struct {
	Code   string
	Params []string
	Method string
}

func (e *APIError) Error() string {
	if len(e.Params) == 0 {
		return fmt.Sprintf("xen: %s failed: %s", e.Method, e.Code)
	}
	return fmt.Sprintf("xen: %s failed: %s (%s)", e.Method, e.Code, strings.Join(e.Params, ", "))
}

// SessionInvalid reports whether the pool rejected the session, which the
// client heals by logging in again rather than surfacing to the caller.
func (e *APIError) SessionInvalid() bool { return e.Code == "SESSION_INVALID" }

// ── session ─────────────────────────────────────────────────────────

// Login authenticates and caches the session reference. It is called lazily
// by call, so most callers never invoke it directly; tests and the doctor
// use it to prove reachability and credentials in one call.
func (c *Client) Login() (string, error) {
	v, err := c.rpc("session.login_with_password",
		c.cfg.Username, c.cfg.Password, apiVersion, "corral")
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("xen: session.login_with_password returned %T, want a session reference", v)
	}
	c.mu.Lock()
	c.session = s
	c.mu.Unlock()
	return s, nil
}

// Logout ends the cached session, if any. Errors are swallowed: a logout
// that fails leaves nothing behind worth reporting, and a failed login
// must not fail the operation that follows it.
func (c *Client) Logout() {
	c.mu.Lock()
	sess := c.session
	c.session = ""
	c.mu.Unlock()
	if sess == "" {
		return
	}
	_, _ = c.rpc("session.logout", sess)
}

func (c *Client) cachedSession() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

func (c *Client) clearSession() {
	c.mu.Lock()
	c.session = ""
	c.mu.Unlock()
}

// call invokes a session-authenticated XenAPI method, logging in first when
// there is no cached session and re-logging in once when the pool reports
// SESSION_INVALID (sessions expire server-side; the retry is the honest
// repair, not a guess).
func (c *Client) call(method string, params ...any) (any, error) {
	sess := c.cachedSession()
	if sess == "" {
		var err error
		if sess, err = c.Login(); err != nil {
			return nil, err
		}
	}
	v, err := c.rpc(method, append([]any{sess}, params...)...)
	if apiErr, ok := err.(*APIError); ok && apiErr.SessionInvalid() {
		c.clearSession()
		if sess, err = c.Login(); err != nil {
			return nil, err
		}
		return c.rpc(method, append([]any{sess}, params...)...)
	}
	return v, err
}

// ── XML-RPC ─────────────────────────────────────────────────────────

// rpc POSTs one XML-RPC method call and returns the response Value: a
// string, bool, int64, float64, []any, or map[string]any. A XenAPI
// Status=Failure response becomes an *APIError; an XML-RPC <fault> or a
// transport problem becomes a plain error.
func (c *Client) rpc(method string, params ...any) (any, error) {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?>` + "\n<methodCall><methodName>")
	_ = xml.EscapeText(&sb, []byte(method)) // strings.Builder never fails
	sb.WriteString("</methodName>")
	if len(params) > 0 {
		sb.WriteString("<params>")
		for _, p := range params {
			sb.WriteString("<param><value>")
			encodeValue(&sb, p)
			sb.WriteString("</value></param>")
		}
		sb.WriteString("</params>")
	}
	sb.WriteString("</methodCall>")

	req, err := http.NewRequest(http.MethodPost, c.base, strings.NewReader(sb.String()))
	if err != nil {
		return nil, fmt.Errorf("xen: %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "text/xml")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xen: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("xen: %s: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xen: %s: unexpected HTTP status %s", method, resp.Status)
	}
	return decodeResponse(method, body)
}

// decodeResponse parses a methodResponse body. XenAPI wraps every result in
// a {Status, Value, ErrorDescription} struct rather than using XML-RPC
// faults for API-level errors, so both shapes are handled here.
func decodeResponse(method string, body []byte) (any, error) {
	var mr methodResponse
	if err := xml.Unmarshal(body, &mr); err != nil {
		return nil, fmt.Errorf("xen: %s: malformed XML-RPC response: %w", method, err)
	}
	if mr.Fault != nil {
		return nil, fmt.Errorf("xen: %s: XML-RPC fault: %s", method, faultString(mr.Fault.Value))
	}
	if mr.Params == nil || len(mr.Params.Param) != 1 {
		return nil, fmt.Errorf("xen: %s: malformed XML-RPC response: want one param", method)
	}
	v := mr.Params.Param[0].Value.goValue()
	rec, ok := v.(map[string]any)
	if !ok {
		// Not the XenAPI envelope (e.g. a bare value in a fixture that
		// tests the transport only); return it as-is.
		return v, nil
	}
	status, _ := rec["Status"].(string)
	switch status {
	case "Success":
		val, ok := rec["Value"]
		if !ok {
			return nil, fmt.Errorf("xen: %s: Success response carries no Value", method)
		}
		return val, nil
	case "Failure":
		desc, _ := rec["ErrorDescription"].([]any)
		apiErr := &APIError{Method: method}
		for i, d := range desc {
			s, _ := d.(string)
			if i == 0 {
				apiErr.Code = s
			} else {
				apiErr.Params = append(apiErr.Params, s)
			}
		}
		if apiErr.Code == "" {
			apiErr.Code = "UNKNOWN_FAILURE"
		}
		return nil, apiErr
	default:
		return nil, fmt.Errorf("xen: %s: unexpected Status %q", method, status)
	}
}

func faultString(v rpcValue) string {
	s := v.goValue()
	if m, ok := s.(map[string]any); ok {
		if code, ok := m["faultCode"].(string); ok {
			if str, ok := m["faultString"].(string); ok {
				return code + ": " + str
			}
			return code
		}
	}
	return fmt.Sprintf("%v", s)
}

// encodeValue writes one XML-RPC <value> body (without the <value> tags).
func encodeValue(sb *strings.Builder, p any) {
	switch t := p.(type) {
	case nil:
		sb.WriteString("<nil/>")
	case string:
		sb.WriteString("<string>")
		_ = xml.EscapeText(sb, []byte(t)) // strings.Builder never fails
		sb.WriteString("</string>")
	case bool:
		if t {
			sb.WriteString("<boolean>1</boolean>")
		} else {
			sb.WriteString("<boolean>0</boolean>")
		}
	case int:
		fmt.Fprintf(sb, "<int>%d</int>", t)
	case int32:
		fmt.Fprintf(sb, "<int>%d</int>", t)
	case int64:
		fmt.Fprintf(sb, "<i8>%d</i8>", t)
	case float64:
		fmt.Fprintf(sb, "<double>%v</double>", t)
	case []string:
		sb.WriteString("<array><data>")
		for _, s := range t {
			sb.WriteString("<value>")
			encodeValue(sb, s)
			sb.WriteString("</value>")
		}
		sb.WriteString("</data></array>")
	case []any:
		sb.WriteString("<array><data>")
		for _, e := range t {
			sb.WriteString("<value>")
			encodeValue(sb, e)
			sb.WriteString("</value>")
		}
		sb.WriteString("</data></array>")
	case map[string]string:
		sb.WriteString("<struct>")
		for _, k := range sortedKeys(t) {
			sb.WriteString("<member><name>")
			_ = xml.EscapeText(sb, []byte(k)) // strings.Builder never fails
			sb.WriteString("</name><value>")
			encodeValue(sb, t[k])
			sb.WriteString("</value></member>")
		}
		sb.WriteString("</struct>")
	case map[string]any:
		sb.WriteString("<struct>")
		for _, k := range sortedAnyKeys(t) {
			sb.WriteString("<member><name>")
			_ = xml.EscapeText(sb, []byte(k)) // strings.Builder never fails
			sb.WriteString("</name><value>")
			encodeValue(sb, t[k])
			sb.WriteString("</value></member>")
		}
		sb.WriteString("</struct>")
	default:
		sb.WriteString("<string>")
		_ = xml.EscapeText(sb, []byte(fmt.Sprintf("%v", t))) // strings.Builder never fails
		sb.WriteString("</string>")
	}
}

// methodResponse is an XML-RPC methodResponse: either params or a fault.
type methodResponse struct {
	Params *struct {
		Param []struct {
			Value rpcValue `xml:"value"`
		} `xml:"param"`
	} `xml:"params"`
	Fault *struct {
		Value rpcValue `xml:"value"`
	} `xml:"fault"`
}

// rpcValue is one XML-RPC <value>. A bare value with no type tag is a
// string, per the spec.
type rpcValue struct {
	Str     *string    `xml:"string"`
	Int     *string    `xml:"int"`
	I4      *string    `xml:"i4"`
	I8      *string    `xml:"i8"`
	Boolean *string    `xml:"boolean"`
	Double  *string    `xml:"double"`
	Nil     *struct{}  `xml:"nil"`
	Array   *rpcArray  `xml:"array"`
	Struct  *rpcStruct `xml:"struct"`
	Text    string     `xml:",chardata"`
}

type rpcArray struct {
	Data []rpcValue `xml:"data>value"`
}

type rpcStruct struct {
	Members []struct {
		Name  string   `xml:"name"`
		Value rpcValue `xml:"value"`
	} `xml:"member"`
}

// goValue converts the decoded XML-RPC value to plain Go types.
func (v rpcValue) goValue() any {
	switch {
	case v.Array != nil:
		out := make([]any, 0, len(v.Array.Data))
		for _, e := range v.Array.Data {
			out = append(out, e.goValue())
		}
		return out
	case v.Struct != nil:
		out := make(map[string]any, len(v.Struct.Members))
		for _, m := range v.Struct.Members {
			out[m.Name] = m.Value.goValue()
		}
		return out
	case v.Str != nil:
		return *v.Str
	case v.Int != nil:
		return parseInt(*v.Int)
	case v.I4 != nil:
		return parseInt(*v.I4)
	case v.I8 != nil:
		return parseInt(*v.I8)
	case v.Boolean != nil:
		s := strings.TrimSpace(*v.Boolean)
		return s == "1" || strings.EqualFold(s, "true")
	case v.Double != nil:
		var f float64
		_, _ = fmt.Sscanf(strings.TrimSpace(*v.Double), "%g", &f) // 0 on a bad value, as documented
		return f
	case v.Nil != nil:
		return nil
	default:
		return v.Text
	}
}

func parseInt(s string) int64 {
	var n int64
	_, _ = fmt.Sscanf(strings.TrimSpace(s), "%d", &n) // 0 on a bad value, as documented
	return n
}

// asString coerces an XML-RPC scalar to string; non-scalars come back empty,
// which is how absent optional fields read.
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%v", t)
	default:
		return ""
	}
}

func asInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case string:
		var n int64
		_, _ = fmt.Sscanf(strings.TrimSpace(t), "%d", &n) // 0 on a bad value, as documented
		return n
	default:
		return 0
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asArray(v any) []any {
	a, _ := v.([]any)
	return a
}

func asStrings(v any) []string {
	var out []string
	for _, e := range asArray(v) {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// sortedKeys orders encoded struct members so requests are deterministic.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedAnyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
