package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show corral configuration",
	Long: `Display the current corral configuration.

The config file is at ~/.config/corral/config.yaml.
Environment variable TS_AUTHKEY overrides the config file.

Example config.yaml:
  tailscale:
    auth_key: tskey-...`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load("")
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		fmt.Printf("Config path:  %s\n", config.DefaultPath())
		if key := cfg.Tailscale.AuthKey; key != "" {
			if len(key) > 10 {
				key = key[:10]
			}
			fmt.Printf("Tailscale:    auth key set (%s...)\n", key)
		} else if key := config.AuthKey(); key != "" {
			fmt.Println("Tailscale:    auth key from TS_AUTHKEY env var")
		} else {
			fmt.Println("Tailscale:    no auth key configured")
			fmt.Println("  Set TS_AUTHKEY env var or add to config.yaml:")
			fmt.Println("    tailscale:")
			fmt.Println("      auth_key: tskey-...")
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
}
