// Package nutanix drives PC-managed AHV VMs through the Prism Central v4
// REST API (first slice: Prism Central only, AHV only).
//
// The client is hand-rolled REST over net/http, following the pkg/proxmoxbe
// precedent. See the NX-0 spike conclusions (PR body): the v4 API is plain
// JSON over HTTPS with Basic or API-key auth, an OData list envelope, and a
// $actions sub-resource for mutations — nothing hostile — while the
// ntnx-api-golang-clients alternative is a huge generated per-namespace tree
// the module does not need. No go.mod dependency.
//
// The HTTP seam is the http.RoundTripper: tests point WithHTTPClient at an
// httptest server serving recorded v4 JSON shapes. There is no shell.Runner
// seam because there is no command to fake.
package nutanix

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config is one Prism Central endpoint, as a Corral context carries it.
type Config struct {
	// Host is the Prism Central VIP or FQDN: "pc.example.com",
	// "pc.example.com:9440", or a full "https://…" URL.
	// The port defaults to 9440.
	Host string
	// Username is the Prism Central account (local or directory).
	Username string
	// Secret is the account password or API key. It is never stored in a
	// file: it arrives via CORRAL_NUTANIX_PASSWORD[_<CTX>] (see config.go).
	Secret string
	// Fingerprint pins the server certificate (SHA-256, hex, colons
	// optional). Self-signed certificates are the norm on PC, and pinning
	// is how trust is established without disabling verification.
	Fingerprint string
	// Insecure skips verification entirely. Explicit, per-context, and
	// never a default.
	Insecure bool
	// APIVersion selects the v4 version string, e.g. "v4.0". Availability
	// varies by PC build (some answer v4.0, some v4.0.b1), so this stays
	// configurable rather than probed: probing turns one failing call
	// into five.
	APIVersion string
}

// DefaultAPIVersion is the v4 version string sent when Config.APIVersion is
// empty. v4.0 is GA on PC 7.3 / AOS 7.3 and later.
const DefaultAPIVersion = "v4.0"

// Client talks to one Prism Central.
type Client struct {
	cfg  Config
	base string
	http *http.Client
}

// Timeout bounds every request. A PC that is up but wedged should fail a
// list, not hang a whole fleet aggregation.
const Timeout = 20 * time.Second

// New builds a client. It fails on a configuration that cannot work rather
// than at the first request, so `corral context add` can reject it
// immediately.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("nutanix: host is required (Prism Central VIP or FQDN)")
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return nil, fmt.Errorf("nutanix: username is required")
	}
	if strings.TrimSpace(cfg.Secret) == "" {
		return nil, fmt.Errorf("nutanix: a password or API key is required " +
			"(set " + PasswordEnv + "; it is never stored in a file)")
	}

	base := cfg.Host
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("nutanix: host %q: %w", cfg.Host, err)
	}
	if parsed.Port() == "" {
		parsed.Host += ":9440"
	}
	parsed.Path = "/api"

	if strings.TrimSpace(cfg.APIVersion) == "" {
		cfg.APIVersion = DefaultAPIVersion
	}

	transport, err := transportFor(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:  cfg,
		base: strings.TrimSuffix(parsed.String(), "/"),
		http: &http.Client{Timeout: Timeout, Transport: transport},
	}, nil
}

// WithHTTPClient replaces the transport. Tests point this at an httptest
// server; nothing in production does.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.http = h
	return c
}

// APIVersion reports the v4 version string this client sends.
func (c *Client) APIVersion() string { return c.cfg.APIVersion }

func transportFor(cfg Config) (http.RoundTripper, error) {
	switch {
	case cfg.Fingerprint != "":
		want := normaliseFingerprint(cfg.Fingerprint)
		if len(want) != 64 {
			return nil, fmt.Errorf("nutanix: fingerprint must be a SHA-256 hex digest, got %d hex chars", len(want))
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
				return fmt.Errorf("nutanix: server certificate does not match the pinned fingerprint")
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

// APIError is a Prism Central error response. It carries the status so
// callers can tell a refusal (400: retrying cannot help) from a failure
// (500: it might).
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("nutanix: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Message)
	}
	return fmt.Sprintf("nutanix: %s %s: HTTP %d", e.Method, e.Path, e.Status)
}

// Unauthorized reports whether the credentials were rejected, which the
// doctor reports differently from an unreachable Prism Central.
func (e *APIError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// get decodes a synchronous v4 response envelope into out.
func (c *Client) get(path string, out any) error {
	return c.do(http.MethodGet, path, nil, out)
}

// post sends a JSON body. Mutations carry an idempotency id because v4
// requires Ntnx-Request-Id on POST/PUT/DELETE; a retried request with the
// same id must not power-cycle a VM twice.
func (c *Client) post(path string, body any, out any) error {
	return c.do(http.MethodPost, path, body, out)
}

func (c *Client) do(method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("nutanix: %s %s: encoding request: %w", method, path, err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.Secret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodDelete {
		req.Header.Set("Ntnx-Request-Id", newRequestID())
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("nutanix: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("nutanix: reading %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Method: method, Path: path,
			Message: errorMessage(payload)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("nutanix: %s %s returned unparseable JSON: %w", method, path, err)
	}
	return nil
}

// errorMessage pulls the human part out of a v4 error body. The shape varies
// by namespace, so several are tried before falling back to truncated text.
func errorMessage(payload []byte) string {
	var body struct {
		Message string `json:"message"`
		Error   *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &body); err == nil {
		switch {
		case body.Error != nil && body.Error.Message != "":
			return body.Error.Message
		case body.Message != "":
			return body.Message
		}
	}
	text := strings.TrimSpace(string(payload))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

// newRequestID mints a v4 idempotency id. crypto/rand hex rather than a UUID
// package: the only requirement is uniqueness per request, and that is one
// fewer dependency for a client that has none.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
