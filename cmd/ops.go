package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/qemu"
)

// Advanced KubeVirt operations mirrored from the web UI: restart, pause,
// migrate, scale (CPU/RAM), snapshot, and disk hotplug.

var (
	migrateNode      string
	scaleCPU         int
	scaleMem         string
	addDiskSize      string
	rmDiskVol        string
	exportVolume     string
	exportOutput     string
	screenshotOutput string
	addNicNAD        string
	addNicIface      string
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

// qemuOnly resolves the VM and errors if it is not a QEMU (local) VM.
func qemuOnly(args []string, action string) (string, error) {
	name, err := requireOrPrompt(args, action)
	if err != nil {
		return "", err
	}
	backend, err := requireBackend(name)
	if err != nil {
		return "", err
	}
	if backend != "qemu" {
		return "", fmt.Errorf("%s is only supported for QEMU VMs", action)
	}
	return name, nil
}

var screenshotCmd = &cobra.Command{
	Use:   "screenshot [name]",
	Short: "Capture a screenshot of a QEMU VM's framebuffer",
	Long: `Capture the current framebuffer of a running QEMU VM over its QMP
monitor socket and save it as a PNG — no VNC client needed. Useful as boot
evidence in CI (a black/blank screen is easy to catch programmatically:
compare pixel variance against a threshold).

KubeVirt VMs aren't supported here — use ` + "`corral viewer`" + ` for VNC instead.`,
	Example: `  corral screenshot myvm
  corral screenshot myvm -o boot-evidence.png`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := qemuOnly(args, "screenshot")
		if err != nil {
			return err
		}
		out := screenshotOutput
		if out == "" {
			out = name + "-screenshot.png"
		}
		if err := qemu.Screenshot(name, out); err != nil {
			return err
		}
		fmt.Printf("Screenshot written to %s\n", out)
		return nil
	},
}

var restartCmd = &cobra.Command{
	Use:     "restart [name]",
	Short:   "Restart a VM",
	Example: `  corral restart myvm`,
	Args:    cobra.MaximumNArgs(1),
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
	Use:     "pause [name]",
	Short:   "Pause a running VM (KubeVirt)",
	Example: `  corral pause myvm`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "pause")
		if err != nil {
			return err
		}
		return c.PauseVM(name)
	},
}

var unpauseCmd = &cobra.Command{
	Use:     "unpause [name]",
	Short:   "Resume a paused VM (KubeVirt)",
	Example: `  corral unpause myvm`,
	Args:    cobra.MaximumNArgs(1),
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
	Example: `  corral migrate myvm                # let the scheduler choose
  corral migrate myvm --node karnataka`,
	Args: cobra.MaximumNArgs(1),
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
hotplugged without downtime; otherwise the VM is restarted to apply.`,
	Example: `  corral scale web --cpu 4
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
	Use:     "adddisk [name]",
	Short:   "Create and hotplug a new disk onto a VM (KubeVirt)",
	Example: `  corral adddisk myvm --size 20Gi`,
	Args:    cobra.MaximumNArgs(1),
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
	Use:     "rmdisk [name]",
	Short:   "Detach a hotplugged disk from a VM (KubeVirt)",
	Example: `  corral rmdisk myvm --volume myvm-data-a1b2c3`,
	Args:    cobra.MaximumNArgs(1),
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

var networksCmd = &cobra.Command{
	Use:     "networks",
	Short:   "List Multus NetworkAttachmentDefinitions available for --lan/--network-nad (KubeVirt)",
	Example: `  corral networks`,
	RunE: func(cmd *cobra.Command, args []string) error {
		nads := kubevirt.ListNADs()
		if len(nads) == 0 {
			fmt.Println("No NetworkAttachmentDefinitions found — Multus may not be installed on this cluster.")
			return nil
		}
		for _, n := range nads {
			fmt.Println(n)
		}
		return nil
	},
}

var addNicCmd = &cobra.Command{
	Use:   "addnic [name]",
	Short: "Bridge a secondary NIC onto the LAN for an existing VM (KubeVirt)",
	Long: `Attach a bridge-bound secondary NIC backed by a Multus NetworkAttachmentDefinition
— same mechanism as ` + "`corral create --lan`" + `, for a VM that already exists.
Hotplugs on a running VM where KubeVirt supports it, otherwise it takes
effect on the next boot.`,
	Example: `  corral addnic myvm
  corral addnic myvm --network-nad default/lan-bridge --iface net1`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "addnic")
		if err != nil {
			return err
		}
		nad, err := kubevirt.ResolveNAD(addNicNAD, kubevirt.ListNADs())
		if err != nil {
			return err
		}
		if err := c.AddNIC(name, nad, addNicIface); err != nil {
			return err
		}
		iface := addNicIface
		if iface == "" {
			iface = "net1"
		}
		fmt.Printf("Bridged %s onto %s (guest iface %s)\n", name, nad, iface)
		return nil
	},
}

var exportCmd = &cobra.Command{
	Use:   "export [name]",
	Short: "Back up a VM's disk to a compressed image (KubeVirt)",
	Long: `Export (back up) a VM's persistent disk to a gzip image via the
KubeVirt export API. The VM should be stopped first — its disk can't be read
while a running VM holds it.`,
	Example: `  corral export web                       # → web.img.gz
  corral export web -o /backups/web.gz
  corral export web --volume web-disk`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "export")
		if err != nil {
			return err
		}
		out, err := c.Export(name, exportVolume, exportOutput)
		if err != nil {
			return err
		}
		fmt.Printf("Exported %s → %s\n", name, out)
		return nil
	},
}

// ── template subcommands ──────────────────────────────────────────

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage golden VM templates (KubeVirt)",
}

var templateMarkCmd = &cobra.Command{
	Use:   "mark [name]",
	Short: "Mark a VM as a template",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "template")
		if err != nil {
			return err
		}
		return c.MarkTemplate(name, true)
	},
}

var templateUnmarkCmd = &cobra.Command{
	Use:   "unmark [name]",
	Short: "Remove the template mark from a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, name, err := kubevirtOnly(args, "template")
		if err != nil {
			return err
		}
		return c.MarkTemplate(name, false)
	},
}

var templateListCmd = &cobra.Command{
	Use:   "ls",
	Short: "List template VMs",
	RunE: func(cmd *cobra.Command, args []string) error {
		vms, err := kubevirt.NewClient("").ListVMs()
		if err != nil {
			return err
		}
		n := 0
		for _, vm := range vms {
			if vm.IsTemplate {
				fmt.Printf("%-30s  %s  %d CPU / %s\n", vm.Name, vm.Namespace, vm.CPU, vm.Mem)
				n++
			}
		}
		if n == 0 {
			fmt.Println("No templates. Mark one: corral template mark <vm>")
		}
		return nil
	},
}

var templateNewCmd = &cobra.Command{
	Use:     "new [template] [newname]",
	Short:   "Create a new VM from a template (clone)",
	Example: `  corral template new tmpl-ubuntu dev1`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, tmpl, err := kubevirtOnly(args[:1], "template")
		if err != nil {
			return err
		}
		if err := c.CreateFromTemplate(tmpl, args[1]); err != nil {
			return err
		}
		fmt.Printf("Creating %s from template %s…\n", args[1], tmpl)
		return nil
	},
}

// ── snapshot subcommands ──────────────────────────────────────────

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Manage VM snapshots (KubeVirt)",
	Example: `  corral snapshot create myvm
  corral snapshot ls myvm
  corral snapshot restore myvm myvm-20260704-1200
  corral snapshot rm myvm myvm-20260704-1200`,
}

var snapshotCreateCmd = &cobra.Command{
	Use:     "create [name]",
	Short:   "Take a snapshot of a VM",
	Example: `  corral snapshot create myvm`,
	Args:    cobra.MaximumNArgs(1),
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
	rootCmd.AddCommand(restartCmd, pauseCmd, unpauseCmd, migrateCmd, scaleCmd, addDiskCmd, rmDiskCmd, exportCmd, snapshotCmd, templateCmd, screenshotCmd, networksCmd, addNicCmd)
	templateCmd.AddCommand(templateMarkCmd, templateUnmarkCmd, templateListCmd, templateNewCmd)

	migrateCmd.Flags().StringVar(&migrateNode, "node", "", "Target node (default: scheduler chooses)")
	scaleCmd.Flags().IntVar(&scaleCPU, "cpu", 0, "New vCPU count")
	scaleCmd.Flags().StringVar(&scaleMem, "mem", "", "New memory (e.g. 8G)")
	addDiskCmd.Flags().StringVar(&addDiskSize, "size", "10Gi", "Disk size")
	rmDiskCmd.Flags().StringVar(&rmDiskVol, "volume", "", "Volume (PVC) name to detach")
	exportCmd.Flags().StringVar(&exportVolume, "volume", "", "Volume/PVC to export (default: primary disk)")
	exportCmd.Flags().StringVarP(&exportOutput, "output", "o", "", "Output file (default: <name>.img.gz)")
	screenshotCmd.Flags().StringVarP(&screenshotOutput, "output", "o", "", "Output PNG path (default: <name>-screenshot.png)")
	addNicCmd.Flags().StringVar(&addNicNAD, "network-nad", "", "NetworkAttachmentDefinition to bridge onto (\"ns/name\"); default: the cluster's only one")
	addNicCmd.Flags().StringVar(&addNicIface, "iface", "", "Guest interface name for the new NIC (default: net1)")

	snapshotCmd.AddCommand(snapshotCreateCmd, snapshotListCmd, snapshotRestoreCmd, snapshotDeleteCmd)
}
