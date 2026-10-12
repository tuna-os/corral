package doctor

// vSphere context checks (RFC-0003).
//
// A vCenter context has three distinct ways to be wrong, and each has a
// different remedy: the endpoint is unreachable, the credentials are
// rejected, or the certificate is untrusted. Reporting one message for all
// three is what makes a homelab operator start disabling TLS verification
// out of frustration, so they are reported separately — the same split the
// Proxmox checks draw.

import (
	"context"
	"fmt"
	"strings"

	"github.com/tuna-os/corral/pkg/vsphere"
)

func vsphereChecks(contextName string) []Check {
	// Through the cache, so tests can point the context at the simulator
	// and repeated probes share one session.
	client, err := vsphere.ClientForContext(contextName)
	if err != nil {
		return []Check{{Name: "Endpoint configuration", Detail: err.Error(), Severity: "required"}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), vsphere.Timeout)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		return []Check{vsphereConnectRefusal(err)}
	}

	checks := []Check{{
		Name:     "vCenter reachable",
		OK:       true,
		Severity: "required",
		Detail:   fmt.Sprintf("session established on %s", client.Host()),
	}}
	return append(checks, vsphereHostChecks(ctx, client)...)
}

// vsphereConnectRefusal separates the three failures, because the remedies
// have nothing in common.
func vsphereConnectRefusal(err error) Check {
	detail := err.Error()
	switch {
	case strings.Contains(detail, "InvalidLogin"):
		return Check{Name: "Credentials accepted", Severity: "required",
			Detail: fmt.Sprintf("vCenter rejected the login; verify the username in the context and %s: %s",
				vsphere.PasswordEnv, detail)}
	case strings.Contains(detail, "certificate"):
		return Check{Name: "TLS trust", Severity: "required",
			Detail: fmt.Sprintf("the vCenter certificate is not trusted; pin it with vsphere.fingerprint or set vsphere.insecure for this context: %s", detail)}
	default:
		return Check{Name: "vCenter reachable", Severity: "required",
			Detail: fmt.Sprintf("could not reach the endpoint; verify host, port, and network path: %s", detail)}
	}
}

// vsphereHostChecks reports the estate's own health, which is what decides
// whether a migration has anywhere to go.
func vsphereHostChecks(ctx context.Context, client *vsphere.Client) []Check {
	hosts, err := client.Hosts(ctx)
	if err != nil {
		return []Check{{Name: "Hosts", Severity: "warning", Detail: "could not list hosts: " + err.Error()}}
	}
	checks := []Check{{
		Name:     "Hosts",
		OK:       true,
		Severity: "required",
		Detail:   fmt.Sprintf("%d host(s) responding", len(hosts)),
	}}
	if len(hosts) < 2 {
		checks = append(checks, Check{Name: "Migration target", Severity: "warning",
			Detail: "a single host has nowhere to vMotion to; migrations are refused until a second host joins"})
	}
	return checks
}
