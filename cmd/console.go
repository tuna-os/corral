package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/web"
)

var consoleAddr string
var consoleNotifyReady bool

var consoleCmd = &cobra.Command{
	Use:   "console <name>",
	Short: "Serve a VM's VNC console on a local URL for browsers and scripts",
	Long: `Serve one VM's VNC console as a standalone web page and print its URL.

The page shows only the VM's screen, at 1:1 scale (add ?scale=fit to scale
it to the window). It sets data-connected="true" on <body> when the console
is up, gives the #vnc-canvas canvas keyboard focus, and reconnects after a
guest reboot or logout. The same page is at /console/<namespace>/<name> on
every 'corral web' server.

--addr defaults to 127.0.0.1:0, so the OS chooses a free port. With --json
the URL is printed as one JSON line:

  {"vm":"desk","namespace":"corral-vms","url":"http://127.0.0.1:41234/console/corral-vms/desk","websocket":"ws://127.0.0.1:41234/api/vnc/corral-vms/desk"}

With --json --notify-ready, the first line's standalone page URL contains a
per-listener readiness nonce (the websocket URL is unchanged). After that
page's actual noVNC RFB connection, a second JSON line is emitted once:

  {"event":"rfb-connected","vm":"desk","namespace":"corral-vms"}

Opening the page or upgrading a websocket does not report readiness. Keep
the command alive while using its console; SIGINT or SIGTERM stops it.`,
	Example: `  corral console desk
  corral --json console desk   # one JSON line for scripts (Playwright, etc.)
  corral --json console qemu//local/desk --notify-ready`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		vm, err := resolveVM(args[0])
		if err != nil {
			return err
		}
		if !vm.Capabilities.VNC {
			return fmt.Errorf("%s has no VNC console", vm.Name)
		}
		if !vm.Running {
			fmt.Fprintf(os.Stderr, "%s is not running; the console connects when it starts.\n", vm.Name)
		}
		if rootDemo {
			web.EnableDemo()
		}
		ctx, stop := signalContext(cmd.Context())
		defer stop()
		_, err = runConsole(ctx, os.Stdout, vm, consoleAddr, consoleNotifyReady)
		return err
	},
}

// consoleEndpoint is what `corral console` prints once it listens.
type consoleEndpoint struct {
	VM        string `json:"vm"`
	Namespace string `json:"namespace"`
	Context   string `json:"context,omitempty"`
	URL       string `json:"url"`
	WebSocket string `json:"websocket"`
}

type consoleReadyEvent struct {
	Event     string `json:"event"`
	VM        string `json:"vm"`
	Namespace string `json:"namespace"`
}

// newConsoleEndpoint builds vm's console page and websocket URLs on the web
// server at base (http://host:port). The context query matches what the
// dashboard sends, so both reach the same backend context.
func newConsoleEndpoint(base string, vm types.VM) consoleEndpoint {
	path := "/" + url.PathEscape(vm.Namespace) + "/" + url.PathEscape(vm.Name)
	query := ""
	if vm.Context != "" {
		query = "?context=" + url.QueryEscape(vm.Context)
	}
	return consoleEndpoint{
		VM:        vm.Name,
		Namespace: vm.Namespace,
		Context:   vm.Context,
		URL:       base + "/console" + path + query,
		WebSocket: "ws" + strings.TrimPrefix(base, "http") + "/api/vnc" + path + query,
	}
}

// runConsole binds addr, prints vm's console URL to out (one JSON line with
// --json), and serves until ctx ends. It returns the exact bound server after
// cleanup, so retirement checks cannot mistake a new owner of its old port.
func runConsole(ctx context.Context, out io.Writer, vm types.VM, addr string, notifyReady bool) (*web.Server, error) {
	if notifyReady && !jsonOutput {
		return nil, fmt.Errorf("--notify-ready requires --json")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv, err := web.Listen(addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = srv.Close() }()
	endpoint := newConsoleEndpoint(srv.URL(), vm)
	var ready <-chan struct{}
	if notifyReady {
		endpoint.URL, ready, err = srv.NotifyConsoleReady(ctx, vm.Namespace, vm.Name, vm.Context)
		if err != nil {
			return srv, err
		}
	}
	if jsonOutput {
		if notifyReady {
			err = writeConsoleJSON(ctx, out, endpoint)
		} else {
			err = json.NewEncoder(out).Encode(endpoint)
		}
	} else {
		_, err = fmt.Fprintf(out, "Console of %s: %s\n", vm.Name, endpoint.URL)
	}
	if err != nil {
		if notifyReady && ctx.Err() != nil {
			return srv, nil
		}
		return srv, err
	}
	if !notifyReady {
		return srv, srv.Serve(ctx)
	}
	served := make(chan error, 1)
	go func() {
		err := srv.Serve(ctx)
		cancel()
		served <- err
	}()
	select {
	case <-ready:
		err = writeConsoleJSON(ctx, out, consoleReadyEvent{Event: "rfb-connected", VM: vm.Name, Namespace: vm.Namespace})
		if err != nil {
			canceled := ctx.Err() != nil
			cancel()
			if serveErr := <-served; serveErr != nil {
				return srv, serveErr
			}
			if canceled {
				return srv, nil
			}
			return srv, err
		}
		return srv, <-served
	case err := <-served:
		return srv, err
	}
}

// A blocked output pipe must not hold HTTP handlers or the listener alive after
// cancellation. There are at most two writes, serialized by runConsole; the
// writer is caller-owned and must not be closed here.
func writeConsoleJSON(ctx context.Context, out io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	written := make(chan error, 1)
	go func() {
		if err := ctx.Err(); err != nil {
			written <- err
			return
		}
		n, err := out.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		written <- err
	}()
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func init() {
	rootCmd.AddCommand(consoleCmd)
	consoleCmd.Flags().StringVar(&consoleAddr, "addr", "127.0.0.1:0", "Listen address (port 0 lets the OS choose a free port)")
	consoleCmd.Flags().BoolVar(&consoleNotifyReady, "notify-ready", false, "With --json, report one rfb-connected event after this page's real noVNC connection")
}
