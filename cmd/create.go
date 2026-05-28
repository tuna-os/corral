package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/hanthor/tailvm-go/pkg/kubevirt"
	"github.com/hanthor/tailvm-go/pkg/qemu"
	"github.com/hanthor/tailvm-go/pkg/types"
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
)

var createCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a new VM",
	Long: `Create a new virtual machine.

By default, creates a local QEMU/KVM VM. Use --kubevirt for a
Kubernetes KubeVirt VM. The backend choice is persisted so
subsequent commands (start, stop, viewer...) auto-detect it.

QEMU examples:
  tailvm create myvm --iso https://example.com/ubuntu.iso
  tailvm create myvm --qcow ./template.qcow2 --disk 40G

KubeVirt examples:
  tailvm create myvm --kubevirt --iso https://example.com/bluefin.iso
  tailvm create myvm --kubevirt --container-disk quay.io/containerdisks/ubuntu:24.04`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		if existing := resolveBackend(name); existing != "" && !createForce {
			return fmt.Errorf("VM %q already exists (backend: %s). Use --force to overwrite", name, existing)
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
}

func runKubevirtCreate(name string) error {
	ns := createNamespace
	if ns == "" {
		ns = kubevirt.DefaultNamespace
	}
	kubevirt.EnsureNamespace()

	hasISO := createISO != ""
	hasContainer := createContainerDisk != ""
	hasPVC := createPVC != ""

	if hasISO {
		dv := kubevirt.GenerateDataVolume(name+"-iso", ns, createISO)
		if err := applyResource(dv); err != nil {
			return fmt.Errorf("creating ISO DataVolume: %w", err)
		}
		fmt.Fprintf(os.Stderr, "ISO DataVolume: %s-iso (importing from %s)\n", name, createISO)

		diskSize := createDisk
		if diskSize == "" {
			diskSize = "20G"
		}
		pvc := kubevirt.GeneratePVC(name+"-disk", ns, diskSize)
		if err := applyResource(pvc); err != nil {
			return fmt.Errorf("creating boot PVC: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Boot PVC: %s-disk (%s)\n", name, diskSize)
	} else if hasContainer && createDisk != "" {
		pvc := kubevirt.GeneratePVC(name+"-data", ns, createDisk)
		if err := applyResource(pvc); err != nil {
			return fmt.Errorf("creating data PVC: %w", err)
		}
	} else if !hasPVC && !hasContainer {
		diskSize := createDisk
		if diskSize == "" {
			diskSize = "20G"
		}
		pvc := kubevirt.GeneratePVC(name+"-disk", ns, diskSize)
		if err := applyResource(pvc); err != nil {
			return fmt.Errorf("creating boot PVC: %w", err)
		}
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
	}
	vm := kubevirt.GenerateVM(opts)
	if err := applyResource(vm); err != nil {
		return fmt.Errorf("creating VM: %w", err)
	}

	if registryStore != nil {
		if err := registryStore.Set(name, types.RegistryEntry{
			Backend:   "kubevirt",
			Namespace: ns,
		}); err != nil {
			return fmt.Errorf("saving registry: %w", err)
		}
	}

	fmt.Fprintf(os.Stderr, "VM %q created in ns/%s\n", name, ns)
	fmt.Fprintf(os.Stderr, "  Start:  tailvm start %s\n", name)
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

func applyResource(obj map[string]any) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(string(data))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
