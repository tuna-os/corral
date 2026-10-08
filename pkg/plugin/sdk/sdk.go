// Package sdk contains the stable, dependency-light contract implemented by
// Corral extension executables. External authors may emit the same JSON
// without importing this package.
package sdk

import (
	"encoding/json"
	"fmt"
	"os"
)

const API = "corral.plugin/v1"

type Metadata struct {
	API               string   `json:"api"`
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	Description       string   `json:"description,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`
	Permissions       []string `json:"permissions,omitempty"`
	SupportedBackends []string `json:"supportedBackends,omitempty"`
}

// HandleMetadata implements the reserved --corral-plugin-metadata handshake.
// Call it at the beginning of main and return when it reports true.
func HandleMetadata(meta Metadata) bool {
	if len(os.Args) != 2 || os.Args[1] != "--corral-plugin-metadata" {
		return false
	}
	if meta.API == "" {
		meta.API = API
	}
	if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return true
}

// CapHostPower is the capability a plugin declares to power the machines
// that host VMs on and off: an on-demand cloud instance kept stopped when
// idle, a lab box behind a smart plug, a Wake-on-LAN workstation. Core
// knows nothing about any of those; it only calls the plugin:
//
//	corral-<name> host-power list            → JSON []Host on stdout
//	corral-<name> host-power start <host-id> → exit 0 when the request is accepted
//	corral-<name> host-power stop  <host-id> → exit 0 when the request is accepted
//
// start/stop return once the provider has accepted the request; the host
// reaches its new state asynchronously and `list` reports progress.
const CapHostPower = "host-power"

// Host is one power-manageable machine reported by a host-power plugin.
type Host struct {
	// ID is the plugin's own stable identifier, passed back to start/stop.
	ID string `json:"id"`
	// Name is what the UI shows.
	Name string `json:"name"`
	// Node is the Kubernetes node this machine runs, if any, so the UI can
	// tie power state to the node's VMs.
	Node string `json:"node,omitempty"`
	// State is one of running, stopped, starting, stopping, unknown.
	State string `json:"state"`
	// Actions lists what may be requested now ("start", "stop").
	Actions []string `json:"actions,omitempty"`
	// Detail is an optional human-readable note (cost, idle timer, provider state).
	Detail string `json:"detail,omitempty"`
}
