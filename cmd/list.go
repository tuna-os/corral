package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/charmbracelet/lipgloss"
	"github.com/hanthor/tailvm-go/pkg/kubevirt"
	"github.com/hanthor/tailvm-go/pkg/types"
	"github.com/spf13/cobra"
)

var (
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	runningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	stoppedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VMs (both backends)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runList()
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}

func runList() error {
	// KubeVirt VMs
	client := kubevirt.NewClient("")
	vms, err := client.ListVMs()
	if err == nil {
		printVMList(vms)
	}

	// QEMU VMs (stub)
	qemuVMs := listQemuVMs()
	for _, vm := range qemuVMs {
		status := stoppedStyle.Render("○ Stopped")
		if vm.Running {
			status = runningStyle.Render("● Running")
		}
		fmt.Printf("%-20s  %-6s  %-16s  %-12s  %-4v  %-6v  %s\n",
			vm.Name, "qemu", status, "—", vm.CPU, vm.Mem, "—")
	}

	return nil
}

func printVMList(vms []types.VM) {
	if len(vms) == 0 {
		return
	}

	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })

	fmt.Printf("%s\n", headerStyle.Render(
		fmt.Sprintf("%-20s  %-6s  %-16s  %-12s  %-4s  %-6s  %s",
			"NAME", "BACKEND", "STATUS", "NAMESPACE", "CPU", "MEM", "NODE")))
	fmt.Println("────────────────────────────────────────────────────────────────────────────────────────────────────")

	for _, vm := range vms {
		status := stoppedStyle.Render("○ Stopped")
		if vm.Ready {
			status = runningStyle.Render("● Running")
		} else if vm.Running {
			status = "◐ Starting"
		}

		proxyIcon := "○"
		if vm.VNC == "on" {
			proxyIcon = "🔵"
		} else if vm.VNC == "pending" {
			proxyIcon = "⏳"
		}

		fmt.Printf("%-20s  %-6s  %-16s  %-12s  %-4d  %-6s  %s  ports:%s\n",
			vm.Name, "kubevirt", status, vm.Namespace, vm.CPU, vm.Mem, vm.Node, proxyIcon)
	}
}

func listQemuVMs() []types.VM {
	home, _ := os.UserHomeDir()
	qemuDir := home + "/.local/share/tailvm/vms"
	entries, err := os.ReadDir(qemuDir)
	if err != nil {
		return nil
	}
	var vms []types.VM
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "cache" {
			continue
		}
		vms = append(vms, types.VM{
			Name:    e.Name(),
			Backend: "qemu",
		})
	}
	return vms
}

func resolveBackend(name string) string {
	if registryStore != nil {
		if entry, ok := registryStore.Get(name); ok {
			return entry.Backend
		}
	}
	// Check cluster
	client := kubevirt.NewClient("")
	if client.VMExists(name) {
		return "kubevirt"
	}
	return ""
}

func requireBackend(name string) (string, error) {
	b := resolveBackend(name)
	if b == "" {
		return "", fmt.Errorf("VM %q does not exist", name)
	}
	return b, nil
}
