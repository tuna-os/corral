package cmd

import (
	"fmt"
	"os"

	"github.com/hanthor/tailvm-go/pkg/registry"
	"github.com/spf13/cobra"
)

var registryStore *registry.Store

var rootCmd = &cobra.Command{
	Use:   "tailvm",
	Short: "TailVM — QEMU/KubeVirt VMs with Tailscale VNC",
	Long: `TailVM manages virtual machines across QEMU (local)
and KubeVirt (Kubernetes) backends, with automatic
Tailscale service exposure for VNC, SSH, RDP, and custom ports.

Run without arguments to launch the interactive TUI.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		s, err := registry.NewStore()
		if err != nil {
			return fmt.Errorf("init registry: %w", err)
		}
		registryStore = s
		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {
		runTUI()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
