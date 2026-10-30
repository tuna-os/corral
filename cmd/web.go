package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/web"
)

var (
	webAddr          string
	webAllowedHosts  []string
	webAccent        string
	webBrandTitle    string
	webBrandEmoji    string
	webBrandSubtitle string
	webCustomCSS     string
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Serve the Corral web UI (Proxmox-style dashboard)",
	Long: `Serve the Corral web UI: a Proxmox-style dashboard with in-browser
VNC and serial TTY consoles. Works on mobile.

Shows both backends: KubeVirt VMs and CTs from the cluster, plus this
host's local QEMU VMs under a "local" node (lifecycle, VNC console, info).

The web UI shares the registry and cluster state with the CLI and TUI,
so all three can be used in tandem.

By default it binds to 127.0.0.1:8006 (Proxmox's port). To reach it from
other tailnet devices, bind your Tailscale IP.

Loopback listeners accept only their literal host, localhost alias, and bound
port by default, which prevents DNS rebinding. Use --allowed-host to pin
additional exact Host values; this is useful when a trusted reverse proxy has
a different public host.

There is no authentication — never bind a public interface.

--demo serves a built-in fake cluster (VMs, CTs, nodes, live metrics) so
you can explore the dashboard — or develop on it — with no cluster at all.`,
	Example: `  corral web
  corral web --addr "$(tailscale ip -4):8006"
  corral web --demo   # explore the UI without a cluster
  corral web --accent "#3b82f6" --brand-emoji "⚡"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if rootDemo {
			web.EnableDemo()
		}
		// CLI flags override config file; the theme is assembled inside web.Listen.
		web.SetCLITheme(webAccent, webBrandTitle, webBrandEmoji, webBrandSubtitle, webCustomCSS)
		ctx, stop := signalContext(cmd.Context())
		defer stop()
		return runWeb(ctx, os.Stdout, os.Stderr, webAddr, webAllowedHosts)
	},
}

// webListening is what `corral --json web` prints once it listens.
type webListening struct {
	URL string `json:"url"`
}

// runWeb binds addr and reports the URL it took: a line on errOut, or one
// JSON line on out with --json. Then it serves until ctx ends.
func runWeb(ctx context.Context, out, errOut io.Writer, addr string, allowedHosts []string) error {
	srv, err := web.Listen(addr, allowedHosts...)
	if err != nil {
		return err
	}
	if jsonOutput {
		err = json.NewEncoder(out).Encode(webListening{URL: srv.URL()})
	} else {
		_, err = fmt.Fprintf(errOut, "Corral web UI listening on %s\n", srv.URL())
	}
	if err != nil {
		return err
	}
	return srv.Serve(ctx)
}

// signalContext ends on SIGINT or SIGTERM, so a server can stop cleanly.
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

func init() {
	rootCmd.AddCommand(webCmd)
	webCmd.Flags().StringVar(&webAddr, "addr", "127.0.0.1:8006", "Listen address")
	webCmd.Flags().StringSliceVar(&webAllowedHosts, "allowed-host", nil,
		"exact HTTP Host value allowed by the server (repeat for trusted aliases; loopback binds are pinned automatically)")
	webCmd.Flags().BoolVar(&web.MetricsEnabled, "metrics", false,
		"Serve Prometheus metrics on /metrics (starts a background fleet collector)")
	webCmd.Flags().StringVar(&webAccent, "accent", "", "Accent colour (CSS hex, e.g. #3b82f6)")
	webCmd.Flags().StringVar(&webBrandTitle, "brand-title", "", "Brand title shown in the header")
	webCmd.Flags().StringVar(&webBrandEmoji, "brand-emoji", "", "Emoji shown left of the brand title")
	webCmd.Flags().StringVar(&webBrandSubtitle, "brand-subtitle", "", "Subtitle text next to the brand title")
	webCmd.Flags().StringVar(&webCustomCSS, "custom-css", "", "Path to a CSS file whose contents are injected into the page")
}
