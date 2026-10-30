// Package web serves the Corral web UI: a Proxmox-style dashboard over both
// backends — KubeVirt VMs/CTs and this host's local QEMU VMs (#91) — with
// in-browser VNC (noVNC) and serial TTY (xterm.js) consoles. It shares the
// registry and cluster state with the CLI/TUI, so both can be used in tandem.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

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

// Server is the web UI bound to its listener and ready to serve.
type Server struct {
	listener net.Listener
	handler  http.Handler
	url      string
}

// Listen prepares the web UI and binds addr. Port 0 lets the OS choose a
// free port; URL reports the address the server actually took.
func Listen(addr string, allowedHosts ...string) (*Server, error) {
	state, err := NewServerState()
	if err != nil {
		return nil, err
	}
	// Sync ServerState into package globals for handlers that haven't migrated yet.
	store = state.GetStore()
	consoleDialer = state.GetConsoleDialer()
	defaultRunner = state.GetRunner()
	_ = state // Retain reference for future handler migration to accept *ServerState parameter
	kubevirt.ProxyTags = config.TailnetTags()

	// Assemble theme: config.yaml → CLI flags override → serve.
	if cfg, err := config.Load(""); err == nil {
		loadThemeFromConfig(cfg)
	}
	applyCLITheme() // CLI flags always win over config file

	mux, err := newMux()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if len(allowedHosts) == 0 {
		allowedHosts = loopbackAllowedHosts(addr, listener.Addr())
	}
	if len(allowedHosts) > 0 {
		mux, err = enforceAllowedHosts(mux, allowedHosts)
		if err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	return &Server{listener: listener, handler: mux, url: listenURL(addr, listener.Addr())}, nil
}

// listenURL is the base URL a client on this host uses to reach a listener
// bound from bindAddr. It keeps the bind host, because a loopback bind pins
// that exact Host value (see loopbackAllowedHosts), and takes the port from
// the listener, because bindAddr can ask for port 0. A wildcard bind has no
// host to keep, so it gets "localhost".
func listenURL(bindAddr string, listenerAddr net.Addr) string {
	host, _, err := net.SplitHostPort(bindAddr)
	if err != nil {
		host = ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	_, port, err := net.SplitHostPort(listenerAddr.String())
	if err != nil {
		return "http://" + listenerAddr.String()
	}
	return "http://" + net.JoinHostPort(host, port)
}

// URL is the server's base URL, for example http://127.0.0.1:8006.
func (s *Server) URL() string { return s.url }

// Serve starts the background samplers and serves requests until ctx ends.
// Then it stops accepting connections and returns nil.
func (s *Server) Serve(ctx context.Context) error {
	startMetricSampler()
	startVDIReclaim(ctx)
	if MetricsEnabled {
		StartMetrics(ctx)
		fmt.Fprintln(os.Stderr, "Prometheus metrics on /metrics")
	}
	srv := &http.Server{Handler: s.handler}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err := srv.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		<-stopped
		return nil
	}
	return err
}
