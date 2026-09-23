package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/qemu"
)

var (
	diagBundleDir string
	diagJSON      bool
)

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose [name]",
	Short: "Run diagnostics on a QEMU VM (serial, screenshot, QMP, host checks)",
	Long: `Collect diagnostic evidence for a local QEMU VM, mirroring the
checks tuna-os/tunaos iso-e2e.sh runs: serial console for boot failures,
framebuffer blank detection, QMP status, vsock/TPM/OVMF host readiness.

Without --bundle-dir it prints a report to stdout; with it it writes an
evidence bundle (serial.log, screenshot.png, journal.log, qmp-status.json,
metadata.json, diagnostics.txt) suitable for CI artifacts — the same layout
scripts/evidence-bundle.sh collects.

A blank framebuffer or a boot_failed_on_serial signature is reported as a
warning; use --json for machine-readable output in gates.`,
	Example: `  corral diagnose myvm
  corral diagnose myvm --bundle-dir ./diag-out
  corral diagnose myvm --json`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := qemuOnly(args, "diagnose")
		if err != nil {
			return err
		}
		if diagBundleDir != "" {
			if err := qemu.DiagnosticBundle(name, diagBundleDir); err != nil {
				return err
			}
			fmt.Printf("Diagnostics written to %s\n", diagBundleDir)
		}
		// Serial boot failure
		if failed, sig := qemu.BootFailedOnSerial(name); failed {
			fmt.Fprintf(os.Stderr, "⚠️  boot failed on serial: %s\n", sig)
		} else {
			fmt.Fprintln(os.Stderr, "✓ serial log: no boot failure signature")
		}
		// QMP status
		if raw, err := qemu.QMPStatus(name); err != nil {
			fmt.Fprintf(os.Stderr, "QMP: %v\n", err)
		} else {
			if diagJSON {
				fmt.Println(string(raw))
			} else {
				fmt.Fprintf(os.Stderr, "QMP status: %s\n", string(raw))
			}
		}
		// Screenshot
		tmp := filepath.Join(os.TempDir(), name+"-diag-screenshot.png")
		if frame, err := qemu.Capture(name, tmp); err != nil {
			fmt.Fprintf(os.Stderr, "screenshot: %v\n", err)
		} else {
			verdict := "painted"
			if frame.Blank() {
				verdict = "blank ⚠️"
			}
			fmt.Fprintf(os.Stderr, "screenshot: %s (%dx%d dev %.4f — %s)\n", frame.Path, frame.Width, frame.Height, frame.StdDev, verdict)
			_ = os.Remove(tmp)
		}
		// Vsock / TPM / OVMF checks
		if ok, reason := qemu.VsockHostAvailable(); ok {
			fmt.Fprintln(os.Stderr, "vsock host: available")
		} else {
			fmt.Fprintf(os.Stderr, "vsock host: unavailable (%s)\n", reason)
		}
		if ok, reason := qemu.TPMHostAvailable(); ok {
			fmt.Fprintln(os.Stderr, "tpm host: available")
		} else {
			fmt.Fprintf(os.Stderr, "tpm host: unavailable (%s)\n", reason)
		}
		if qemu.UEFIAvailable() {
			fmt.Fprintln(os.Stderr, "ovmf: available")
		} else {
			fmt.Fprintln(os.Stderr, "ovmf: not found (install edk2-ovmf)")
		}
		// Serial tail snippet
		fmt.Fprintln(os.Stderr, "--- serial tail (last 20 lines) ---")
		fmt.Fprintln(os.Stderr, qemu.SerialTail(name, 20))
		// JSON summary if requested
		if diagJSON {
			metaRaw, _ := qemu.Info(name)
			var m any
			_ = json.Unmarshal(metaRaw, &m)
			out, _ := json.MarshalIndent(map[string]any{
				"vm":     m,
				"bundle": diagBundleDir,
			}, "", "  ")
			fmt.Println(string(out))
		}
		return nil
	},
}

var qmpCmd = &cobra.Command{
	Use:   "qmp [name] <command> [args...]",
	Short: "Run a QMP command on a QEMU VM (diagnostic)",
	Long: `Send a raw QMP command to a running QEMU VM's monitor socket.
Useful for diagnostics: query-status, query-block, query-vnc, etc.

This is the programmatic counterpart to the QEMU monitor that
tuna-os/tunaos scripts/iso-e2e.sh drives via "screendump" and
system_powerdown — corral screenshot/type/key use the same socket.`,
	Example: `  corral qmp myvm query-status
  corral qmp myvm query-block
  corral qmp myvm query-vnc`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := qemuOnly(args[:1], "qmp")
		if err != nil {
			return err
		}
		raw, err := qemu.QMPExec(name, args[1], args[2:])
		if err != nil {
			return err
		}
		fmt.Println(string(raw))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(diagnoseCmd)
	rootCmd.AddCommand(qmpCmd)
	diagnoseCmd.Flags().StringVar(&diagBundleDir, "bundle-dir", "", "Write evidence bundle to this directory (serial.log, screenshot.png, journal.log, qmp-status.json)")
	diagnoseCmd.Flags().BoolVar(&diagJSON, "json", false, "Emit JSON summary")
}
