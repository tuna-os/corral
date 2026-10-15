package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/ct"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/tuna-os/corral/pkg/vdi"
)

// ctPoolCmd manages CT-backed ephemeral desktop pools (#230). Unlike the VM
// pools above, their definition is a cluster-visible ConfigMap and a
// reconciler keeps the members in line with it. See docs/vdi-ct-pools.md.
func ctPoolCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "ct-pool",
		Short: "Manage CT-backed ephemeral desktop pools",
		Long: `CT-backed pools are ephemeral Linux desktops built from a container image.
The pool definition lives in the cluster (ConfigMap corral-vdi-ctpool-<name>);
"reconcile" creates, starts, replaces, and removes members to match it, and
never destroys a claimed member. Releasing a member destroys it and recreates
it from the image. See docs/vdi-ct-pools.md.`,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			kubeContext, err := ctPoolKubeContext()
			if err != nil || kubeContext == "" {
				return err
			}
			vdi.SetRunner(shell.WithKubeContext(shell.Real{}, kubeContext))
			ct.SetRunner(shell.WithKubeContext(shell.DefaultKubectl, kubeContext))
			return nil
		},
	}
	c.AddCommand(ctPoolApplyCmd(), ctPoolReconcileCmd(), ctPoolStatusCmd(), ctPoolListCmd(),
		ctPoolDeleteCmd(), ctPoolClaimCmd(), ctPoolReleaseCmd())
	return c
}

// ctPoolKubeContext is the kubeconfig context of the --context Corral
// context. Without --context it returns "", which leaves kubectl on its
// current context, as the VM pool commands do.
func ctPoolKubeContext() (string, error) {
	if contextName == "" {
		return "", nil
	}
	cfg, ok := config.FindContext(contextName)
	if !ok {
		return "", fmt.Errorf("unknown context %q (see corral context ls)", contextName)
	}
	if cfg.Backend != "kubevirt" {
		return "", fmt.Errorf("CT pools need a Kubernetes cluster (context %q is %s)", contextName, cfg.Backend)
	}
	return cfg.Context, nil
}

func ctPoolApplyCmd() *cobra.Command {
	var spec vdi.CTPoolSpec
	var noReconcile bool
	c := &cobra.Command{
		Use:   "apply <name>",
		Short: "Create or update a CT pool definition, then reconcile it",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			spec.Name, spec.Namespace = args[0], nsOrDefault()
			applied, err := vdi.ApplyCTPool(spec)
			if err != nil {
				return err
			}
			fmt.Printf("CT pool %q defined: %d × %s (%s)\n", applied.Name, applied.Size, applied.Source.Image, applied.Mode)
			if noReconcile {
				return nil
			}
			status, err := vdi.ReconcileCTPool(applied.Namespace, applied.Name)
			printCTPoolSummary(status)
			return err
		},
	}
	f := c.Flags()
	f.StringVar(&spec.Source.Image, "image", "", "Container image every member is created from (required)")
	f.IntVar(&spec.Size, "size", 1, "Number of members")
	f.StringVar(&spec.Mode, "mode", vdi.PoolModeEphemeral, "Pool mode; only \"ephemeral\" is supported")
	f.IntVar(&spec.Source.CPU, "cpu", 0, "vCPU cores per member (default 1)")
	f.StringVar(&spec.Source.Mem, "mem", "", "Memory per member, e.g. 2Gi (default 512Mi)")
	f.StringVar(&spec.Source.Disk, "disk", "", "Data volume size per member, e.g. 5Gi (default 5Gi)")
	f.StringVar(&spec.Source.StorageClass, "storage-class", "", "StorageClass for member volumes (default: the cluster default)")
	f.BoolVar(&spec.Source.Privileged, "privileged", false, "Run members as privileged CTs (persistent rootfs on the member volume)")
	f.BoolVar(&spec.Source.Init, "init", false, "Run the image's own entrypoint instead of sleep infinity")
	f.BoolVar(&noReconcile, "no-reconcile", false, "Only store the definition")
	_ = c.MarkFlagRequired("image")
	return c
}

func ctPoolReconcileCmd() *cobra.Command {
	var watch bool
	var interval time.Duration
	c := &cobra.Command{
		Use:   "reconcile <name>",
		Short: "Bring a CT pool's members in line with its definition",
		Long: `Creates missing members, starts stopped ones, replaces drifted or failed
unclaimed ones, and removes unclaimed members above the pool size. The result
is recorded in the pool's ConfigMap. With --watch it repeats every --interval
until interrupted, so failed steps are retried automatically.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := nsOrDefault()
			if !watch {
				status, err := vdi.ReconcileCTPool(ns, args[0])
				printCTPoolSummary(status)
				return err
			}
			if interval <= 0 {
				return fmt.Errorf("--interval must be positive")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return watchCTPool(ctx, ns, args[0], interval)
		},
	}
	c.Flags().BoolVar(&watch, "watch", false, "Keep reconciling until interrupted")
	c.Flags().DurationVar(&interval, "interval", 30*time.Second, "Time between reconciles with --watch")
	return c
}

func watchCTPool(ctx context.Context, ns, name string, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		status, err := vdi.ReconcileCTPool(ns, name)
		printCTPoolSummary(status)
		if err != nil {
			fmt.Fprintln(os.Stderr, "reconcile:", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func printCTPoolSummary(s vdi.CTPoolStatus) {
	if s.Pool == "" {
		return
	}
	line := fmt.Sprintf("%s  ns/%s  desired %d, observed %d, ready %d, claimed %d, free %d",
		s.Pool, s.Namespace, s.Desired, s.Observed, s.Ready, s.Claimed, s.Free)
	if s.Unschedulable > 0 {
		line += fmt.Sprintf(", unschedulable %d", s.Unschedulable)
	}
	if s.Exhausted {
		line += "  (no free members)"
	}
	fmt.Println(line)
}

func ctPoolStatusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status <name>",
		Short: "Show the live state of every member (read-only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			status, err := vdi.ObserveCTPool(nsOrDefault(), args[0])
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			}
			printCTPoolSummary(status)
			// tabwriter buffers; a write error surfaces from Flush.
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "MEMBER\tSTATE\tCLAIMED BY\tNOTES")
			for _, m := range status.Members {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.Name, m.State, m.ClaimedBy, ctMemberNotes(m))
			}
			return w.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Print the status as JSON")
	return c
}

func ctMemberNotes(m vdi.CTMemberStatus) string {
	var notes []string
	add := func(ok bool, s string) {
		if ok {
			notes = append(notes, s)
		}
	}
	add(m.ClaimExpired, "claim expired")
	add(m.Maintenance, "maintenance")
	add(m.Drifted, "drifted")
	add(m.Excess, "above pool size")
	add(m.Message != "", m.Message)
	return strings.Join(notes, "; ")
}

func ctPoolListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List CT pool definitions and their last recorded status",
		RunE: func(_ *cobra.Command, _ []string) error {
			specs, statuses, err := vdi.ListCTPools(namespace)
			if len(specs) == 0 {
				if err != nil {
					return err
				}
				fmt.Println("No CT pools found.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "NAME\tNAMESPACE\tSIZE\tREADY\tFREE\tIMAGE\tLAST RECONCILE")
			for i, s := range specs {
				st := statuses[i]
				last := "never"
				if !st.ObservedAt.IsZero() {
					last = st.ObservedAt.Format(time.RFC3339)
					if len(st.Errors) > 0 {
						last += fmt.Sprintf(" (%d errors)", len(st.Errors))
					}
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n", s.Name, s.Namespace, s.Size, st.Ready, st.Free, s.Source.Image, last)
			}
			if flushErr := w.Flush(); flushErr != nil {
				return flushErr
			}
			return err
		},
	}
}

func ctPoolDeleteCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a CT pool and all of its members",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := vdi.DeleteCTPool(nsOrDefault(), args[0], force); err != nil {
				return err
			}
			fmt.Printf("CT pool %q deleted\n", args[0])
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "Also destroy members that users have claimed")
	return c
}

func ctPoolClaimCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "claim <pool> <identity>",
		Short: "Atomically claim a free member for an identity (starts it if stopped)",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			member, err := vdi.ClaimCTMember(nsOrDefault(), args[0], args[1])
			if member != "" {
				fmt.Printf("claimed %s → %s\n", member, args[1])
				fmt.Printf("console:  corral ct console %s -n %s\n", member, nsOrDefault())
			}
			return err
		},
	}
}

func ctPoolReleaseCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "release <pool> <member> [identity]",
		Short: "Release a claimed member: destroy it and recreate it from the pool image",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(_ *cobra.Command, args []string) error {
			identity := ""
			if len(args) == 3 {
				identity = args[2]
			}
			if err := vdi.ReleaseCTMember(nsOrDefault(), args[0], args[1], identity, force); err != nil {
				return err
			}
			fmt.Printf("released %s\n", args[1])
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "Administrative release: ignore the claim holder")
	return c
}
