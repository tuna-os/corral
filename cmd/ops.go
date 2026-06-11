package cmd

import (
	"fmt"

	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/qemu"
	"github.com/spf13/cobra"
)

// Advanced KubeVirt operations mirrored from the web UI: restart, pause,
// migrate, scale (CPU/RAM), snapshot, and disk hotplug.

var (
	migrateNode string
	scaleCPU    int
	scaleMem    string
	addDiskSize string
	rmDiskVol   string
)

// kubevirtOnly resolves the VM and errors if it is not a KubeVirt VM.
func kubevirtOnly(args []string, action string) (*kubevirt.Client, string, error) {
	name, err := requireOrPrompt(args, action)
	if err != nil {
		return nil, "", err
	}
	backend, err := requireBackend(name)
	if err != nil {
		return nil, "", err
	}
	if backend != "kubevirt" {
		return nil, "", fmt.Errorf("%s is only supported for KubeVirt VMs", action)
	}
	ns, _ := resolveNamespace(name)
	return kubevirt.NewClient(ns), name, nil
}

var restartCmd = &cobra.Command{
	Use:   "restart [name]",
	Short: "Restart a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "restart")
		if err != nil {
			return err
		}
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			ns, _ := resolveNamespace(name)
			return kubevirt.NewClient(ns).RestartVM(name)
		}
		if err := qemu.Stop(name); err != nil {
			return err
		}
		return qemu.Start(name)
	},
}

var pauseCmd = &cobra.Command{
	Use:   "pause [name]",
	Short: "Pause a running VM (KubeVirt)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "pause")
		if err != nil {
			return err
		}
		return c.PauseVM(name)
	},
}

var unpauseCmd = &cobra.Command{
	Use:   "unpause [name]",
	Short: "Resume a paused VM (KubeVirt)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "unpause")
		if err != nil {
			return err
		}
		return c.UnpauseVM(name)
	},
}

var migrateCmd = &cobra.Command{
	Use:   "migrate [name]",
	Short: "Live-migrate a VM to another node (KubeVirt)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "migrate")
		if err != nil {
			return err
		}
		return c.Migrate(name, migrateNode)
	},
}

var scaleCmd = &cobra.Command{
	Use:   "scale [name]",
	Short: "Change a VM's CPU and/or memory (KubeVirt, live when possible)",
	Long: `Change CPU and/or memory. For live-migratable VMs the change is
hotplugged without downtime; otherwise the VM is restarted to apply.

Examples:
  corral scale web --cpu 4
  corral scale web --mem 8G
  corral scale web --cpu 4 --mem 8G`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "scale")
		if err != nil {
			return err
		}
		if scaleCPU == 0 && scaleMem == "" {
			return fmt.Errorf("specify --cpu and/or --mem")
		}
		if err := c.Scale(name, scaleCPU, scaleMem); err != nil {
			return err
		}
		if scaleCPU > 0 {
			fmt.Printf("CPU → %d\n", scaleCPU)
		}
		if scaleMem != "" {
			fmt.Printf("Memory → %s\n", scaleMem)
		}
		return nil
	},
}

var addDiskCmd = &cobra.Command{
	Use:   "adddisk [name]",
	Short: "Create and hotplug a new disk onto a VM (KubeVirt)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "adddisk")
		if err != nil {
			return err
		}
		pvc, err := c.AddVolume(name, addDiskSize)
		if err != nil {
			return err
		}
		fmt.Printf("Attached disk %s\n", pvc)
		return nil
	},
}

var rmDiskCmd = &cobra.Command{
	Use:   "rmdisk [name]",
	Short: "Detach a hotplugged disk from a VM (KubeVirt)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "rmdisk")
		if err != nil {
			return err
		}
		if rmDiskVol == "" {
			return fmt.Errorf("specify --volume <name>")
		}
		return c.RemoveVolume(name, rmDiskVol)
	},
}

// ── snapshot subcommands ──────────────────────────────────────────

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Manage VM snapshots (KubeVirt)",
}

var snapshotCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Take a snapshot of a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "snapshot")
		if err != nil {
			return err
		}
		snap, err := c.Snapshot(name, "")
		if err != nil {
			return err
		}
		fmt.Println(snap)
		return nil
	},
}

var snapshotListCmd = &cobra.Command{
	Use:   "ls [name]",
	Short: "List snapshots for a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "snapshot")
		if err != nil {
			return err
		}
		snaps, err := c.ListSnapshots(name)
		if err != nil {
			return err
		}
		if len(snaps) == 0 {
			fmt.Println("No snapshots.")
			return nil
		}
		for _, s := range snaps {
			ready := "…"
			if s.Ready {
				ready = "ready"
			}
			fmt.Printf("%-40s  %-6s  %s\n", s.Name, ready, s.Created)
		}
		return nil
	},
}

var snapshotRestoreCmd = &cobra.Command{
	Use:   "restore [name] [snapshot]",
	Short: "Restore a VM from a snapshot (VM must be stopped)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args[:1], "snapshot")
		if err != nil {
			return err
		}
		if len(args) < 2 {
			return fmt.Errorf("specify the snapshot name (corral snapshot ls %s)", name)
		}
		return c.RestoreSnapshot(name, args[1])
	},
}

var snapshotDeleteCmd = &cobra.Command{
	Use:   "rm [name] [snapshot]",
	Short: "Delete a snapshot",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := kubevirtOnly(args[:1], "snapshot")
		if err != nil {
			return err
		}
		if len(args) < 2 {
			return fmt.Errorf("specify the snapshot name")
		}
		return c.DeleteSnapshot(args[1])
	},
}

func init() {
	rootCmd.AddCommand(restartCmd, pauseCmd, unpauseCmd, migrateCmd, scaleCmd, addDiskCmd, rmDiskCmd, snapshotCmd)

	migrateCmd.Flags().StringVar(&migrateNode, "node", "", "Target node (default: scheduler chooses)")
	scaleCmd.Flags().IntVar(&scaleCPU, "cpu", 0, "New vCPU count")
	scaleCmd.Flags().StringVar(&scaleMem, "mem", "", "New memory (e.g. 8G)")
	addDiskCmd.Flags().StringVar(&addDiskSize, "size", "10Gi", "Disk size")
	rmDiskCmd.Flags().StringVar(&rmDiskVol, "volume", "", "Volume (PVC) name to detach")

	snapshotCmd.AddCommand(snapshotCreateCmd, snapshotListCmd, snapshotRestoreCmd, snapshotDeleteCmd)
}
