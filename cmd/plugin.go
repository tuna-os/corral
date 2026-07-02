package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/plugin"
)

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Manage Corral extensions (plugins)",
	Long: `Browse, install, and remove Corral extensions.

Plugins are standalone executables named "corral-<name>" in
~/.local/share/corral/plugins. Once installed, run them as "corral <name> …".
The marketplace is a curated index of installable plugins.`,
}

var pluginListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed plugins",
	RunE: func(cmd *cobra.Command, args []string) error {
		ps := plugin.Installed()
		if len(ps) == 0 {
			fmt.Println("No plugins installed. Browse: corral plugin search")
			return nil
		}
		for _, p := range ps {
			fmt.Printf("%-16s %s\n", p.Name, p.Path)
		}
		return nil
	},
}

var pluginSearchCmd = &cobra.Command{
	Use:   "search [term]",
	Short: "Search the marketplace",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		idx, err := plugin.FetchIndex()
		if err != nil {
			return err
		}
		term := ""
		if len(args) == 1 {
			term = args[0]
		}
		fmt.Printf("%-14s %-9s %-9s %s\n", "NAME", "VERSION", "STATUS", "DESCRIPTION")
		for _, e := range idx.Plugins {
			if term != "" && !contains(e.Name+" "+e.Description, term) {
				continue
			}
			status := "available"
			if plugin.IsInstalled(e.Name) {
				status = "installed"
			}
			fmt.Printf("%-14s %-9s %-9s %s\n", e.Name, e.Version, status, e.Description)
		}
		return nil
	},
}

var pluginInstallCmd = &cobra.Command{
	Use:   "install <name>",
	Short: "Install a plugin from the marketplace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		idx, err := plugin.FetchIndex()
		if err != nil {
			return err
		}
		e := idx.Find(args[0])
		if e == nil {
			return fmt.Errorf("no plugin %q in the marketplace (corral plugin search)", args[0])
		}
		if err := e.Install(); err != nil {
			return err
		}
		fmt.Printf("Installed %s %s → run `corral %s`\n", e.Name, e.Version, e.Name)
		return nil
	},
}

var pluginRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"uninstall", "rm"},
	Short:   "Remove an installed plugin",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := plugin.Remove(args[0]); err != nil {
			return err
		}
		fmt.Printf("Removed %s\n", args[0])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pluginCmd)
	pluginCmd.AddCommand(pluginListCmd, pluginSearchCmd, pluginInstallCmd, pluginRemoveCmd)
}

func contains(haystack, needle string) bool {
	h, n := []rune(haystack), []rune(needle)
	if len(n) == 0 {
		return true
	}
	lower := func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := range n {
			if lower(h[i+j]) != lower(n[j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
