// Package vsphere drives a VMware vSphere estate (vCenter) as a Corral
// backend (RFC-0003).
//
// Provenance: github.com/vmware/govmomi v0.56.0 (Apache-2.0, actively
// maintained), the SDK VMware supports for programmatic access to
// https://<vc>/sdk/vim25 — the same reasoning that picked PVE's REST API over
// `pvesh` for pkg/proxmoxbe. Pinned in go.mod; fixtures run against
// govmomi/simulator, the honest substitute the same way Proxmox's
// recorded-payload httptest fixtures are.
package vsphere

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/tlspin"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
)

// Config is one vCenter endpoint, as a Corral context carries it.
type Config struct {
	// Host is the vCenter endpoint: "vc.example.com",
	// "vc.example.com:443", or a full "https://…" URL. The path is always
	// /sdk/vim25.
	Host string
	// Username is the vSphere user, e.g. "administrator@vsphere.local".
	Username string
	// Password is the user's password or session ticket. It lives in memory
	// only: ConfigForContext reads it from CORRAL_VSPHERE_PASSWORD[_<CTX>],
	// never from the config file.
	Password string
	// Fingerprint pins the server certificate (SHA-256, hex, colons
	// optional). Self-signed certificates are common on vCenter, and pinning
	// is how trust is established without disabling verification.
	Fingerprint string
	// Insecure skips verification entirely. Explicit, per-context, and never
	// a default — a silent skip is how a homelab convenience becomes a habit.
	Insecure bool
}

// Client talks to one vCenter.
type Client struct {
	cfg Config
	vim *vim25.Client
}

// Timeout bounds session setup. A vCenter that is up but wedged should fail a
// list, not hang a whole fleet aggregation — the same reasoning behind the
// kubectl request timeout in pkg/shell and proxmoxbe.Timeout.
const Timeout = 20 * time.Second

// DefaultTimeout bounds a wait the caller did not size itself, mirroring
// proxmoxbe.DefaultTimeout.
const DefaultTimeout = 5 * time.Minute

// New builds a client. It fails on a configuration that cannot work rather
// than at the first request, so `corral context add` can reject it
// immediately, per the proxmoxbe.New precedent. It does not dial: the session
// starts on the first Connect.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("vsphere: host is required (a vCenter endpoint, e.g. vc.example.com)")
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return nil, fmt.Errorf("vsphere: username is required (e.g. administrator@vsphere.local)")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("vsphere: password is required; set %s (see ConfigForContext)", PasswordEnv)
	}
	endpoint, err := endpointURL(cfg.Host)
	if err != nil {
		return nil, err
	}
	if cfg.Fingerprint != "" {
		if _, err := tlspin.Parse(cfg.Fingerprint); err != nil {
			return nil, fmt.Errorf("vsphere: fingerprint %q is not usable: %w", cfg.Fingerprint, err)
		}
	}
	if cfg.Fingerprint != "" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("vsphere: host %q is not https, so the pinned fingerprint could never be checked", cfg.Host)
	}
	return &Client{cfg: cfg}, nil
}

// WithVimClient replaces the session client. Tests point this at the
// govmomi/simulator; nothing in production does.
func (c *Client) WithVimClient(vc *vim25.Client) *Client {
	c.vim = vc
	return c
}

// Host is the configured vCenter endpoint.
func (c *Client) Host() string { return c.cfg.Host }

// Connect opens the SOAP session, unless tests already injected one. It is
// idempotent: a live client is returned as-is, so every operation can ensure
// a session without tracking login state itself.
func (c *Client) Connect(ctx context.Context) error {
	if c.vim != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	endpoint, err := endpointURL(c.cfg.Host)
	if err != nil {
		return err
	}
	var soapClient *soap.Client
	if c.cfg.Fingerprint != "" {
		pin, err := tlspin.Parse(c.cfg.Fingerprint)
		if err != nil {
			return fmt.Errorf("vsphere: %w", err)
		}
		// A pin wins over insecure, as on the other backends.
		soapClient = soap.NewClient(endpoint, false)
		pinEndpoint(soapClient, endpoint, pin)
	} else {
		soapClient = soap.NewClient(endpoint, c.cfg.Insecure)
	}
	if testRootCAs != nil {
		soapClient.DefaultTransport().TLSClientConfig.RootCAs = testRootCAs
	}
	vimClient, err := vim25.NewClient(ctx, soapClient)
	if err != nil {
		return fmt.Errorf("vsphere: connect to %s: %w", c.cfg.Host, err)
	}
	credentials := url.UserPassword(c.cfg.Username, c.cfg.Password)
	if err := session.NewManager(vimClient).Login(ctx, credentials); err != nil {
		return fmt.Errorf("vsphere: login to %s as %s: %w", c.cfg.Host, c.cfg.Username, err)
	}
	c.vim = vimClient
	return nil
}

// endpointURL normalises a configured host into the SOAP endpoint URL.
func endpointURL(host string) (*url.URL, error) {
	base := strings.TrimSpace(host)
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("vsphere: host %q: %w", host, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("vsphere: host %q is not usable", host)
	}
	parsed.Path = "/sdk/vim25"
	return parsed, nil
}

// testRootCAs replaces the system trust store in tests, so a test can stand
// up a server whose certificate the client would trust without a pin. nil in
// production.
var testRootCAs *x509.CertPool

// pinEndpoint makes the vCenter endpoint trusted exactly when its leaf
// certificate matches the pin, whatever the system trust store says.
//
// govmomi's own thumbprint support is not enough: its dialer verifies against
// the system trust store first and consults the thumbprint only when that
// fails, so a publicly trusted certificate that does not match the pin would
// be accepted. Connections to the pinned host:port therefore dial with the
// pin's own TLS config. Other hosts (ESXi hosts that an NFC export lease
// points at, whose thumbprints vCenter reports over the pinned session) keep
// govmomi's dialer. The VerifyConnection hook covers the remaining path, a
// proxied request, where net/http does the handshake with the transport's
// TLS config instead of calling the dialer: for the pinned host it requires
// the pin as well (on top of normal verification, so it fails closed).
func pinEndpoint(sc *soap.Client, endpoint *url.URL, pin tlspin.Pin) {
	t := sc.DefaultTransport()
	host := endpoint.Hostname()
	port := endpoint.Port()
	if port == "" {
		port = "443"
	}
	pinnedAddr := net.JoinHostPort(host, port)

	t.TLSClientConfig.VerifyConnection = func(cs tls.ConnectionState) error {
		if !strings.EqualFold(cs.ServerName, host) {
			return nil
		}
		raw := make([][]byte, 0, len(cs.PeerCertificates))
		for _, cert := range cs.PeerCertificates {
			raw = append(raw, cert.Raw)
		}
		return pin.Verify(raw, nil)
	}

	govmomiDial := t.DialTLSContext
	t.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !strings.EqualFold(addr, pinnedAddr) {
			return govmomiDial(ctx, network, addr)
		}
		config := pin.ClientConfig()
		config.ServerName = host
		dialer := &tls.Dialer{Config: config}
		return dialer.DialContext(ctx, network, addr)
	}
}
