package cmd

import (
	"fmt"
	"os"

	"github.com/hanthor/tailvm-go/pkg/kubevirt"
	"github.com/spf13/cobra"
)

var (
	startNoViewer bool
	forceDelete   bool
)

var startCmd = &cobra.Command{
	Use:   "start [name]",
	Short: "Start a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "start")
		if err != nil {
			return err
		}
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			ns, _ := resolveNamespace(name)
			return kubevirt.NewClient(ns).StartVM(name)
		}
		return fmt.Errorf("QEMU backend: use Python tailvm for now")
	},
}

var stopCmd = &cobra.Command{
	Use:   "stop [name]",
	Short: "Stop a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "stop")
		if err != nil {
			return err
		}
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			ns, _ := resolveNamespace(name)
			return kubevirt.NewClient(ns).StopVM(name)
		}
		return fmt.Errorf("QEMU backend: use Python tailvm for now")
	},
}

var deleteCmd = &cobra.Command{
	Use:   "delete [name]",
	Short: "Delete a VM and its disks",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "delete")
		if err != nil {
			return err
		}
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			ns, _ := resolveNamespace(name)
			return kubevirt.NewClient(ns).DeleteVM(name)
		}
		return fmt.Errorf("QEMU backend: use Python tailvm for now")
	},
}

var infoCmd = &cobra.Command{
	Use:   "info [name]",
	Short: "Show VM details",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "view details for")
		if err != nil {
			return err
		}
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			ns, _ := resolveNamespace(name)
			data, err := kubevirt.NewClient(ns).VMInfo(name)
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}
		return fmt.Errorf("QEMU backend: use Python tailvm for now")
	},
}

var viewerCmd = &cobra.Command{
	Use:   "viewer [name]",
	Short: "Launch VNC viewer for a VM",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "view")
		if err != nil {
			return err
		}
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			ns, _ := resolveNamespace(name)
			return kubevirt.NewClient(ns).Viewer(name)
		}
		return fmt.Errorf("QEMU backend: use Python tailvm for now")
	},
}

var logsCmd = &cobra.Command{
	Use:   "logs [name]",
	Short: "Tail VM logs",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := requireOrPrompt(args, "view logs for")
		return err
	},
}

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
	names := allVMNames()
	if len(names) == 0 {
		return "", fmt.Errorf("no VMs found. Create one: tailvm create <name>")
	}
	fmt.Fprintf(os.Stderr, "Available VMs to %s:\n", action)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", n)
	}
	return "", fmt.Errorf("please specify a VM name")
}

func allVMNames() []string {
	var names []string
	// KubeVirt VMs
	client := kubevirt.NewClient("")
	vms, err := client.ListVMs()
	if err == nil {
		for _, vm := range vms {
			names = append(names, vm.Name)
		}
	}
	// QEMU VMs
	home, _ := os.UserHomeDir()
	qemuDir := home + "/.local/share/tailvm/vms"
	if entries, err := os.ReadDir(qemuDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != "cache" {
				names = append(names, e.Name())
			}
		}
	}
	return uniq(names)
}

func resolveNamespace(name string) (string, string) {
	if registryStore != nil {
		if entry, ok := registryStore.Get(name); ok && entry.Namespace != "" {
			return entry.Namespace, entry.Backend
		}
	}
	return "default", resolveBackend(name)
}
