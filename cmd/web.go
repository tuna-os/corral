package cmd

import (
	"github.com/hanthor/corral/pkg/web"
	"github.com/spf13/cobra"
)

var webAddr string

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Serve the Corral web UI (Proxmox-style dashboard)",
	Long: `Serve the Corral web UI: a Proxmox-style dashboard for the KubeVirt
backend with in-browser VNC and serial TTY consoles. Works on mobile.

The web UI shares the registry and cluster state with the CLI and TUI,
so all three can be used in tandem.

By default it binds to 127.0.0.1:8006 (Proxmox's port). To reach it from
other tailnet devices, bind your Tailscale IP:

  corral web --addr "$(tailscale ip -4):8006"

There is no authentication — never bind a public interface.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return web.Serve(webAddr)
	},
}

func init() {
	rootCmd.AddCommand(webCmd)
	webCmd.Flags().StringVar(&webAddr, "addr", "127.0.0.1:8006", "Listen address")
}
