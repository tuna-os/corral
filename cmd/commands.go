package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var (
	startNoViewer bool
)

var startCmd = &cobra.Command{
	Use:   "start [name]",
	Short: "Start a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := requireOrPrompt(args, "start")
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			return startKubevirt(name)
		}
		return startQemu(name)
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop [name]",
	Short: "Stop a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := requireOrPrompt(args, "stop")
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			return stopKubevirt(name)
		}
		return stopQemu(name)
	},
}

var deleteCmd = &cobra.Command{
	Use:   "delete [name]",
	Short: "Delete a VM and its disks",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := requireOrPrompt(args, "delete")
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			return deleteKubevirt(name, forceDelete)
		}
		return deleteQemu(name, forceDelete)
	},
}

var infoCmd = &cobra.Command{
	Use:   "info [name]",
	Short: "Show VM details",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := requireOrPrompt(args, "view details for")
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			return infoKubevirt(name)
		}
		return infoQemu(name)
	},
}

var viewerCmd = &cobra.Command{
	Use:   "viewer [name]",
	Short: "Launch VNC viewer for a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := requireOrPrompt(args, "view")
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			return viewerKubevirt(name)
		}
		return viewerQemu(name)
	},
}

var logsCmd = &cobra.Command{
	Use:   "logs [name]",
	Short: "Tail VM logs",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := requireOrPrompt(args, "view logs for")
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			return logsKubevirt(name)
		}
		return logsQemu(name)
	},
}

var forceDelete bool

func init() {
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(infoCmd)
	rootCmd.AddCommand(viewerCmd)
	rootCmd.AddCommand(logsCmd)

	startCmd.Flags().BoolVar(&startNoViewer, "no-viewer", false, "Don't launch VNC viewer")

	deleteCmd.Flags().BoolVarP(&forceDelete, "force", "f", false, "Skip confirmation")
}

func requireOrPrompt(args []string, action string) (string, error) {
	if len(args) == 1 && args[0] != "" {
		return args[0], nil
	}
	// TODO: interactive name selection via TUI
	names := allVMNames()
	if len(names) == 0 {
		return "", fmt.Errorf("no VMs found. Create one first: tailvm create <name>")
	}
	// For now, just print available names
	fmt.Fprintf(os.Stderr, "Available VMs to %s:\n", action)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", n)
	}
	return "", fmt.Errorf("please specify a VM name")
}

func allVMNames() []string {
	var names []string
	// QEMU VMs
	qemuDir := filepathJoin(homeDir(), qemuBaseDir)
	if entries, err := os.ReadDir(qemuDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != "cache" {
				names = append(names, e.Name())
			}
		}
	}
	// KubeVirt VMs
	out, err := exec.Command("kubectl", "get", "vms", "-A", "-o", "jsonpath={.items[*].metadata.name}").Output()
	if err == nil {
		for _, n := range splitFields(string(out)) {
			names = append(names, n)
		}
	}
	return uniq(names)
}

// Stub backends — panic until implemented
func startQemu(n string) error    { return fmt.Errorf("QEMU backend: use Python tailvm for now") }
func startKubevirt(n string) error { return fmt.Errorf("KubeVirt backend: use Python tailvm for now") }
func stopQemu(n string) error     { return fmt.Errorf("QEMU backend: use Python tailvm for now") }
func stopKubevirt(n string) error { return fmt.Errorf("KubeVirt backend: use Python tailvm for now") }
func deleteQemu(n string, f bool) error  { return fmt.Errorf("QEMU backend: use Python tailvm for now") }
func deleteKubevirt(n string, f bool) error { return fmt.Errorf("KubeVirt backend: use Python tailvm for now") }
func infoQemu(n string) error     { return fmt.Errorf("QEMU backend: use Python tailvm for now") }
func infoKubevirt(n string) error { return fmt.Errorf("KubeVirt backend: use Python tailvm for now") }
func viewerQemu(n string) error   { return fmt.Errorf("QEMU backend: use Python tailvm for now") }
func viewerKubevirt(n string) error { return fmt.Errorf("KubeVirt backend: use Python tailvm for now") }
func logsQemu(n string) error     { return fmt.Errorf("QEMU backend: use Python tailvm for now") }
func logsKubevirt(n string) error { return fmt.Errorf("KubeVirt backend: use Python tailvm for now") }
