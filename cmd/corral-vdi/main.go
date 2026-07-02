// corral-vdi is the VDI Corral plugin: Phase 1 of RFC-0001
// (docs/rfc/0001-vdi-plugin.md) — static desktop pools with manual
// assignment, built by cloning an already-built "golden" VM N times
// (kubevirt.Client.Clone) and tracking assignment as plain labels on the
// VM object, not a new CRD. Installed via the marketplace
// (`corral plugin install vdi`) and invoked as `corral vdi`.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/registry"
	"github.com/tuna-os/corral/pkg/types"
	"github.com/tuna-os/corral/pkg/vdi"
)

var namespace string

func nsOrDefault() string {
	if namespace != "" {
		return namespace
	}
	return kubevirt.DefaultNamespace
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "vdi",
		Short: "Desktop pools — Phase 1 VDI: static pools, manual assignment",
		Long: `Desktop pools built from an already-built "golden" VM (made the normal way,
via corral create / corral bootc / corral-windows). Pool members are clones
of that VM; assignment is a label on the VM object, not a new CRD — see
docs/rfc/0001-vdi-plugin.md for the design and what's still ahead of this
first slice (self-serve claim, idle reclaim, GPU pools).`,
	}
	root.PersistentFlags().StringVarP(&namespace, "namespace", "n", "", "Namespace (default: corral's default)")
	root.AddCommand(poolCmd(), assignCmd(), unassignCmd(), connectCmd())
	return root
}

func poolCmd() *cobra.Command {
	pool := &cobra.Command{
		Use:   "pool",
		Short: "Manage desktop pools",
	}
	pool.AddCommand(poolCreateCmd(), poolListCmd(), poolDeleteCmd())
	return pool
}

func poolCreateCmd() *cobra.Command {
	var from string
	var size int
	c := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a pool by cloning an existing VM <size> times",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			ns := nsOrDefault()
			fmt.Fprintf(os.Stderr, "cloning %q → %d pool members in ns/%s...\n", from, size, ns)
			p, err := vdi.CreatePool(vdi.CreateOpts{Name: name, Namespace: ns, From: from, Size: size})
			if err != nil {
				return err
			}
			if store, rerr := registry.NewStore(); rerr == nil {
				for _, m := range p.Members {
					store.Set(m.Name, types.RegistryEntry{Backend: "kubevirt", Namespace: ns})
				}
			}
			fmt.Printf("pool %q created: %d members\n", name, len(p.Members))
			for _, m := range p.Members {
				fmt.Printf("  %s\n", m.Name)
			}
			return nil
		},
	}
	c.Flags().StringVar(&from, "from", "", "Existing VM to clone as the pool template (required)")
	c.Flags().IntVar(&size, "size", 1, "Number of pool members")
	c.MarkFlagRequired("from")
	return c
}

func poolListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List pools and their members",
		RunE: func(_ *cobra.Command, _ []string) error {
			pools, err := vdi.ListPools()
			if err != nil {
				return err
			}
			if len(pools) == 0 {
				fmt.Println("No pools found.")
				return nil
			}
			for _, p := range pools {
				fmt.Printf("%s  (ns/%s, %d members)\n", p.Name, p.Namespace, len(p.Members))
				for _, m := range p.Members {
					status := "free"
					if m.AssignedTo != "" {
						status = "assigned to " + m.AssignedTo
					}
					running := "stopped"
					if m.Running {
						running = "running"
					}
					fmt.Printf("  %-24s %-24s %s\n", m.Name, status, running)
				}
			}
			return nil
		},
	}
}

func poolDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a pool and all its members",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return vdi.DeletePool(nsOrDefault(), args[0])
		},
	}
}

func assignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "assign <pool> <user>",
		Short: "Claim the first free member of a pool for a user (starts it if stopped)",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			member, err := vdi.Assign(nsOrDefault(), args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("assigned %s → %s\n", member, args[1])
			fmt.Printf("connect:  corral vdi connect %s\n", member)
			return nil
		},
	}
}

func unassignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unassign <member>",
		Short: "Release a member back to its pool's free set and stop it",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return vdi.Unassign(nsOrDefault(), args[0])
		},
	}
}

// connectCmd prints how to reach a member — it reuses the same VNC/RDP/SSH
// paths every other Corral VM already has (virtctl vnc, the RDP port
// probe, corral ssh) rather than inventing a new connection mechanism.
// True one-click routing (picking the right protocol automatically) is
// Phase 2 territory once ADR-0002 phase 2's in-browser RDP lands.
func connectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect <member>",
		Short: "Print how to connect to an assigned desktop",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name, ns := args[0], nsOrDefault()
			fmt.Printf("VNC (browser or client):  corral web  →  open %s  →  Console\n", name)
			fmt.Printf("RDP (if the guest answers on 3389):  corral viewer %s  (or a native RDP client via virtctl port-forward)\n", name)
			fmt.Printf("SSH (Linux guests):  corral ssh %s\n", name)
			fmt.Printf("(namespace: %s)\n", ns)
			return nil
		},
	}
}

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
