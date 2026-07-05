package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/doctor"
)

var doctorFix bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose the cluster setup Corral's features need (and reconcile fixes)",
	Long: `Check whether the cluster has the pieces Corral's features need —
KubeVirt, CDI, the right feature gates + LiveUpdate, expandable/snapshot
storage, the export proxy, metrics — and report what's missing or
misconfigured. --fix reconciles the safe, config-only issues.`,
	Example: `  corral doctor
  corral doctor --fix`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if doctorFix {
			fixed, err := doctor.Fix()
			if err != nil {
				return err
			}
			if len(fixed) == 0 {
				fmt.Println("Nothing to reconcile.")
			} else {
				fmt.Printf("Reconciled: %v\n\n", fixed)
			}
		}

		checks := doctor.Run()
		anyFixable := false
		for _, c := range checks {
			mark := "\033[32m✓\033[0m"
			if !c.OK {
				mark = "\033[31m✗\033[0m"
			}
			suffix := ""
			if !c.OK && c.Fixable {
				suffix = " \033[33m(fixable)\033[0m"
				anyFixable = true
			}
			fmt.Printf("  %s %-30s %s%s\n", mark, c.Name, c.Detail, suffix)
		}
		if anyFixable && !doctorFix {
			fmt.Println("\nReconcile the fixable items: corral doctor --fix")
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&doctorFix, "fix", false, "Reconcile fixable (config-only) issues")
}
