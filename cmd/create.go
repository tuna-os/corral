package cmd

import (
	"fmt"
	"os"

	"github.com/hanthor/corral/pkg/config"
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
	createPVC               string
	createNamespace         string
	createNode              string
	createCloudInitPassword string
	createCloudInit         string
	createBootc             string
	createTSAuthKey         string
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

Bootc examples:
  corral create myvm --bootc quay.io/centos-bootc/centos-bootc:stream9
  corral create myvm --bootc quay.io/centos-bootc/centos-bootc:stream9 --disk 30G`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if existing := resolveBackend(name); existing != "" && !createForce {
			return fmt.Errorf("VM %q already exists (backend: %s). Use --force to overwrite", name, existing)
		}

		if createBootc != "" {
			return runBootcCreate(name)
		}
		if createKubevirt {
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
	createCmd.Flags().StringVar(&createPVC, "pvc", "", "[kubevirt] Existing PVC to use")
	createCmd.Flags().StringVarP(&createNamespace, "namespace", "n", "tailvm", "[kubevirt] Namespace")
	createCmd.Flags().StringVar(&createNode, "node", "", "[kubevirt] Schedule on specific node")
	createCmd.Flags().StringVar(&createCloudInitPassword, "cloud-init-password", "", "[kubevirt] Cloud-init password")
	createCmd.Flags().StringVar(&createCloudInit, "cloud-init", "", "[kubevirt] Extra cloud-init user-data YAML")
	createCmd.Flags().StringVar(&createBootc, "bootc", "", "Bootc container image URI (builds disk on-cluster)")
	createCmd.Flags().StringVar(&createTSAuthKey, "ts-authkey", "", "[kubevirt] Tailscale auth key for the VM (default: config/TS_AUTHKEY)")
	createCmd.Flags().StringVar(&createInstanceType, "instancetype", "", "[kubevirt] Cluster instancetype for sizing (overrides --cpu/--mem)")
	createCmd.Flags().StringVar(&createPreference, "preference", "", "[kubevirt] Cluster preference (guest device/firmware defaults)")
}

// tsAuthKey resolves the Tailscale auth key: flag > env/config file.
func tsAuthKey() string {
	if createTSAuthKey != "" {
		return createTSAuthKey
	}
	return config.AuthKey()
}

func runKubevirtCreate(name string) error {
	ns := createNamespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}

	opts := types.CreateOpts{
		Name:              name,
		Namespace:         ns,
		Mem:               createMem,
		CPU:               createCPU,
		Disk:              createDisk,
		ISO:               createISO,
		ContainerDisk:     createContainerDisk,
		PVC:               createPVC,
		Node:              createNode,
		CloudInitPassword: createCloudInitPassword,
		CloudInitExtra:    createCloudInit,
		InstanceType:      createInstanceType,
		Preference:        createPreference,
		SSHPublicKey:      kubevirt.LoadSSHPublicKey(),
		TailscaleAuthKey:  tsAuthKey(),
	}
	if opts.TailscaleAuthKey != "" {
		fmt.Fprintln(os.Stderr, "Tailscale auth key found — VM will join the tailnet on first boot")
	}
	if err := kubevirt.CreateVM(opts); err != nil {
		return err
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

func runBootcCreate(name string) error {
	ns := createNamespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	kubevirt.EnsureNamespace(ns)

	// Load SSH public key from localhost
	sshKey := kubevirt.LoadSSHPublicKey()
	if sshKey == "" {
		return fmt.Errorf("no SSH public key found in ~/.ssh/ — needed for bootc VM access")
	}

	diskSize := createDisk
	if diskSize == "" {
		diskSize = "50Gi" // bootc GNOME images need more space
	}

	// Build the bootc disk on-cluster
	build, err := kubevirt.BootcBuildDisk(name, ns, createBootc, sshKey, diskSize, os.Stderr)
	if err != nil {
		return fmt.Errorf("bootc build: %w", err)
	}

	// Create the VM using the built PVC with kernel boot
	vm := kubevirt.GenerateBootcVM(name, ns, build.PVCName, createBootc,
		build.RootUUID, build.KernelVersion, createMem, createCPU, createNode)
	if err := kubevirt.Apply(vm); err != nil {
		return fmt.Errorf("creating VM: %w", err)
	}

	if registryStore != nil {
		if err := registryStore.Set(name, types.RegistryEntry{
			Backend:   "kubevirt",
			Namespace: ns,
			Extra:     map[string]string{"bootc_image": createBootc},
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
