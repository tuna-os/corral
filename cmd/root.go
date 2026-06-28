package cmd

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tuna-os/corral/pkg/plugin"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/spf13/cobra"
)

var registryStore *registry.Store

var rootCmd = &cobra.Command{
	Use:   "corral",
	Short: "Corral — herd your VMs into your tailnet",
	Long: `Corral manages virtual machines across QEMU (local)
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
		p := tea.NewProgram(newTUIModel(), tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if postQuitAction != nil {
			postQuitAction()
		}
	},
}

func Execute() {
	// kubectl-style plugin dispatch: if the first arg isn't a built-in command
	// or flag but a `corral-<arg>` plugin exists, hand off to it.
	if len(os.Args) > 1 {
		first := os.Args[1]
		if first != "" && first[0] != '-' {
			if c, _, err := rootCmd.Find(os.Args[1:]); (err != nil || c == rootCmd) && plugin.IsInstalled(first) {
				if err := plugin.Dispatch(first, os.Args[2:]); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				return
			}
		}
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
