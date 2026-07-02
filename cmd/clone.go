package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/types"
)

var (
	cloneNamespace string
)

var cloneCmd = &cobra.Command{
	Use:   "clone <source-vm> <target-vm>",
	Short: "Clone an existing VM to a new VM",
	Long: `Clone a VM's disk and configuration to a new VM name.
Only supported on the KubeVirt backend.

Examples:
  corral clone myvm myvm-clone`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		src := args[0]
		dst := args[1]

		ns := cloneNamespace
		if ns == "" {
			ns = kubevirt.DefaultNamespace
		}

		backend := resolveBackend(src)
		if backend == "" {
			return fmt.Errorf("source VM %q not found", src)
		}
		if backend != "kubevirt" {
			return fmt.Errorf("cloning is only supported on KubeVirt VMs (VM %q uses backend %q)", src, backend)
		}

		if existing := resolveBackend(dst); existing != "" {
			return fmt.Errorf("target VM %q already exists (backend: %s)", dst, existing)
		}

		fmt.Printf("Cloning VM %q to %q in ns/%s...\n", src, dst, ns)
		if err := kubevirt.NewClient(ns).Clone(src, dst); err != nil {
			return err
		}

		if registryStore != nil {
			if err := registryStore.Set(dst, types.RegistryEntry{
				Backend:   "kubevirt",
				Namespace: ns,
			}); err != nil {
				return fmt.Errorf("saving registry entry: %w", err)
			}
		}

		fmt.Printf("VM %q successfully cloned from %q\n", dst, src)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(cloneCmd)
	cloneCmd.Flags().StringVarP(&cloneNamespace, "namespace", "n", "", "[kubevirt] Namespace")
}
