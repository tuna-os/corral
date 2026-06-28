// corral-backup is the S3/R2 backup Corral plugin: on-demand VM disk backups to
// any rclone remote and restore back into a fresh disk. It reuses KubeVirt's
// exporter to pull the disk, rclone to move it to object storage (the tuna-os
// convention for R2/S3), and `virtctl image-upload` to restore via CDI.
// Installed via the marketplace (`corral plugin install backup`) and invoked as
// `corral backup …`.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/shell"
	"github.com/spf13/cobra"
)

// runner shells out to rclone/virtctl; swapped in tests.
var runner shell.Runner = shell.Real{}

var (
	flagNamespace string
	flagDest      string
	flagSrc       string
	flagSize      string
	flagKeepLocal bool
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "backup",
		Short: "Back up and restore VM disks to S3/R2 (via rclone)",
		Long: "Back up KubeVirt VM disks to any rclone remote and restore them.\n\n" +
			"rclone must already be configured with your remote (e.g. an R2/S3\n" +
			"bucket): see `rclone config`. The remote is referenced as\n" +
			"`<remote>:<bucket>/<path>`.",
	}
	root.PersistentFlags().StringVarP(&flagNamespace, "namespace", "n", "", "VM namespace (default: corral's default)")
	root.AddCommand(createCmd(), restoreCmd(), listCmd())
	return root
}

func ns() string {
	if flagNamespace != "" {
		return flagNamespace
	}
	return kubevirt.DefaultNamespace
}

func createCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "create <vm>",
		Short: "Export a stopped VM's disk and upload it to the remote",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runBackup(args[0])
		},
	}
	c.Flags().StringVar(&flagDest, "dest", "", "rclone destination dir, e.g. r2:backups/corral (required)")
	c.Flags().BoolVar(&flagKeepLocal, "keep-local", false, "keep the local export file after upload")
	c.MarkFlagRequired("dest")
	return c
}

func restoreCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "restore <new-disk-name>",
		Short: "Download a backup from the remote and upload it into a new PVC (via CDI)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runRestore(args[0])
		},
	}
	c.Flags().StringVar(&flagSrc, "src", "", "rclone source file, e.g. r2:backups/corral/web-2026….img.gz (required)")
	c.Flags().StringVar(&flagSize, "size", "", "size of the restored disk, e.g. 20Gi (required)")
	c.MarkFlagRequired("src")
	c.MarkFlagRequired("size")
	return c
}

func listCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List backups under a remote dir",
		RunE: func(_ *cobra.Command, _ []string) error {
			out, err := runner.Run("rclone", "lsf", flagDest)
			if err != nil {
				return fmt.Errorf("rclone lsf %s: %w", flagDest, err)
			}
			fmt.Print(string(out))
			return nil
		},
	}
	c.Flags().StringVar(&flagDest, "dest", "", "rclone dir to list, e.g. r2:backups/corral (required)")
	c.MarkFlagRequired("dest")
	return c
}

// runBackup exports the VM disk locally then rclone-copies it to the remote.
func runBackup(vm string) error {
	if err := ensureRclone(); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "corral-backup-")
	if err != nil {
		return err
	}
	if !flagKeepLocal {
		defer os.RemoveAll(tmp)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	fname := fmt.Sprintf("%s-%s.img.gz", vm, stamp)
	local := filepath.Join(tmp, fname)

	fmt.Fprintf(os.Stderr, "exporting %s disk → %s …\n", vm, local)
	if _, err := kubevirt.NewClient(ns()).Export(vm, "", local); err != nil {
		return fmt.Errorf("export %s (is it stopped?): %w", vm, err)
	}

	dest := strings.TrimSuffix(flagDest, "/") + "/" + fname
	fmt.Fprintf(os.Stderr, "uploading → %s …\n", dest)
	if out, err := runner.Run("rclone", "copyto", local, dest); err != nil {
		return fmt.Errorf("rclone copyto: %s", strings.TrimSpace(string(out)))
	}
	fmt.Printf("backed up %s → %s\n", vm, dest)
	if flagKeepLocal {
		fmt.Printf("local copy kept at %s\n", local)
	}
	return nil
}

// runRestore downloads a backup and uploads it into a new CDI-backed PVC.
func runRestore(diskName string) error {
	if err := ensureRclone(); err != nil {
		return err
	}
	virtctl, err := kubevirt.NewClient(ns()).Virtctl()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "corral-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	local := filepath.Join(tmp, filepath.Base(flagSrc))

	fmt.Fprintf(os.Stderr, "downloading %s → %s …\n", flagSrc, local)
	if out, err := runner.Run("rclone", "copyto", flagSrc, local); err != nil {
		return fmt.Errorf("rclone copyto: %s", strings.TrimSpace(string(out)))
	}

	fmt.Fprintf(os.Stderr, "uploading into PVC %s (%s) via CDI …\n", diskName, flagSize)
	// CDI's upload proxy decompresses gzip and detects the image format.
	out, err := runner.Run(virtctl, "image-upload", "dv", diskName,
		"--size", flagSize, "--image-path", local, "--namespace", ns(), "--insecure")
	if err != nil {
		return fmt.Errorf("virtctl image-upload: %s", strings.TrimSpace(string(out)))
	}
	fmt.Printf("restored %s → DataVolume %s/%s\n", flagSrc, ns(), diskName)
	fmt.Printf("attach it to a VM with: corral create <name> --pvc %s\n", diskName)
	return nil
}

func ensureRclone() error {
	if _, err := runner.Run("rclone", "version"); err != nil {
		return fmt.Errorf("rclone not found or not configured — install it and run `rclone config` for your S3/R2 remote")
	}
	return nil
}
