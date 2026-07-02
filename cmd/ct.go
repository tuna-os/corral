package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/kubevirt"
)

var (
	ctNamespace    string
	ctImage        string
	ctCPU          int
	ctMem          string
	ctDisk         string
	ctStorageClass string
	ctPrivileged   bool
)

var ctCmd = &cobra.Command{
	Use:   "ct",
	Short: "Manage Containers (CT) — Proxmox-style pet pods, not VMs",
	Long: `Containers (CT) are plain Kubernetes pods presented in the Proxmox CT shape —
console-able and long-lived like a VM, but without a hypervisor underneath.

Unprivileged (default): a PVC mounts at /data only; the rest of the
filesystem resets to the image's baked-in state on every Stop/Start.

Privileged: distrobox-on-Kubernetes — the CT seeds its volume with a full
copy of the image's own root filesystem and chroots into it on boot, so
package installs and dotfiles survive Stop/Start. Needs a real OS image
(debian/ubuntu/fedora), not alpine/busybox.`,
}

var ctCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a Container (CT)",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		if ctImage == "" {
			return fmt.Errorf("--image is required")
		}
		ns := ctNamespace
		if ns == "" {
			ns = kubevirt.DefaultNamespace
		}
		if err := ct.Create(ct.CreateOpts{
			Name: name, Namespace: ns, Image: ctImage,
			CPU: ctCPU, Mem: ctMem, Disk: ctDisk,
			StorageClass: ctStorageClass, Privileged: ctPrivileged,
		}); err != nil {
			return err
		}
		fmt.Printf("CT %q created in ns/%s\n", name, ns)
		return nil
	},
}

var ctListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Containers",
	RunE: func(_ *cobra.Command, _ []string) error {
		cts, err := ct.ListCTs()
		if err != nil {
			return err
		}
		if len(cts) == 0 {
			fmt.Println("No Containers found.")
			return nil
		}
		fmt.Printf("%-20s %-16s %-10s %-6s %-6s %s\n", "NAME", "NAMESPACE", "PHASE", "CPU", "MEM", "PRIVILEGED")
		for _, c := range cts {
			fmt.Printf("%-20s %-16s %-10s %-6d %-6s %v\n", c.Name, c.Namespace, c.Phase, c.CPU, c.Mem, c.Privileged)
		}
		return nil
	},
}

var ctStartCmd = &cobra.Command{
	Use:   "start <name>",
	Short: "Start a stopped Container",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return ct.Start(args[0], ctNamespaceOrDefault())
	},
}

var ctStopCmd = &cobra.Command{
	Use:   "stop <name>",
	Short: "Stop a Container (its data volume is kept)",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return ct.Stop(args[0], ctNamespaceOrDefault())
	},
}

var ctDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a Container and its data volume",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return ct.Delete(args[0], ctNamespaceOrDefault())
	},
}

var ctConsoleCmd = &cobra.Command{
	Use:   "console <name>",
	Short: "Open an interactive console on a Container",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return ct.Console(args[0], ctNamespaceOrDefault())
	},
}

func ctNamespaceOrDefault() string {
	if ctNamespace != "" {
		return ctNamespace
	}
	return kubevirt.DefaultNamespace
}

func init() {
	rootCmd.AddCommand(ctCmd)
	ctCmd.PersistentFlags().StringVarP(&ctNamespace, "namespace", "n", "", "Namespace (default: corral's default)")
	ctCmd.AddCommand(ctCreateCmd, ctListCmd, ctStartCmd, ctStopCmd, ctDeleteCmd, ctConsoleCmd)

	ctCreateCmd.Flags().StringVar(&ctImage, "image", "", "OCI image (required)")
	ctCreateCmd.Flags().IntVar(&ctCPU, "cpu", 1, "vCPU cores")
	ctCreateCmd.Flags().StringVar(&ctMem, "mem", "512Mi", "Memory")
	ctCreateCmd.Flags().StringVar(&ctDisk, "disk", "5Gi", "Data volume size")
	ctCreateCmd.Flags().StringVarP(&ctStorageClass, "storage-class", "s", "", "StorageClass (default: cluster preference)")
	ctCreateCmd.Flags().BoolVar(&ctPrivileged, "privileged", false, "Persistent full rootfs (distrobox-style) — needs a real OS image")
}
