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

	"github.com/hanthor/corral/pkg/catalog"
	"github.com/hanthor/corral/pkg/config"
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
				return fmt.Errorf("--image is required — a catalog name (see `corral bootc images`) or an OCI ref like quay.io/centos-bootc/centos-bootc:stream9")
			}
			image = catalog.ResolveBootc(image)
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
				build.RootUUID, build.KernelVersion, mem, cpu, node, config.AuthKey())
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
	create.Flags().String("image", "", "Bootc container image — catalog name or OCI ref")
	create.Flags().StringVar(&disk, "disk", "50Gi", "Disk size")
	create.Flags().StringVar(&mem, "mem", "4G", "Memory")
	create.Flags().IntVar(&cpu, "cpu", 2, "vCPUs")
	create.Flags().StringVar(&node, "node", "", "Schedule on a specific node")
	create.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: ~/.ssh/*.pub)")

	images := &cobra.Command{
		Use:   "images",
		Short: "List the curated bootc image catalog",
		Long: `Well-maintained bootable container bases from Fedora, CentOS, and
Universal Blue. Use a catalog name directly:

  corral bootc create myvm --image fedora-bootc`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("%-22s %-46s %s\n", "NAME", "IMAGE", "DESCRIPTION")
			for _, b := range catalog.BootcImages {
				fmt.Printf("%-22s %-46s %s\n", b.Name, b.Image, b.Description)
			}
			return nil
		},
	}

	// recordedImage returns the bootc image a VM was built from (registry).
	recordedImage := func(name string) string {
		store, err := registry.NewStore()
		if err != nil {
			return ""
		}
		if e, ok := store.Get(name); ok {
			return e.Extra["bootc_image"]
		}
		return ""
	}
	// rebuild reruns the on-cluster build and re-points the VM. newImage "" =
	// keep the recorded image (an upgrade — pull the latest of the same tag).
	rebuild := func(name, newImage string) error {
		ns := namespace
		if ns == "" {
			ns = kubevirt.DefaultNamespace
		}
		if !kubevirt.BootcAvailable() {
			return fmt.Errorf("this corral-bootc build lacks the bootc pipeline (build with -tags bootc)")
		}
		image := catalog.ResolveBootc(newImage)
		if image == "" {
			image = recordedImage(name)
		}
		if image == "" {
			return fmt.Errorf("no image recorded for %q — pass --image <ref>", name)
		}
		key := sshKey
		if key == "" {
			key = kubevirt.LoadSSHPublicKey()
		}
		if err := kubevirt.BootcRebuild(name, ns, image, key, disk, os.Stderr); err != nil {
			return err
		}
		if store, err := registry.NewStore(); err == nil {
			store.Set(name, types.RegistryEntry{
				Backend: "kubevirt", Namespace: ns,
				Extra: map[string]string{"bootc_image": image},
			})
		}
		fmt.Fprintf(os.Stderr, "VM %q rebuilt from %s and restarted\n", name, image)
		return nil
	}

	rebuildCmd := &cobra.Command{
		Use:   "rebuild <name>",
		Short: "Rebuild a bootc VM's disk from its image and restart it",
		Long: `Rebuilds the on-cluster disk from the VM's recorded bootc image (or
--image to override), swaps it in, and restarts the VM. Sizing/networking are
preserved. Use this to apply image updates.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			img, _ := cmd.Flags().GetString("image")
			return rebuild(args[0], img)
		},
	}
	rebuildCmd.Flags().String("image", "", "Override the image (default: the recorded one)")
	rebuildCmd.Flags().StringVar(&disk, "disk", "", "Override disk size (default: keep existing)")
	rebuildCmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: ~/.ssh/*.pub)")

	upgradeCmd := &cobra.Command{
		Use:   "upgrade <name>",
		Short: "Rebuild a bootc VM from the latest of its current image (alias for rebuild)",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return rebuild(args[0], "") },
	}
	upgradeCmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: ~/.ssh/*.pub)")

	switchCmd := &cobra.Command{
		Use:   "switch <name> --image <ref>",
		Short: "Rebuild a bootc VM onto a different image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			img, _ := cmd.Flags().GetString("image")
			if img == "" {
				return fmt.Errorf("--image is required (catalog name or OCI ref)")
			}
			return rebuild(args[0], img)
		},
	}
	switchCmd.Flags().String("image", "", "New bootc image — catalog name or OCI ref")
	switchCmd.Flags().StringVar(&disk, "disk", "", "Override disk size (default: keep existing)")
	switchCmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH public key (default: ~/.ssh/*.pub)")

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "List bootc VMs and the image each was built from",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := registry.NewStore()
			if err != nil {
				return err
			}
			fmt.Printf("%-24s %-12s %s\n", "VM", "NAMESPACE", "BOOTC IMAGE")
			for name, e := range store.All() {
				if img := e.Extra["bootc_image"]; img != "" {
					ns := e.Namespace
					if ns == "" {
						ns = kubevirt.DefaultNamespace
					}
					fmt.Printf("%-24s %-12s %s\n", name, ns, img)
				}
			}
			return nil
		},
	}

	root := &cobra.Command{
		Use:   "corral-bootc",
		Short: "Corral bootc plugin — boot a container image as a VM",
	}
	root.PersistentFlags().StringVarP(&namespace, "namespace", "n", kubevirt.DefaultNamespace, "Namespace")
	root.AddCommand(create, images, rebuildCmd, upgradeCmd, switchCmd, statusCmd)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
