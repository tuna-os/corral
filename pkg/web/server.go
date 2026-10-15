// Package web serves the Corral web UI: a Proxmox-style dashboard over both
// backends — KubeVirt VMs/CTs and this host's local QEMU VMs (#91) — with
// in-browser VNC (noVNC) and serial TTY (xterm.js) consoles. It shares the
// registry and cluster state with the CLI/TUI, so both can be used in tandem.
package web

import (
	"context"
	"embed"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/shell"
)

// defaultRunner is the command runner used by handlers that shell out
// (vmiIndex, handleNodes, handleExport). Defaults to shell.Real; set in tests.
var defaultRunner shell.Runner = shell.DefaultKubectl

// requestRunner is defaultRunner bound to r, so the commands a handler runs
// log the request ID.
func requestRunner(r *http.Request) shell.Runner {
	return shell.Bind(r.Context(), defaultRunner)
}

//go:embed static
var staticFS embed.FS

var store *registry.Store

// Serve starts the web UI on addr and blocks.
func Serve(addr string, allowedHosts ...string) error {
	if s, err := registry.NewStore(); err == nil {
		store = s
	}
	kubevirt.ProxyTags = config.TailnetTags()

	// Assemble theme: config.yaml → CLI flags override → serve.
	if cfg, err := config.Load(""); err == nil {
		loadThemeFromConfig(cfg)
	}
	applyCLITheme() // CLI flags always win over config file

	mux, err := newMux()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if len(allowedHosts) == 0 {
		allowedHosts = loopbackAllowedHosts(addr, listener.Addr())
	}
	if len(allowedHosts) > 0 {
		mux, err = enforceAllowedHosts(mux, allowedHosts)
		if err != nil {
			_ = listener.Close()
			return err
		}
	}
	startMetricSampler()
	if MetricsEnabled {
		StartMetrics(context.Background())
		fmt.Fprintln(os.Stderr, "Prometheus metrics on /metrics")
	}
	fmt.Fprintf(os.Stderr, "Corral web UI listening on http://%s\n", listener.Addr())
	return (&http.Server{Handler: mux}).Serve(listener)
}
