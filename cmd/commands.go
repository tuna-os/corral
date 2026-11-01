package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/fleet"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/types"
)

var (
	sshUser          string
	sshIdentity      string
	sshCommand       string
	sshPort          int
	sshPassword      string
	sshLocalForwards []string
	sshVsock         bool
)

var startCmd = &cobra.Command{
	Use:     "start [name]",
	Short:   "Start a VM",
	Example: `  corral start myvm`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "start")
		if err != nil {
			return err
		}
		if p, ok, err := resolvePeerTarget(name); ok {
			if err != nil {
				return err
			}
			return p.action("POST", "start")
		}
		power, err := instancePower(name)
		if err != nil {
			return err
		}
		return power.Start(name)
	},
}

var stopCmd = &cobra.Command{
	Use:     "stop [name]",
	Short:   "Stop a VM",
	Example: `  corral stop myvm`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "stop")
		if err != nil {
			return err
		}
		if p, ok, err := resolvePeerTarget(name); ok {
			if err != nil {
				return err
			}
			return p.action("POST", "stop")
		}
		power, err := instancePower(name)
		if err != nil {
			return err
		}
		return power.Stop(name)
	},
}

var infoCmd = &cobra.Command{
	Use:   "info [name]",
	Short: "Show VM details",
	Long: `Show VM details.

Without --json, info prints the backend's own description of the VM. With
--json, it prints one structured object: name, namespace, backend, status,
node, CPU topology and pinning, memory, disks, input devices, firmware, IP
addresses, tags, labels, and the bootc image if one is recorded.`,
	Example: `  corral info myvm
  corral info myvm --json | jq '{node, cpu, inputs}'`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "view details for")
		if err != nil {
			return err
		}
		if p, ok, err := resolvePeerTarget(name); ok {
			if err != nil {
				return err
			}
			return p.action("GET", "")
		}
		adapter, err := instanceAdapter(name)
		if err != nil {
			return err
		}
		if jsonOutput {
			detailer, ok := adapter.(backend.Detailer)
			if !ok {
				return unsupported(adapter, "info --json")
			}
			details, err := detailer.Details(name)
			if err != nil {
				return err
			}
			return printJSON(details)
		}
		describer, ok := adapter.(backend.Describer)
		if !ok {
			return unsupported(adapter, "info")
		}
		data, err := describer.Describe(name)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	},
}

var viewerCmd = &cobra.Command{
	Use:     "viewer [name]",
	Short:   "Launch VNC viewer for a VM",
	Example: `  corral viewer myvm`,
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "view")
		if err != nil {
			return err
		}
		adapter, err := instanceAdapter(name)
		if err != nil {
			return err
		}
		viewer, ok := adapter.(backend.Viewer)
		if !ok {
			return unsupported(adapter, "viewer")
		}
		return viewer.View(name)
	},
}

// resolveSSHCredentials resolves the username/password for `corral ssh
// <name>`, checking (in order) the explicit flag, then whatever's
// remembered in the registry, then $USER/"root" for username. If
// flagUser was passed explicitly and differs from what's remembered, it's
// saved back to the registry when possible — so a user only needs `-u` once
// per host. A failed cache write warns but does not change this request's credentials.
func resolveSSHCredentials(ref types.InstanceRef, flagUser, flagPassword string) (user, password string) {
	var saved types.RegistryEntry
	if registryStore != nil {
		saved, _ = registryStore.GetRef(ref)
	}

	user = flagUser
	if user == "" {
		user = saved.Username
	}
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "root"
	}

	if flagUser != "" && flagUser != saved.Username && registryStore != nil {
		updated := saved
		updated.Username = flagUser
		if err := registryStore.SetRef(ref, updated); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remember SSH username; using the requested credentials for this session: %v\n", err)
		}
	}

	password = flagPassword
	if password == "" {
		password = saved.Password
	}
	return user, password
}

var sshCmd = &cobra.Command{
	Use:   "ssh [name]",
	Short: "Open an SSH session to a VM",
	Long: `Open an interactive SSH session to a VM.

For KubeVirt VMs, this uses virtctl ssh which tunnels through the
Kubernetes API. For QEMU VMs, it connects to the VM's Tailscale IP.

-L forwards a local port through the VM, same as ssh -L: [bind_address:]port:host:hostport.
Repeatable. host is resolved from the VM's side, so "localhost" there means
the VM itself, not your machine — e.g. -L 8080:localhost:80 reaches
something the VM has listening on its own port 80.`,
	Example: `  corral ssh myvm
  corral ssh myvm --user root
  corral ssh myvm -u root -i ~/.ssh/vm_key
  corral ssh myvm -c "ls /"
  corral ssh myvm -L 8080:localhost:80
  corral ssh myvm -L 5432:localhost:5432 -L 6379:localhost:6379`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "ssh into")
		if err != nil {
			return err
		}
		if p, ok, err := resolvePeerTarget(name); ok {
			if err != nil {
				return err
			}
			user := sshUser
			if user == "" {
				user = os.Getenv("USER")
			}
			if user == "" {
				user = "root"
			}
			return p.ssh(user, sshCommand)
		}
		ref, err := instanceRef(name)
		if err != nil {
			return err
		}
		adapter, err := backend.For(ref)
		if err != nil {
			return err
		}
		sheller, ok := adapter.(backend.Sheller)
		if !ok {
			return unsupported(adapter, "ssh")
		}
		user, password := resolveSSHCredentials(ref, sshUser, sshPassword)
		return sheller.SSH(name, backend.SSHOptions{
			User: user, Identity: sshIdentity, Command: sshCommand, Port: sshPort,
			Password: password, LocalForwards: sshLocalForwards, Vsock: sshVsock,
		})
	},
}

var (
	logsSerial bool
	logsTail   int
)

var logsCmd = &cobra.Command{
	Use:   "logs [name]",
	Short: "Tail VM logs",
	Long: `Tail VM logs. For QEMU VMs, --serial streams the guest console
(serial.log) instead of the systemd journal — the evidence that survives a
boot that never reaches SSH (panic, dracut emergency shell, failed unit).
This is the log iso-e2e.sh gates on; corral diagnose bundles it.`,
	Example: `  corral logs myvm
  corral logs myvm --serial --tail 100`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name, err := requireOrPrompt(args, "view logs for")
		if err != nil {
			return err
		}
		adapter, err := instanceAdapter(name)
		if err != nil {
			return err
		}
		if logsSerial {
			serial, ok := adapter.(backend.SerialLogger)
			if !ok {
				return unsupported(adapter, "logs --serial")
			}
			return serial.SerialLogs(name, logsTail)
		}
		logger, ok := adapter.(backend.Logger)
		if !ok {
			return unsupported(adapter, "logs")
		}
		return logger.Logs(name)
	},
}

func init() {
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(infoCmd)
	rootCmd.AddCommand(viewerCmd)
	rootCmd.AddCommand(sshCmd)
	rootCmd.AddCommand(logsCmd)

	sshCmd.Flags().StringVarP(&sshUser, "user", "u", "", "SSH username (default: $USER)")
	sshCmd.Flags().StringVarP(&sshIdentity, "identity", "i", "", "Path to SSH private key")
	sshCmd.Flags().StringVarP(&sshCommand, "command", "c", "", "Command to execute (non-interactive)")
	sshCmd.Flags().IntVarP(&sshPort, "port", "p", 22, "SSH port")
	sshCmd.Flags().StringVar(&sshPassword, "password", "", "SSH password (uses cloud-init password if empty)")
	sshCmd.Flags().StringArrayVarP(&sshLocalForwards, "local-forward", "L", nil, "Forward a local port through the VM: [bind_address:]port:host:hostport (repeatable)")
	sshCmd.Flags().BoolVar(&sshVsock, "vsock", false, "Use AF_VSOCK transport (guest CID via vsock, not TCP hostfwd — for live ISOs with sshd disabled)")
	logsCmd.Flags().BoolVar(&logsSerial, "serial", false, "Show guest serial console (serial.log) instead of journal")
	logsCmd.Flags().IntVar(&logsTail, "tail", 0, "When --serial, show last N lines (0=all)")
}

func requireOrPrompt(args []string, action string) (string, error) {
	if len(args) == 1 && args[0] != "" {
		for _, peer := range config.Peers() {
			if strings.HasPrefix(args[0], peer.Name+"/") {
				return args[0], nil
			}
		}
		selectedVM = nil
		vm, err := resolveVM(args[0])
		if err != nil {
			return "", err
		}
		selectedVM = &vm
		activateVMContext(vm)
		return vm.Name, nil
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
	result := fleet.List(context.Background())
	counts := map[string]int{}
	for _, vm := range result.VMs {
		counts[vm.Name]++
	}
	var names []string
	for _, vm := range result.VMs {
		if counts[vm.Name] == 1 {
			names = append(names, vm.Name)
		} else {
			names = append(names, vm.ID)
		}
	}
	return uniq(names)
}

func resolveNamespace(name string) (string, string) {
	if selectedVM != nil && selectedVM.Name == name {
		activateVMContext(*selectedVM)
		return selectedVM.Namespace, selectedVM.Backend
	}
	if vm, err := resolveVM(name); err == nil {
		activateVMContext(vm)
		return vm.Namespace, vm.Backend
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
