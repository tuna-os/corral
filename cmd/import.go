package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/types"
	"github.com/spf13/cobra"
)

// corral import — bring an existing disk image into KubeVirt and boot it as a
// VM (the migration path from Proxmox/ESXi/libvirt). URLs import via a CDI
// DataVolume; local files upload through the CDI upload proxy with
// `virtctl image-upload`.

var (
	importSource    string
	importNamespace string
	importDisk      string
	importMem       string
	importCPU       int
)

var importRunner shell.Runner = shell.Real{}

var importCmd = &cobra.Command{
	Use:   "import <name> --source <url-or-file>",
	Short: "Import a qcow2/raw disk image as a new VM",
	Long: `Import an existing disk image into KubeVirt and wrap a VM around it.

  corral import legacy --source https://server/disk.qcow2   # CDI HTTP import
  corral import legacy --source ./disk.qcow2                # upload local file

qcow2 and raw images work as-is. Convert other formats first:

  qemu-img convert -O qcow2 disk.vmdk disk.qcow2             # VMDK
  tar xf appliance.ova && qemu-img convert -O qcow2 *.vmdk disk.qcow2   # OVA`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if importSource == "" {
			return fmt.Errorf("--source is required (URL or local qcow2/raw file)")
		}
		return runImport(args[0], importNamespace, importSource, importDisk, importMem, importCPU)
	},
}

func runImport(name, ns, source, disk, mem string, cpu int) error {
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	if err := checkImportFormat(source); err != nil {
		return err
	}

	opts := types.CreateOpts{
		Name:         name,
		Namespace:    ns,
		Mem:          mem,
		CPU:          cpu,
		Disk:         disk,
		SSHPublicKey: kubevirt.LoadSSHPublicKey(),
	}

	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		opts.ImportURL = source
	} else {
		// Local file → CDI upload proxy. image-upload creates the DataVolume
		// and PVC, so the VM just references the PVC.
		if _, err := os.Stat(source); err != nil {
			return fmt.Errorf("source %q is neither a URL nor a readable file: %w", source, err)
		}
		pvc := name + "-disk"
		fmt.Fprintf(os.Stderr, "uploading %s → %s/%s …\n", source, ns, pvc)
		out, err := importRunner.Run("virtctl", "image-upload", "dv", pvc,
			"-n", ns, "--size", disk, "--image-path", source,
			"--insecure", "--force-bind")
		if err != nil {
			return fmt.Errorf("virtctl image-upload: %s", strings.TrimSpace(string(out)))
		}
		opts.PVC = pvc
	}

	if err := kubevirt.CreateVM(opts); err != nil {
		return err
	}
	if registryStore != nil {
		registryStore.Set(name, types.RegistryEntry{Backend: "kubevirt", Namespace: ns})
	}
	fmt.Fprintf(os.Stderr, "VM %q created in ns/%s — start: corral start %s\n", name, ns, name)
	return nil
}

// checkImportFormat rejects formats CDI cannot consume directly, with a
// conversion hint.
func checkImportFormat(source string) error {
	s := strings.ToLower(source)
	s = strings.TrimSuffix(s, ".gz")
	s = strings.TrimSuffix(s, ".xz")
	switch {
	case strings.HasSuffix(s, ".vmdk"):
		return fmt.Errorf("VMDK is not directly importable — convert first: qemu-img convert -O qcow2 %s disk.qcow2", source)
	case strings.HasSuffix(s, ".ova"), strings.HasSuffix(s, ".ovf"):
		return fmt.Errorf("OVA/OVF is an archive — unpack it and convert the disk: tar xf %s && qemu-img convert -O qcow2 <disk>.vmdk disk.qcow2", source)
	case strings.HasSuffix(s, ".vhd"), strings.HasSuffix(s, ".vhdx"):
		return fmt.Errorf("VHD(X) is not directly importable — convert first: qemu-img convert -O qcow2 %s disk.qcow2", source)
	}
	return nil
}

func init() {
	importCmd.Flags().StringVar(&importSource, "source", "", "Disk image URL or local qcow2/raw file")
	importCmd.Flags().StringVarP(&importNamespace, "namespace", "n", "", "Namespace (default corral)")
	importCmd.Flags().StringVar(&importDisk, "disk", "20G", "Boot disk size")
	importCmd.Flags().StringVar(&importMem, "mem", "4G", "Memory")
	importCmd.Flags().IntVar(&importCPU, "cpu", 2, "vCPUs")
	rootCmd.AddCommand(importCmd)
}
