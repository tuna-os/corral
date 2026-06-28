package cmd

import (
	"fmt"
	"os"

	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/qemu"
	"github.com/spf13/cobra"
)

var (
	forceDelete bool
	sshUser     string
	sshIdentity string
	sshCommand  string
	sshPort     int
	sshPassword string
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
		return qemu.Start(name)
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
		return qemu.Stop(name)
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
		if !forceDelete {
			fmt.Fprintf(os.Stderr, "Delete VM %q and its disks? [y/N] ", name)
			var resp string
			fmt.Fscanln(os.Stdin, &resp)
			if resp != "y" && resp != "Y" && resp != "yes" {
				return fmt.Errorf("aborted")
			}
		}
		if backend == "kubevirt" {
			ns, _ := resolveNamespace(name)
			if err := kubevirt.NewClient(ns).DeleteVM(name); err != nil {
				return err
			}
		} else if err := qemu.Delete(name); err != nil {
			return err
		}
		if registryStore != nil {
			registryStore.Remove(name)
		}
		return nil
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
		data, err := qemu.Info(name)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
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
		return qemu.Viewer(name)
	},
}

var sshCmd = &cobra.Command{
	Use:   "ssh [name]",
	Short: "Open an SSH session to a VM",
	Long: `Open an interactive SSH session to a VM.

For KubeVirt VMs, this uses virtctl ssh which tunnels through the
Kubernetes API. For QEMU VMs, it connects to the VM's Tailscale IP.

Examples:
  corral ssh myvm
  corral ssh myvm --user root
  corral ssh myvm -u root -i ~/.ssh/vm_key
  corral ssh myvm -c "ls /"`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "ssh into")
		if err != nil {
			return err
		}
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}

		user := sshUser
		if user == "" {
			user = os.Getenv("USER")
			if user == "" {
				user = "root"
			}
		}

		password := sshPassword
		if password == "" && registryStore != nil {
			if entry, ok := registryStore.Get(name); ok {
				password = entry.Password
			}
		}

		if backend == "kubevirt" {
			ns, _ := resolveNamespace(name)
			return kubevirt.NewClient(ns).SSH(name, user, sshIdentity, sshCommand, sshPort, password)
		}
		return qemu.SSH(name, user, sshIdentity, sshCommand, sshPort, password)
	},
}

var logsCmd = &cobra.Command{
	Use:   "logs [name]",
	Short: "Tail VM logs",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "view logs for")
		if err != nil {
			return err
		}
		backend, err := requireBackend(name)
		if err != nil {
			return err
		}
		if backend == "kubevirt" {
			ns, _ := resolveNamespace(name)
			return kubevirt.NewClient(ns).Logs(name)
		}
		return qemu.Logs(name)
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(infoCmd)
	rootCmd.AddCommand(viewerCmd)
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(logsCmd)

	deleteCmd.Flags().BoolVarP(&forceDelete, "force", "f", false, "Skip confirmation")
	sshCmd.Flags().StringVarP(&sshUser, "user", "u", "", "SSH username (default: $USER)")
	sshCmd.Flags().StringVarP(&sshIdentity, "identity", "i", "", "Path to SSH private key")
	sshCmd.Flags().StringVarP(&sshCommand, "command", "c", "", "Command to execute (non-interactive)")
	sshCmd.Flags().IntVarP(&sshPort, "port", "p", 22, "SSH port")
	sshCmd.Flags().StringVar(&sshPassword, "password", "", "SSH password (uses cloud-init password if empty)")
}

func requireOrPrompt(args []string, action string) (string, error) {
	if len(args) == 1 && args[0] != "" {
		return args[0], nil
	}
	names := allVMNames()
	if len(names) == 0 {
		return "", fmt.Errorf("no VMs found. Create one: corral create <name>")
	}
	fmt.Fprintf(os.Stderr, "Available VMs to %s:\n", action)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", n)
	}
	return "", fmt.Errorf("please specify a VM name")
}

func allVMNames() []string {
	var names []string
	client := kubevirt.NewClient("")
	vms, err := client.ListVMs()
	if err == nil {
		for _, vm := range vms {
			names = append(names, vm.Name)
		}
	}
	qemuVMs, _ := qemu.List()
	for _, vm := range qemuVMs {
		names = append(names, vm.Name)
	}
	return uniq(names)
}

func resolveNamespace(name string) (string, string) {
	if registryStore != nil {
		if entry, ok := registryStore.Get(name); ok && entry.Namespace != "" {
			return entry.Namespace, entry.Backend
		}
	}
	// Search all KubeVirt namespaces for the VM
	client := kubevirt.NewClient("")
	vms, _ := client.ListVMs()
	for _, vm := range vms {
		if vm.Name == name {
			return vm.Namespace, "kubevirt"
		}
	}
	return kubevirt.DefaultNamespace, resolveBackend(name)
}
