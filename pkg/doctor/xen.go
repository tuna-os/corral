package doctor

import (
	"fmt"
	"strings"

	"github.com/tuna-os/corral/pkg/xen"
)

func xenChecks(contextName string) []Check {
	// Through the cache, so tests can point the context at a fake pool
	// and repeated probes share one session.
	client, err := xen.ClientForContext(contextName)
	if err != nil {
		return []Check{{Name: "Endpoint configuration", Detail: err.Error(), Severity: "required"}}
	}

	if _, err := client.Login(); err != nil {
		return []Check{xenConnectRefusal(err)}
	}

	checks := []Check{{
		Name:     "Pool reachable",
		OK:       true,
		Severity: "required",
		Detail:   fmt.Sprintf("session established on %s", client.Host()),
	}}
	return append(checks, xenHostChecks(client)...)
}

func xenConnectRefusal(err error) Check {
	detail := err.Error()
	switch {
	case strings.Contains(detail, "SESSION_AUTHENTICATION_FAILED"):
		return Check{Name: "Credentials accepted", Severity: "required",
			Detail: fmt.Sprintf("the pool rejected the login; verify the username in the context and %s: %s",
				xen.PasswordEnv, detail)}
	case strings.Contains(detail, "certificate"):
		return Check{Name: "TLS trust", Severity: "required",
			Detail: fmt.Sprintf("the pool certificate is not trusted; pin it with xen.fingerprint or set xen.insecure for this context: %s", detail)}
	default:
		return Check{Name: "Pool reachable", Severity: "required",
			Detail: fmt.Sprintf("could not reach the endpoint; verify host, port, and network path: %s", detail)}
	}
}

func xenHostChecks(client *xen.Client) []Check {
	hosts, err := client.Hosts()
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
			Detail: "a single host has nowhere to migrate to; migrations are refused until a second host joins"})
	}
	return checks
}
