// corral-bootc is the bootc Corral plugin: it builds a bootable container
// image into a VM disk on-cluster and boots it. Installed via the Corral
// marketplace (`corral plugin install bootc`) and invoked as `corral bootc`.
//
// It's a separate binary built with `-tags bootc`, so the bootc pipeline in
// pkg/kubevirt (//go:build bootc) is registered here but stays out of the lean
// core binary.
package main

import (
	"fmt"
	"os"

	"github.com/hanthor/corral/pkg/kubevirt"
	"github.com/hanthor/corral/pkg/registry"
	"github.com/hanthor/corral/pkg/types"
	"github.com/spf13/cobra"
)

func main() {
	var (
		namespace, disk, mem, sshKey, node string
		cpu                                int
	)

	create := &cobra.Command{
		Use:   "create <name> --image <bootc-image>",
		Short: "Build a bootc container image into a VM and create it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			image, _ := cmd.Flags().GetString("image")
			if image == "" {
				return fmt.Errorf("--image is required (e.g. quay.io/centos-bootc/centos-bootc:stream9)")
			}
			name := args[0]
			ns := namespace
			if ns == "" {
				ns = kubevirt.DefaultNamespace
			}
			if !kubevirt.BootcAvailable() {
				return fmt.Errorf("this corral-bootc build lacks the bootc pipeline (build with -tags bootc)")
			}
			kubevirt.EnsureNamespace(ns)

			key := sshKey
			if key == "" {
				key = kubevirt.LoadSSHPublicKey()
			}
			if key == "" {
				return fmt.Errorf("no SSH public key (--ssh-key or ~/.ssh/*.pub) — needed for bootc VM access")
			}
			size := disk
			if size == "" {
				size = "50Gi"
			}

			build, err := kubevirt.BootcBuildDisk(name, ns, image, key, size, os.Stderr)
			if err != nil {
				return fmt.Errorf("bootc build: %w", err)
			}
			vm := kubevirt.GenerateBootcVM(name, ns, build.PVCName, image,
				build.RootUUID, build.KernelVersion, mem, cpu, node)
			if vm == nil {
				return fmt.Errorf("bootc VM manifest unavailable")
			}
			if err := kubevirt.Apply(vm); err != nil {
				return fmt.Errorf("creating VM: %w", err)
			}
			if store, err := registry.NewStore(); err == nil {
				store.Set(name, types.RegistryEntry{
					Backend:   "kubevirt",
					Namespace: ns,
					Extra:     map[string]string{"bootc_image": image},
				})
			}
			fmt.Fprintf(os.Stderr, "VM %q created in ns/%s — start: corral start %s\n", name, ns, name)
			return nil
		},
	}
	create.Flags().String("image", "", "Bootc container image URI")
	create.Flags().StringVarP(&namespace, "namespace", "n", "tailvm", "Namespace")
	create.Flags().StringVar(&disk, "disk", "50Gi", "Disk size")
	create.Flags().StringVar(&mem, "mem", "4G", "Memory")
	create.Flags().IntVar(&cpu, "cpu", 2, "vCPUs")
	create.Flags().StringVar(&node, "node", "", "Schedule on a specific node")
	create.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: ~/.ssh/*.pub)")

	root := &cobra.Command{
		Use:   "corral-bootc",
		Short: "Corral bootc plugin — boot a container image as a VM",
	}
	root.AddCommand(create)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
