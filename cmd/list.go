package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"
	"github.com/tuna-os/corral/pkg/backend"
	"github.com/tuna-os/corral/pkg/fleet"
	"github.com/tuna-os/corral/pkg/kubevirt"
	"github.com/tuna-os/corral/pkg/types"
)

var (
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	runningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	stoppedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

var selectedVM *types.VM

var (
	listPrefix string
	listTags   []string
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List VMs across every configured context",
	Example: `  corral list
  corral list --prefix tuna-q-
  corral list --tag pair=run42 --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runList()
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
	listCmd.Flags().StringVar(&listPrefix, "prefix", "", "Show only VMs whose name starts with this prefix")
	listCmd.Flags().StringArrayVar(&listTags, "tag", nil, "Show only VMs with this tag, as key=value (key alone means key=true); repeat to require all")
}

func runList() error {
	tags, err := kubevirt.ParseTags(listTags)
	if err != nil {
		return err
	}
	result := fleet.List(context.Background())
	vms := filterVMs(result.VMs, listPrefix, tags)

	if jsonOutput {
		sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
		if vms == nil {
			vms = []types.VM{}
		}
		if err := printJSON(vms); err != nil {
			return fmt.Errorf("encode VM list: %w", err)
		}
		for target, message := range result.Errors {
			fmt.Fprintf(os.Stderr, "warning: context %s unavailable: %s\n", target, message)
		}
		return nil
	}
	if len(vms) == 0 {
		if listPrefix != "" || len(tags) > 0 {
			fmt.Println("No VMs match the filter.")
		} else {
			fmt.Println("No VMs found. Create one: corral create <name>")
		}
		return nil
	}
	printVMList(vms)
	for target, message := range result.Errors {
		fmt.Printf("warning: context %s unavailable: %s\n", target, message)
	}
	return nil
}

// filterVMs keeps the VMs whose name starts with prefix and that carry every
// tag with the given value. Only backends that store tags (KubeVirt labels)
// can match a tag filter.
func filterVMs(vms []types.VM, prefix string, tags map[string]string) []types.VM {
	if prefix == "" && len(tags) == 0 {
		return vms
	}
	var out []types.VM
	for _, vm := range vms {
		if !strings.HasPrefix(vm.Name, prefix) {
			continue
		}
		match := true
		for k, v := range tags {
			if got, ok := vm.TagValues[k]; !ok || got != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, vm)
		}
	}
	return out
}

func printVMList(vms []types.VM) {
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })

	fmt.Printf("%s\n", headerStyle.Render(
		fmt.Sprintf("%-20s  %-10s  %-16s  %-18s  %-12s  %-4s  %-6s  %s",
			"NAME", "BACKEND", "STATUS", "CONTEXT", "NAMESPACE", "CPU", "MEM", "NODE")))
	fmt.Println("────────────────────────────────────────────────────────────────────────────────────────────────────")

	for _, vm := range vms {
		status := stoppedStyle.Render("○ Stopped")
		if vm.Ready {
			status = runningStyle.Render("● Running")
		} else if vm.Running {
			status = "◐ Starting"
		} else if vm.Status != "" {
			status = vm.Status
		}

		ns := vm.Namespace
		if ns == "" {
			ns = "—"
		}
		node := vm.Node
		if node == "" {
			node = "—"
		}

		extra := ""
		switch vm.Backend {
		case "kubevirt":
			proxyIcon := "○"
			if vm.VNC == "on" {
				proxyIcon = "●"
			} else if vm.VNC == "pending" {
				proxyIcon = "◐"
			}
			extra = "ports:" + proxyIcon
		case "qemu":
			if vm.VNC != "" {
				extra = "vnc:" + vm.VNC
			}
		}
		if vm.Ephemeral {
			extra += "  ⏳" + ephemeralSummary(vm)
		}

		contextName := vm.Context
		if contextName == "" {
			contextName = "local"
		}
		if vm.Peer != "" {
			contextName = vm.Peer + "/" + contextName
		}
		fmt.Printf("%-20s  %-10s  %-16s  %-18s  %-12s  %-4d  %-6s  %s  %s\n",
			vm.Name, vm.Backend, status, contextName, ns, vm.CPU, vm.Mem, node, extra)
		fmt.Printf("  id: %s\n", vm.ID)
	}
}

// ephemeralSummary renders a short human hint for `corral list`: time left
// before `corral gc` stops the VM, or how it's progressing through the two
// gc stages once past that point.
func ephemeralSummary(vm types.VM) string {
	if vm.Running {
		expiresAt, err := time.Parse(time.RFC3339, vm.ExpiresAt)
		if err != nil {
			return " (no valid TTL)"
		}
		if left := time.Until(expiresAt); left > 0 {
			return fmt.Sprintf(" %s left", left.Round(time.Minute))
		}
		return " gc: stop due"
	}
	if vm.StoppedAt != "" {
		return " gc-stopped"
	}
	return ""
}

func resolveBackend(name string) string {
	if selectedVM != nil && selectedVM.Name == name {
		activateVMContext(*selectedVM)
		return selectedVM.Backend
	}
	vm, err := resolveVM(name)
	if err != nil {
		if registryStore != nil {
			if entry, ok := registryStore.Get(name); ok {
				activateVMContext(types.VM{Backend: entry.Backend, Context: entry.Context})
				return entry.Backend
			}
		}
		return ""
	}
	activateVMContext(vm)
	return vm.Backend
}

func requireBackend(name string) (string, error) {
	if selectedVM != nil && selectedVM.Name == name {
		activateVMContext(*selectedVM)
		return selectedVM.Backend, nil
	}
	vm, err := resolveVM(name)
	if err != nil {
		if registryStore != nil {
			if entry, ok := registryStore.Get(name); ok {
				activateVMContext(types.VM{Backend: entry.Backend, Context: entry.Context})
				return entry.Backend, nil
			}
		}
		return "", err
	}
	activateVMContext(vm)
	return vm.Backend, nil
}

// instanceAdapter resolves the VM a command names to its pkg/backend adapter,
// bound to that VM's own context. Commands then assert the family they need
// and refuse by backend name when it is absent — the alternative, a branch on
// the KubeVirt backend name with local QEMU as the else, sent every other
// backend's VMs to QEMU code that had never heard of them (#489).
func instanceAdapter(name string) (backend.Adapter, error) {
	ref, err := instanceRef(name)
	if err != nil {
		return nil, err
	}
	return backend.For(ref)
}

// instancePower is instanceAdapter asserted for Power, the family every
// backend is required to have; the refusal covers a registered backend that
// somehow lacks it rather than panicking on the assertion.
func instancePower(name string) (backend.Power, error) {
	adapter, err := instanceAdapter(name)
	if err != nil {
		return nil, err
	}
	power, ok := adapter.(backend.Power)
	if !ok {
		return nil, unsupported(adapter, "start, stop, and delete")
	}
	return power, nil
}

func instanceRef(name string) (types.InstanceRef, error) {
	if selectedVM != nil && selectedVM.Name == name {
		activateVMContext(*selectedVM)
		return selectedVM.Ref(), nil
	}
	vm, err := resolveVM(name)
	if err != nil {
		if registryStore != nil {
			if entry, ok := registryStore.Get(name); ok {
				vm = types.VM{Name: name, Backend: entry.Backend, Context: entry.Context, Namespace: entry.Namespace}
				activateVMContext(vm)
				return vm.Ref(), nil
			}
		}
		return types.InstanceRef{}, err
	}
	activateVMContext(vm)
	return vm.Ref(), nil
}

func resolveVM(selector string) (types.VM, error) {
	return fleet.Resolve(fleet.List(context.Background()).VMs, selector)
}

// activateVMContext exports the VM's context for its backend's package-level
// helpers; pkg/backend owns which variable that is.
func activateVMContext(vm types.VM) {
	backend.ActivateContext(vm.Ref())
}
