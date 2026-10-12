package doctor

// Nutanix context checks (RFC-0003).
//
// A Prism Central context has three distinct ways to be wrong, and each has
// a different remedy: the endpoint is unreachable, the credentials are
// rejected, or the certificate is untrusted. Reporting one message for all
// three is what makes a homelab operator start disabling TLS verification
// out of frustration, so they are reported separately — the same split the
// Proxmox and vSphere checks draw. v4 authenticates per request, so the
// probe is a one-VM list page, not a session login.

import (
	"fmt"
	"strings"

	"github.com/tuna-os/corral/pkg/nutanix"
)

func nutanixChecks(contextName string) []Check {
	// Through the cache, so tests can point the context at a fake Prism
	// Central and repeated probes share one client.
	client, err := nutanix.ClientForContext(contextName)
	if err != nil {
		return []Check{{Name: "Endpoint configuration", Detail: err.Error(), Severity: "required"}}
	}

	if err := client.Probe(); err != nil {
		return []Check{nutanixConnectRefusal(err)}
	}

	checks := []Check{{
		Name:     "Prism Central reachable",
		OK:       true,
		Severity: "required",
		Detail:   fmt.Sprintf("v4 API answers on %s", client.Host()),
	}}
	return append(checks, nutanixMigrateNote())
}

// nutanixConnectRefusal separates the three failures, because the remedies
// have nothing in common.
func nutanixConnectRefusal(err error) Check {
	detail := err.Error()
	switch {
	case strings.Contains(detail, "401") || strings.Contains(detail, "403"):
		return Check{Name: "Credentials accepted", Severity: "required",
			Detail: fmt.Sprintf("Prism Central rejected the credentials; verify the username in the context and %s: %s",
				nutanix.PasswordEnv, detail)}
	case strings.Contains(detail, "certificate"):
		return Check{Name: "TLS trust", Severity: "required",
			Detail: fmt.Sprintf("the Prism Central certificate is not trusted; pin it with nutanix.fingerprint or set nutanix.insecure for this context: %s", detail)}
	default:
		return Check{Name: "Prism Central reachable", Severity: "required",
			Detail: fmt.Sprintf("could not reach the endpoint; verify host, port, and network path: %s", detail)}
	}
}

// nutanixMigrateNote warns that migration pre-flight is approximate: without
// the clustermgmt host listing there is no second-host check, so an explicit
// host extId is required and blind targets fail at action time.
func nutanixMigrateNote() Check {
	return Check{Name: "Migration targets", Severity: "warning",
		Detail: "host listing lands with wiring; until then CanMigrate reads power state only and Migrate needs an explicit host extId"}
}
