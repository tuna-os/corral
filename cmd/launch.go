package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/launcher"
	"github.com/tuna-os/corral/pkg/qemu"
)

var (
	launchManifest       string
	launchImage          string
	launchSHA256         string
	launchSize           int64
	launchArch           string
	launchFirmware       string
	launchFormat         string
	launchName           string
	launchMem            string
	launchCPU            int
	launchDisk           string
	launchWebPort        int
	launchVMTimeout      int
	launchWebTimeout     int
	launchStopVM         bool
	launchForce          bool
	launchAllowEmulation bool
	launchAllowCrossArch bool
)

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "One-line verified Tuna VM + loopback web console launcher",
	Long: `Launch a verified Tuna VM with QEMU and open its noVNC console in a browser.

The launcher binds every byte to an immutable manifest (exact URL, SHA256,
length, guest arch, firmware) and refuses anything unverified. There is no
default Tuna artifact: package/image qualification is a prerequisite you
satisfy by passing --manifest or explicit --image coordinates, never
something the launcher claims.

Pipeline: preflight (host/arch/QEMU/firmware/disk) → verified download into
a digest-keyed cache → VM create (explicit guest arch, exclusive loopback
ports) → VM readiness (process alive + VNC accepting) → owned loopback web
server → browser to the encoded console URL.

The VM survives launcher exit by default (closing the browser never stops
the guest). Pass --stop-vm to stop the VM on exit. The web server is always
owned: it binds 127.0.0.1 only and shuts down verified on SIGINT/SIGTERM.

Native scopes: linux/amd64 (KVM), linux/arm64 (KVM), darwin/amd64 (HVF),
darwin/arm64 (HVF). Anything else needs --allow-emulation (same-arch TCG)
or --allow-cross-arch (cross-arch TCG, always emulated) and is reported as
emulated, never native.`,
	Example: `  # From a manifest (recommended: the manifest IS the qualification record)
  corral launch --manifest ./tuna-v1-amd64.json --name tuna-v1

  # From explicit coordinates (all five required, no defaults)
  corral launch --name tuna-v1 \
    --image https://example.com/tuna.qcow2 \
    --sha256 <64-hex> --size <bytes> --arch amd64 --firmware uefi

  # Apple Silicon native + stop the VM on exit
  corral launch --manifest ./tuna-v1-arm64.json --name tuna-v1 --stop-vm`,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts, err := launchOptsFromFlags()
		if err != nil {
			return err
		}
		// The signal context is installed before the first phase, so Ctrl-C
		// during download, creation or readiness cancels the wait instead
		// of stranding it — and the failure path below owns exactly the
		// running state this invocation started.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		opts.Ctx = ctx
		imagePath, im, err := launcher.Prepare(opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Image verified: %s (%s/%s, %d bytes)\n", imagePath, im.Arch, im.Firmware, im.Size)
		created, admitted, err := launcher.EnsureVMAdmitted(opts, imagePath, im)
		if err != nil {
			return err
		}
		if created {
			fmt.Fprintf(os.Stderr, "VM %q created.\n", opts.Name)
		} else {
			fmt.Fprintf(os.Stderr, "VM %q exists and matches the manifest — reusing it.\n", opts.Name)
		}
		vmTimeout := time.Duration(launchVMTimeout) * time.Second
		if vmTimeout <= 0 {
			vmTimeout = 5 * time.Minute
		}
		fmt.Fprintf(os.Stderr, "Starting VM %q (waiting up to %s for VNC readiness)...\n", opts.Name, vmTimeout)
		started, tok, err := launcher.StartAndWaitAdmitted(ctx, opts.Name, admitted, vmTimeout)
		if err != nil {
			// The started/token outcome survives the error (a child
			// launched but unverified still reaches cleanup), and a
			// failed cleanup joins the primary error instead of hiding
			// a possibly-still-running VM behind it.
			if cerr := cleanupOwnedStart(opts.Name, started, tok); cerr != nil {
				return errors.Join(err, cerr)
			}
			return err
		}
		fmt.Fprintf(os.Stderr, "VM %q is ready.\n", opts.Name)
		res, openErr := launcher.ServeAndOpenCtx(ctx, opts.Name, opts)
		if res == nil {
			if cerr := cleanupOwnedStart(opts.Name, started, tok); cerr != nil {
				return errors.Join(openErr, cerr)
			}
			return openErr
		}
		res.ImagePath = imagePath
		res.Started, res.Token = started, tok
		fmt.Fprintf(os.Stderr, "Console: %s\n", res.Console)
		if openErr != nil {
			fmt.Fprintf(os.Stderr, "Note: %v\n", openErr)
		}
		if opts.StopVM {
			fmt.Fprintf(os.Stderr, "Press Ctrl-C to stop the web server and the VM.\n")
		} else {
			fmt.Fprintf(os.Stderr, "Press Ctrl-C to stop the web server (the VM keeps running).\n")
		}
		<-ctx.Done()
		fmt.Fprintf(os.Stderr, "\nShutting down...\n")
		return launcher.Shutdown(res, opts.StopVM)
	},
}

// cleanupOwnedStart stops a VM this invocation started when a later phase
// failed. The unit-testable logic (token-owned stop, stranger refusal)
// lives in launcher.CleanupOwnedStart; this stays a thin delegate so cmd
// carries no lifecycle logic of its own. The cleanup error is returned for
// joining with the primary error.
func cleanupOwnedStart(name string, started bool, tok qemu.StartToken) error {
	return launcher.CleanupOwnedStart(name, started, tok)
}

func launchOptsFromFlags() (launcher.LaunchOpts, error) {
	name := launchName
	if name == "" {
		name = "tuna-v1"
	}
	opts := launcher.LaunchOpts{
		Name:           name,
		Mem:            launchMem,
		CPU:            launchCPU,
		Disk:           launchDisk,
		ManifestPath:   launchManifest,
		Arch:           launchArch,
		Firmware:       launchFirmware,
		AllowEmulation: launchAllowEmulation,
		AllowCrossArch: launchAllowCrossArch,
		WebPort:        launchWebPort,
		VMTimeout:      time.Duration(launchVMTimeout) * time.Second,
		WebTimeout:     time.Duration(launchWebTimeout) * time.Second,
		StopVM:         launchStopVM,
		Force:          launchForce,
	}
	if launchImage != "" || launchSHA256 != "" || launchSize != 0 || launchArch != "" || launchFirmware != "" {
		// Explicit coordinates: all five are required, no partial default.
		if launchManifest != "" {
			return opts, fmt.Errorf("pass exactly one of --manifest or explicit --image coordinates, not both")
		}
		if launchImage == "" || launchSHA256 == "" || launchSize == 0 || launchArch == "" || launchFirmware == "" {
			return opts, fmt.Errorf("explicit image needs all of --image, --sha256, --size, --arch, --firmware (there is no default Tuna artifact)")
		}
		format := launchFormat
		if format == "" {
			format = "qcow2"
		}
		opts.Image = &launcher.ManifestImage{
			URL:      launchImage,
			SHA256:   launchSHA256,
			Size:     launchSize,
			Arch:     launchArch,
			Firmware: launchFirmware,
			Format:   format,
		}
		// Clear the override fields: they ARE the image here, not overrides.
		opts.Arch = ""
		opts.Firmware = ""
	}
	if opts.ManifestPath == "" && opts.Image == nil {
		return opts, fmt.Errorf("no image: pass --manifest <file> or explicit --image/--sha256/--size/--arch/--firmware (there is no default Tuna artifact — package qualification is a prerequisite)")
	}
	return opts, nil
}

func init() {
	rootCmd.AddCommand(launchCmd)
	launchCmd.Flags().StringVar(&launchManifest, "manifest", "", "Path to an immutable image manifest (JSON: version/name/image{url,sha256,size,arch,firmware,format})")
	launchCmd.Flags().StringVar(&launchImage, "image", "", "Explicit image URL (https:// or file://) — requires --sha256/--size/--arch/--firmware")
	launchCmd.Flags().StringVar(&launchSHA256, "sha256", "", "Exact lowercase hex SHA256 of the image bytes")
	launchCmd.Flags().Int64Var(&launchSize, "size", 0, "Exact image length in bytes")
	launchCmd.Flags().StringVar(&launchArch, "arch", "", "Guest arch for explicit coordinates (amd64 or arm64)")
	launchCmd.Flags().StringVar(&launchFirmware, "firmware", "", "Firmware for explicit coordinates (uefi or bios)")
	launchCmd.Flags().StringVar(&launchFormat, "format", "qcow2", "Image format for explicit coordinates (qcow2 or raw)")
	launchCmd.Flags().StringVar(&launchName, "name", "tuna-v1", "VM name")
	launchCmd.Flags().StringVar(&launchMem, "mem", "4G", "Memory allocation")
	launchCmd.Flags().IntVar(&launchCPU, "cpu", 2, "CPU cores")
	launchCmd.Flags().StringVar(&launchDisk, "disk", "20G", "Disk size")
	launchCmd.Flags().IntVar(&launchWebPort, "web-port", 0, "Loopback web port (0 = any free port)")
	launchCmd.Flags().IntVar(&launchVMTimeout, "vm-timeout", 300, "Seconds to wait for VM VNC readiness")
	launchCmd.Flags().IntVar(&launchWebTimeout, "web-timeout", 30, "Seconds to wait for web readiness")
	launchCmd.Flags().BoolVar(&launchStopVM, "stop-vm", false, "Stop the VM on launcher exit (default: VM survives)")
	launchCmd.Flags().BoolVar(&launchForce, "force", false, "Recreate the VM when it already exists")
	launchCmd.Flags().BoolVar(&launchAllowEmulation, "allow-emulation", false, "Permit same-arch TCG when native KVM/HVF is unavailable (reported as emulated, never native)")
	launchCmd.Flags().BoolVar(&launchAllowCrossArch, "allow-cross-arch", false, "Permit a guest arch that differs from the host (always TCG emulated)")
}
