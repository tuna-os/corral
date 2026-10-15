package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/setup"
)

var setupForce bool
var setupYes bool

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Scan this host for usable backends and save the ones you confirm",
	Long: `Probe the local host (qemu, incus, libvirt) plus already-configured
kubevirt/proxmox targets, then offer to save a context for each backend that
works. Every save is confirmed first; existing contexts are only replaced
with --force. The scan never dials hosts you have not configured.`,
	Example: `  corral setup
  corral setup --yes     # accept every offer without prompting
  corral setup --force   # allow replacing existing contexts`,
	RunE: func(cmd *cobra.Command, args []string) error {
		report := setup.Scan()
		for _, r := range report.Results {
			fmt.Printf("%-10s %-14s %s\n", r.Backend, r.Verdict, r.Detail)
			if r.Remedy != "" && r.Verdict != setup.VerdictReady {
				fmt.Printf("%-10s %-14s remedy: %s\n", "", "", r.Remedy)
			}
		}
		fmt.Println()

		// The local qemu backend needs no context; say so once.
		if q := report.ByBackend("qemu"); q != nil && q.Verdict == setup.VerdictReady {
			fmt.Println("qemu works locally — no context to save (it is always available).")
		}

		in := bufio.NewReader(os.Stdin)
		ask := func(prompt string) bool {
			if setupYes {
				return true
			}
			fmt.Printf("%s [y/N] ", prompt)
			line, _ := in.ReadString('\n')
			line = strings.ToLower(strings.TrimSpace(line))
			return line == "y" || line == "yes"
		}

		defaulted := false
		// Ranked: READY first, so the first offer is the default candidate —
		// at most one default is ever set.
		for _, r := range report.Ranked() {
			var opts setup.ApplyOptions
			// The scan says which backends get a context offer; a
			// configured-but-unhealthy PVE host is not re-offered, since an
			// explicit new host comes from flags, not from the scan.
			if !r.Offerable || r.Verdict != setup.VerdictReady {
				continue
			}
			if _, ok := config.FindContext(r.Backend); ok && !setupForce {
				fmt.Printf("%s: context %q already exists (use --force to replace).\n", r.Backend, r.Backend)
				continue
			}
			opts.Overwrite = setupForce
			if !defaulted && ask(fmt.Sprintf("Save the %s context%s?", r.Backend, defaultSuffix(r))) {
				if !defaulted && ask("Make it the default destination?") {
					opts.MakeDefault = true
					defaulted = true
				}
				if err := setup.Apply(r.Backend, opts); err != nil {
					fmt.Printf("%s: %v\n", r.Backend, err)
					continue
				}
				fmt.Printf("%s: context saved.\n", r.Backend)
				if h := setup.TakeHint(); h != "" {
					fmt.Println(h)
				}
			}
		}
		fmt.Println("\nRemote backends (proxmox, vsphere, xen, nutanix) need a host and credentials — add them explicitly:")
		fmt.Println("  corral context add <name> --backend proxmox --context <pve-host>")
		return nil
	},
}

func defaultSuffix(r setup.BackendResult) string {
	if _, ok := config.FindContext(r.Backend); ok {
		return ""
	}
	return " (first working backend — default candidate)"
}

func init() {
	setupCmd.Flags().BoolVar(&setupForce, "force", false, "Replace existing contexts instead of skipping them")
	setupCmd.Flags().BoolVar(&setupYes, "yes", false, "Accept every offer without prompting")
	rootCmd.AddCommand(setupCmd)
}
