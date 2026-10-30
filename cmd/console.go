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

The command serves until it gets SIGINT or SIGTERM.`,
	Example: `  corral console desk
  corral --json console desk   # one JSON line for scripts (Playwright, etc.)`,
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
		return runConsole(ctx, os.Stdout, vm, consoleAddr)
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
// --json), and serves until ctx ends.
func runConsole(ctx context.Context, out io.Writer, vm types.VM, addr string) error {
	srv, err := web.Listen(addr)
	if err != nil {
		return err
	}
	endpoint := newConsoleEndpoint(srv.URL(), vm)
	if jsonOutput {
		err = json.NewEncoder(out).Encode(endpoint)
	} else {
		_, err = fmt.Fprintf(out, "Console of %s: %s\n", vm.Name, endpoint.URL)
	}
	if err != nil {
		return err
	}
	return srv.Serve(ctx)
}

func init() {
	rootCmd.AddCommand(consoleCmd)
	consoleCmd.Flags().StringVar(&consoleAddr, "addr", "127.0.0.1:0", "Listen address (port 0 lets the OS choose a free port)")
}
