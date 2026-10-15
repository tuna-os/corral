package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/types"
)

var (
	cloneNamespace string
)

var cloneCmd = &cobra.Command{
	Use:   "clone <source-vm> <target-vm>",
	Short: "Clone an existing VM to a new VM",
	Long: `Clone a VM's disk and configuration to a new VM name, through the
backend's own clone mechanism. Backends without one refuse by name.`,
	Example: `  corral clone myvm myvm-clone
  corral clone myvm myvm-clone -n other-namespace`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		src := args[0]
		dst := args[1]

		ref, err := instanceRef(src)
		if err != nil {
			return fmt.Errorf("source VM %q not found", src)
		}
		// -n names the namespace the source lives in, where the backend has
		// namespaces at all; the clone lands beside it.
		if cloneNamespace != "" && backend.Namespaced(ref.Backend) {
			ref.Namespace = cloneNamespace
		}
		adapter, err := backend.For(ref)
		if err != nil {
			return err
		}
		cloner, ok := adapter.(backend.Cloner)
		if !ok {
			return unsupported(adapter, "clone")
		}

		if existing := resolveBackend(dst); existing != "" {
			return fmt.Errorf("target VM %q already exists (backend: %s)", dst, existing)
		}

		fmt.Printf("Cloning VM %q to %q...\n", src, dst)
		if err := cloner.Clone(ref.Name, dst); err != nil {
			return err
		}

		if registryStore != nil {
			if err := registryStore.Set(dst, types.RegistryEntry{
				Backend:   ref.Backend,
				Context:   ref.Context,
				Namespace: ref.Namespace,
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
