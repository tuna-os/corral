// Context configuration (client-only slice: types and environment only).
//
// A Xen context is a pool endpoint plus the credentials to reach it. The
// password is a secret, so it comes from the environment first and is never
// read from the config file: a homelab config file is often in a dotfiles
// repo, and a password that ends up committed is a pool somebody else can
// drive.
//
// Binding this to pkg/config contexts (a `xen:` block beside
// `proxmox:`) arrives with the wiring turn, after the vSphere backend
// merges — this file deliberately does not import pkg/config.

package xen

import (
	"fmt"
	"os"
	"strings"
)

// PasswordEnv holds the XenAPI password for every Xen context, and
// overrides anything else. CORRAL_XEN_PASSWORD_<CONTEXT> (uppercased,
// non-alphanumerics as underscores) overrides it per context, for an
// operator with two pools.
const PasswordEnv = "CORRAL_XEN_PASSWORD"

// Stored is the file half of a Xen context: everything but the secret. It
// exists so the shape of the future config-file binding is settled now —
// host, user, and trust — while the password stays out of the file by
// construction (there is no field for it).
type Stored struct {
	Host        string
	Username    string
	Fingerprint string
	Insecure    bool
}

// Resolve combines the stored endpoint with the password from the
// environment into a client Config, failing fast when either half is
// missing.
func (s Stored) Resolve(contextName string) (Config, error) {
	cfg := Config{
		Host:        s.Host,
		Username:    s.Username,
		Fingerprint: s.Fingerprint,
		Insecure:    s.Insecure,
		Password:    PasswordFromEnv(contextName),
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return Config{}, fmt.Errorf("xen: context %q has no host", contextName)
	}
	if strings.TrimSpace(cfg.Username) == "" {
		cfg.Username = "root"
	}
	if cfg.Password == "" {
		return Config{}, fmt.Errorf("xen: context %q has no password; set %s or %s_%s",
			contextName, PasswordEnv, PasswordEnv, envSuffix(contextName))
	}
	return cfg, nil
}

// PasswordFromEnv returns the password for a context: the per-context
// variable first, then the shared one.
func PasswordFromEnv(contextName string) string {
	if pw := os.Getenv(PasswordEnv + "_" + envSuffix(contextName)); pw != "" {
		return pw
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
