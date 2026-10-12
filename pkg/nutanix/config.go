package nutanix

// Types-only context configuration.
//
// A Nutanix context is a Prism Central endpoint plus the credentials to
// reach it. The secret is read from the environment first: a homelab config
// file is often in a dotfiles repo, and a password that ends up committed is
// a cluster somebody else can drive. There is deliberately no file field for
// it, and no ConfigForContext here yet — backend wiring (pkg/backend,
// pkg/types, pkg/config) follows post-vSphere-merge, and this package stays
// importable without the config tree until then.

import (
	"os"
	"strings"
)

// PasswordEnv holds the Prism Central password or API key for every Nutanix
// context, and overrides any config file. CORRAL_NUTANIX_PASSWORD_<CONTEXT>
// (uppercased, non-alphanumerics as underscores) overrides it per context,
// for an operator with two Prism Centrals.
const PasswordEnv = "CORRAL_NUTANIX_PASSWORD"

func passwordFromEnv(contextName string) string {
	if v := os.Getenv(PasswordEnv + "_" + envSuffix(contextName)); v != "" {
		return v
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
