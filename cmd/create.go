package cmd

import (
	"fmt"
	"os"

	"github.com/hanthor/corral/pkg/catalog"
	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/qemu"
	"github.com/hanthor/corral/pkg/types"
	"github.com/spf13/cobra"
)

var (
	createKubevirt          bool
	createMem               string
	createCPU               int
	createDisk              string
	createISO               string
	createQCOW              string
	createForce             bool
	createContainerDisk     string
	createImage             string
	createImport            string
	createPVC               string
	createNamespace         string
	createNode              string
	createCloudInitPassword string
	createCloudInit         string
	createInstanceType      string
	createPreference        string
)

var createCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new VM",
	Long: `Create a new virtual machine.

By default, creates a local QEMU/KVM VM. Use --kubevirt for a
Kubernetes KubeVirt VM. The backend choice is persisted so
subsequent commands (start, stop, viewer...) auto-detect it.

QEMU examples:
  corral create myvm --iso https://example.com/ubuntu.iso
  corral create myvm --qcow ./template.qcow2 --disk 40G

KubeVirt examples:
  corral create myvm --kubevirt --iso https://example.com/bluefin.iso
  corral create myvm --kubevirt --container-disk quay.io/containerdisks/ubuntu:24.04

Boot a container image as a VM? Install the bootc extension:
  corral plugin install bootc && corral bootc create myvm --image quay.io/centos-bootc/centos-bootc:stream9`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if existing := resolveBackend(name); existing != "" && !createForce {
			return fmt.Errorf("VM %q already exists (backend: %s). Use --force to overwrite", name, existing)
		}

		if createKubevirt || createImage != "" || createImport != "" {
			return runKubevirtCreate(name)
		}
		return runQemuCreate(name)
	},
}

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.Flags().BoolVarP(&createKubevirt, "kubevirt", "k", false, "Use KubeVirt backend")
	createCmd.Flags().StringVar(&createMem, "mem", "4G", "Memory allocation")
	createCmd.Flags().IntVar(&createCPU, "cpu", 2, "CPU cores")
	createCmd.Flags().StringVar(&createDisk, "disk", "", "Disk size (default: 20G)")
	createCmd.Flags().StringVar(&createISO, "iso", "", "ISO path/URL (QEMU: local file, KubeVirt: CDI imports from URL)")
	createCmd.Flags().StringVar(&createQCOW, "qcow", "", "[qemu] QCOW2 template")
	createCmd.Flags().BoolVar(&createForce, "force", false, "Overwrite existing VM")
	createCmd.Flags().StringVar(&createContainerDisk, "container-disk", "", "[kubevirt] Container disk image")
	createCmd.Flags().StringVar(&createImage, "image", "", "[kubevirt] OS image from the catalog (see `corral images`)")
	createCmd.Flags().StringVar(&createImport, "import", "", "[kubevirt] Import a qcow2/raw disk image URL via CDI")
	createCmd.Flags().StringVar(&createPVC, "pvc", "", "[kubevirt] Existing PVC to use")
	createCmd.Flags().StringVarP(&createNamespace, "namespace", "n", kubevirt.DefaultNamespace, "[kubevirt] Namespace")
	createCmd.Flags().StringVar(&createNode, "node", "", "[kubevirt] Schedule on specific node")
	createCmd.Flags().StringVar(&createCloudInitPassword, "cloud-init-password", "", "[kubevirt] Cloud-init password")
	createCmd.Flags().StringVar(&createCloudInit, "cloud-init", "", "[kubevirt] Extra cloud-init user-data YAML")
	createCmd.Flags().StringVar(&createInstanceType, "instancetype", "", "[kubevirt] Cluster instancetype for sizing (overrides --cpu/--mem)")
	createCmd.Flags().StringVar(&createPreference, "preference", "", "[kubevirt] Cluster preference (guest device/firmware defaults)")
}

func runKubevirtCreate(name string) error {
	ns := createNamespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}

	containerDisk := createContainerDisk
	importURL := createImport
	iso := createISO
	if createImage != "" {
		img := catalog.Find(createImage)
		if img == nil {
			return fmt.Errorf("unknown image %q — see `corral images`", createImage)
		}
		// Catalog entries boot three ways: containerdisks directly, official
		// cloud images via CDI import, installer ISOs via the ISO path.
		switch img.Kind() {
		case "containerDisk":
			containerDisk = img.ContainerDisk
		case "import":
			importURL = img.URL
		case "iso":
			iso = img.ISO
		}
	}

	opts := types.CreateOpts{
		Name:              name,
		Namespace:         ns,
		Mem:               createMem,
		CPU:               createCPU,
		Disk:              createDisk,
		ISO:               iso,
		ContainerDisk:     containerDisk,
		ImportURL:         importURL,
		PVC:               createPVC,
		Node:              createNode,
		CloudInitPassword: createCloudInitPassword,
		CloudInitExtra:    createCloudInit,
		InstanceType:      createInstanceType,
		Preference:        createPreference,
		SSHPublicKey:      kubevirt.LoadSSHPublicKey(),
	}
	if err := kubevirt.CreateVM(opts); err != nil {
		return err
	}
	// Expose SSH/VNC on the tailnet via the Tailscale operator proxy (no
	// in-guest tailscale needed). Best-effort — the VM is already created.
	if err := kubevirt.ApplyProxy(name, ns, []int{22, 5900}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tailnet expose failed: %v\n", err)
	}

	if registryStore != nil {
		if err := registryStore.Set(name, types.RegistryEntry{
			Backend:   "kubevirt",
			Namespace: ns,
			Password:  kubevirt.LastPassword,
		}); err != nil {
			return fmt.Errorf("saving registry: %w", err)
		}
	}

	fmt.Fprintf(os.Stderr, "VM %q created in ns/%s\n", name, ns)
	fmt.Fprintf(os.Stderr, "  Start:  corral start %s\n", name)
	fmt.Fprintf(os.Stderr, "  SSH:    corral ssh %s\n", name)
	return nil
}

func runQemuCreate(name string) error {
	if err := qemu.Create(types.CreateOpts{
		Name:  name,
		Mem:   createMem,
		CPU:   createCPU,
		Disk:  createDisk,
		ISO:   createISO,
		QCOW:  createQCOW,
		Force: createForce,
	}); err != nil {
		return err
	}
	if registryStore != nil {
		registryStore.Set(name, types.RegistryEntry{Backend: "qemu"})
	}
	return nil
}
