// Context configuration.
//
// A Nutanix context is a Prism Central endpoint plus the credentials to
// reach it. The secret is read from the environment first: a homelab config
// file is often in a dotfiles repo, and a password that ends up committed is
// a cluster somebody else can drive. The config file carries no secret by
// construction.

package nutanix

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/tuna-os/corral/pkg/config"
)

// PasswordEnv holds the Prism Central password or API key for every Nutanix
// context, and overrides the config file — which carries no secret by
// construction. CORRAL_NUTANIX_PASSWORD_<CONTEXT> (uppercased,
// non-alphanumerics as underscores) overrides it per context, for an
// operator with two Prism Centrals.
const PasswordEnv = "CORRAL_NUTANIX_PASSWORD"

var (
	clientsMu sync.Mutex
	clients   = map[string]*Client{}
)

// ClientForContext resolves the client for a configured context. Clients are
// cached because building one parses TLS trust, and a fleet listing resolves
// the same context once per operation otherwise. The client is
// unauthenticated: v4 authenticates per request, so merely resolving a
// context never dials.
func ClientForContext(context string) (*Client, error) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if client, ok := clients[context]; ok {
		return client, nil
	}
	cfg, err := ConfigForContext(context)
	if err != nil {
		return nil, err
	}
	client, err := New(cfg)
	if err != nil {
		return nil, err
	}
	clients[context] = client
	return client, nil
}

// SetClientForContext installs a client for a context, bypassing configuration.
// Tests use it to point a context at an httptest server.
func SetClientForContext(context string, client *Client) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	clients[context] = client
}

// ResetClients drops the cache, so a configuration change takes effect without a
// restart (and so tests do not leak a client into one another).
func ResetClients() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	clients = map[string]*Client{}
}

// ConfigForContext builds a Config from the named context. The context's own
// Context field is the host, matching how every other backend uses it — a
// kubeconfig context, an Incus remote, a libvirt URI, a PVE endpoint, a
// vCenter endpoint, a pool master, a Prism Central VIP.
func ConfigForContext(context string) (Config, error) {
	for _, target := range config.Contexts() {
		if target.Backend != "nutanix" {
			continue
		}
		if target.Context != context && target.Name != context {
			continue
		}
		cfg := Config{Host: target.Context}
		if target.Nutanix != nil {
			cfg.Username = target.Nutanix.Username
			cfg.Fingerprint = target.Nutanix.Fingerprint
			cfg.Insecure = target.Nutanix.Insecure
			cfg.APIVersion = target.Nutanix.APIVersion
		}
		if cfg.Username == "" {
			cfg.Username = "admin"
		}
		cfg.Secret = PasswordFromEnv(target.Name)
		if cfg.Secret == "" {
			return Config{}, fmt.Errorf("nutanix: context %q has no password or API key; set %s or %s_%s",
				target.Name, PasswordEnv, PasswordEnv, envSuffix(target.Name))
		}
		return cfg, nil
	}
	return Config{}, fmt.Errorf("nutanix: no configured context matches %q", context)
}

// PasswordFromEnv returns the secret for a context: the per-context
// variable first, then the shared one.
func PasswordFromEnv(contextName string) string {
	if pw := os.Getenv(PasswordEnv + "_" + envSuffix(contextName)); pw != "" {
		return pw
	}
	return os.Getenv(PasswordEnv)
}

func envSuffix(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(name)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
