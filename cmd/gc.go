package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/kubevirt"
)

var (
	gcDryRun      bool
	gcDeleteAfter string
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Garbage-collect ephemeral VMs (KubeVirt)",
	Long: `Two-stage garbage collection for VMs created with --ephemeral --ttl,
plus orphaned-disk cleanup:

  1. Once a VM's TTL expires, gc stops it (PVCs and disk state survive —
     reclaims the scarce resource, cluster CPU/RAM, immediately).
  2. Once it's sat stopped by gc (not by you) for the delete grace period
     (default 72h), gc deletes it outright, PVCs included.
  3. Every pass also deletes leaked disk PVCs (corral's *-bootc-disk, -disk,
     -data, -iso) whose owning VM no longer exists — the disks left behind
     when a build or gate dies between creating the PVC and its VM.

Run it by hand, or on a schedule (e.g. a CronJob calling ` + "`corral gc`" + `).
Non-ephemeral VMs are never touched (only their orphaned disks, once the VM
is truly gone).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		deleteAfter := kubevirt.GCDefaultDeleteAfter
		if gcDeleteAfter != "" {
			d, err := time.ParseDuration(gcDeleteAfter)
			if err != nil {
				return fmt.Errorf("--delete-after: %w", err)
			}
			deleteAfter = d
		}

		result, err := kubevirt.GC(gcDryRun, deleteAfter)
		if err != nil {
			return err
		}

		verb := map[bool]string{true: "would stop", false: "stopped"}[gcDryRun]
		if len(result.Stopped) == 0 {
			fmt.Println("Nothing to stop (no ephemeral VM past its TTL).")
		} else {
			fmt.Printf("%s (TTL expired, PVCs kept):\n", verb)
			for _, name := range result.Stopped {
				fmt.Printf("  %s\n", name)
			}
		}

		verb = map[bool]string{true: "would delete", false: "deleted"}[gcDryRun]
		if len(result.Deleted) == 0 {
			fmt.Println("Nothing to delete (no gc-stopped VM past its grace period).")
		} else {
			fmt.Printf("%s (past the delete grace period):\n", verb)
			for _, name := range result.Deleted {
				fmt.Printf("  %s\n", name)
			}
		}

		verb = map[bool]string{true: "would delete", false: "deleted"}[gcDryRun]
		if len(result.OrphanedPVCs) == 0 {
			fmt.Println("No orphaned disk PVCs (every corral disk has a VM).")
		} else {
			fmt.Printf("%s orphaned disk PVCs (owning VM gone):\n", verb)
			for _, p := range result.OrphanedPVCs {
				fmt.Printf("  %s\n", p)
			}
		}

		for _, e := range result.Errors {
			fmt.Fprintf(cmd.ErrOrStderr(), "error: %s\n", e)
		}
		if len(result.Errors) > 0 {
			return fmt.Errorf("%d error(s) during gc", len(result.Errors))
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(gcCmd)
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", false, "Report what would happen without stopping/deleting anything")
	gcCmd.Flags().StringVar(&gcDeleteAfter, "delete-after", "", "Grace period a gc-stopped VM sits before deletion (default 72h)")
}
