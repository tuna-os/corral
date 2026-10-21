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

// ServerState encapsulates dependency injection for HTTP handlers.
// It provides controlled access to shared resources without global mutable state.
type ServerState struct {
	store         *registry.Store
	consoleDialer kubevirt.ConsoleDialer
	commandRunner shell.Runner
	metrics       *cpuRing
}

// NewServerState initializes ServerState with production defaults.
func NewServerState() (*ServerState, error) {
	s, err := registry.NewStore()
	if err != nil {
		s = nil
	}
	runner := defaultRunner
	if runner == nil {
		runner = shell.DefaultKubectl
	}
	return &ServerState{
		store:         s,
		metrics:       newCPURing(kubevirt.SampleAllUsage),
		consoleDialer: kubevirt.RealConsoleDialer{},
		commandRunner: runner,
	}, nil
}

// GetStore returns the registry store.
func (ss *ServerState) GetStore() *registry.Store {
	return ss.store
}

// GetConsoleDialer returns the console dialer.
func (ss *ServerState) GetConsoleDialer() kubevirt.ConsoleDialer {
	return ss.consoleDialer
}

// GetRequestRunner returns a shell runner bound to the request context.
func (ss *ServerState) GetRequestRunner(r *http.Request) shell.Runner {
	return shell.Bind(r.Context(), ss.commandRunner)
}

// GetRunner returns the command runner.
func (ss *ServerState) GetRunner() shell.Runner {
	return ss.commandRunner
}

// SetRunner updates the command runner. Used primarily for testing.
func (ss *ServerState) SetRunner(runner shell.Runner) {
	ss.commandRunner = runner
	defaultRunner = runner // Sync to global for backward compatibility with existing handlers
}

// defaultRunner is the command runner used by handlers that shell out
// (vmiIndex, handleNodes, handleExport). Defaults to shell.Real; set in tests.
// TODO: Once all handlers accept ServerState via context, this global can be removed.
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
	state, err := NewServerState()
	if err != nil {
		return err
	}
	// Sync ServerState into package globals for handlers that haven't migrated yet.
	store = state.GetStore()
	consoleDialer = state.GetConsoleDialer()
	defaultRunner = state.GetRunner()
	kubevirt.ProxyTags = config.TailnetTags()

	// Assemble theme: config.yaml → CLI flags override → serve.
	if cfg, err := config.Load(""); err == nil {
		loadThemeFromConfig(cfg)
	}
	applyCLITheme() // CLI flags always win over config file

	mux, err := newMuxWithMetrics(state.metrics)
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
	samplerCtx, cancelSampler := context.WithCancel(context.Background())
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		state.metrics.runSampler(samplerCtx)
	}()
	defer func() { cancelSampler(); <-samplerDone }()
	startVDIReclaim(context.Background())
	if MetricsEnabled {
		StartMetrics(context.Background())
		fmt.Fprintln(os.Stderr, "Prometheus metrics on /metrics")
	}
	fmt.Fprintf(os.Stderr, "Corral web UI listening on http://%s\n", listener.Addr())
	return (&http.Server{Handler: mux}).Serve(listener)
}
