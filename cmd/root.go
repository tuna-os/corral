package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "tailvm",
	Short: "TailVM — QEMU/KubeVirt VMs with Tailscale VNC",
	Long: `TailVM manages virtual machines across QEMU (local)
and KubeVirt (Kubernetes) backends, with automatic
Tailscale service exposure for VNC, SSH, RDP, and custom ports.

Run without arguments to launch the interactive TUI.`,
	Run: func(cmd *cobra.Command, args []string) {
		// No subcommand → interactive TUI
		runTUI()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
