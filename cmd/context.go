package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/config"
)

var (
	contextBackend string
	contextValue   string
)

var contextCmd = &cobra.Command{
	Use:     "context",
	Aliases: []string{"ctx"},
	Short:   "Manage the local, Kubernetes, Incus, libvirt, Proxmox, VirtualBox, vSphere, Xen, and Nutanix contexts aggregated by Corral",
}

var contextGetCmd = &cobra.Command{
	Use:     "get",
	Short:   "Show the default context",
	Example: "  corral context get",
	Args:    cobra.NoArgs,
	Run: func(*cobra.Command, []string) {
		c := config.DefaultContext()
		fmt.Printf("%s\t%s\t%s\n", c.Name, c.Backend, c.Context)
	},
}

var contextUseCmd = &cobra.Command{
	Use:     "use <name>",
	Aliases: []string{"set", "switch"},
	Short:   "Select the default destination without hiding other contexts",
	Example: "  corral context use prod   # sets prod as default; the others stay available",
	Args:    cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return config.SetDefaultContext(args[0])
	},
}

var contextAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add or update an inventory context",
	Example: `  corral context add local --backend qemu
  corral context add prod --backend kubevirt --context prod-cluster`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return config.AddContext(config.ContextConfig{Name: args[0], Backend: contextBackend, Context: contextValue})
	},
}

var contextRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"delete", "rm"},
	Short:   "Remove an inventory context entry (does not delete a VM)",
	Example: "  corral context remove prod   # drops the inventory entry; the VM itself is untouched",
	Args:    cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return config.RemoveContext(args[0])
	},
}

var contextListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List contexts, marking the default with *",
	Example: "  corral context list",
	Args:    cobra.NoArgs,
	Run: func(*cobra.Command, []string) {
		current := config.DefaultContext().Name
		for _, c := range config.Contexts() {
			mark := " "
			if c.Name == current {
				mark = "*"
			}
			fmt.Printf("%s %-18s %-10s %s\n", mark, c.Name, c.Backend, c.Context)
		}
	},
}

func init() {
	contextAddCmd.Flags().StringVarP(&contextBackend, "backend", "b", "", "Backend: qemu, kubevirt, incus, libvirt, proxmox, virtualbox, vsphere, xen, or nutanix")
	contextAddCmd.Flags().StringVar(&contextValue, "context", "", "kubeconfig context, Incus remote, libvirt URI, VirtualBox context label, vCenter host, Xen pool master, or Prism Central VIP")
	_ = contextAddCmd.MarkFlagRequired("backend")
	contextCmd.AddCommand(contextGetCmd, contextUseCmd, contextAddCmd, contextRemoveCmd, contextListCmd)
	rootCmd.AddCommand(contextCmd)
}
