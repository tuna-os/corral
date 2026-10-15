package vsphere

// Context configuration.
//
// A vSphere context is a vCenter endpoint plus the credentials to reach it.
// The password is a secret, so it is read from the environment only: a
// homelab config file is often in a dotfiles repo, and a password that ends
// up committed is an estate somebody else can drive. There is deliberately no
// password field on the file side — unlike Proxmox's token, which the file
// may carry with the environment overriding it.

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/tuna-os/corral/pkg/config"
)

// PasswordEnv holds the password for every vSphere context, and overrides
// nothing in the file because the file carries no password.
// CORRAL_VSPHERE_PASSWORD_<CONTEXT> (uppercased, non-alphanumerics as
// underscores) overrides it per context, for an operator with two estates.
const PasswordEnv = "CORRAL_VSPHERE_PASSWORD"

var (
	clientsMu sync.Mutex
	clients   = map[string]*Client{}
)

// ClientForContext resolves the client for a configured context. Clients are
// cached because building one parses TLS trust, and a fleet listing resolves
// the same context once per operation otherwise. The client is unconnected:
// the session starts on first use, so merely resolving a context never dials.
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

// SetClientForContext installs a client for a context, bypassing
// configuration. Tests use it to point a context at the simulator.
func SetClientForContext(context string, client *Client) {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	clients[context] = client
}

// ResetClients drops the cache, so a configuration change takes effect
// without a restart (and so tests do not leak a client into one another).
func ResetClients() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	clients = map[string]*Client{}
}

// ConfigForContext builds a Config from the named context. The context's own
// Context field is the host, matching how every other backend uses it — a
// kubeconfig context, an Incus remote, a libvirt URI, a PVE endpoint, a
// vCenter endpoint.
func ConfigForContext(context string) (Config, error) {
	for _, target := range config.Contexts() {
		if target.Backend != "vsphere" {
			continue
		}
		if target.Context != context && target.Name != context {
			continue
		}
		cfg := Config{Host: target.Context}
		if target.Vsphere != nil {
			cfg.Username = target.Vsphere.Username
			cfg.Fingerprint = target.Vsphere.Fingerprint
			cfg.Insecure = target.Vsphere.Insecure
		}
		cfg.Password = passwordFromEnv(target.Name)
		if cfg.Username == "" {
			return Config{}, fmt.Errorf("vsphere: context %q has no username; add "+
				"vsphere.username to the context in the config file", target.Name)
		}
		if cfg.Password == "" {
			return Config{}, fmt.Errorf("vsphere: context %q has no password; set %s or %s_%s",
				target.Name, PasswordEnv, PasswordEnv, envSuffix(target.Name))
		}
		return cfg, nil
	}
	return Config{}, fmt.Errorf("vsphere: no configured context matches %q", context)
}

func passwordFromEnv(contextName string) string {
	if password := os.Getenv(PasswordEnv + "_" + envSuffix(contextName)); password != "" {
		return password
	}
	return os.Getenv(PasswordEnv)
}

func envSuffix(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	return sb.String()
}
