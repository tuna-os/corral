package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all VMs (both backends)",
	RunE: func(cmd *cobra.Command, args []string) error {
		listQEMU()
		listKubeVirt()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(listCmd)
}

var (
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	runningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	stoppedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func listQEMU() {
	qemuDir := filepath.Join(homeDir(), qemuBaseDir)
	entries, err := os.ReadDir(qemuDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "cache" {
			continue
		}
		metaFile := filepath.Join(qemuDir, e.Name(), "metadata.json")
		data, err := os.ReadFile(metaFile)
		if err != nil {
			continue
		}
		var meta map[string]any
		if json.Unmarshal(data, &meta) != nil {
			continue
		}

		svc := "tailvm-" + e.Name()
		res, _ := exec.Command("systemctl", "--user", "is-active", svc).Output()
		active := strings.TrimSpace(string(res)) == "active"

		status := stoppedStyle.Render("○ Stopped")
		if active {
			status = runningStyle.Render("● Running")
		}

		fmt.Printf("%-20s  %-6s  %-16s  %v CPU  %v MEM\n",
			e.Name(), "qemu", status,
			meta["cpu"], meta["memory"])
	}
}

type kubevirtVM struct {
	Name    string `json:"name"`
	NS      string `json:"ns"`
	CPU     any    `json:"cpu"`
	Mem     string `json:"mem"`
	Status  string `json:"status"`
	Node    string `json:"node"`
	Ready   bool   `json:"ready"`
	Running bool   `json:"running"`
}

func listKubeVirt() {
	out, err := exec.Command("kubectl", "get", "vms", "-A", "-o", "json").Output()
	if err != nil {
		return
	}
	var result struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Running *bool `json:"running"`
				Template struct {
					Spec struct {
						Domain struct {
							CPU    struct{ Cores int } `json:"cpu"`
							Memory struct{ Guest string } `json:"memory"`
						} `json:"domain"`
						NodeSelector map[string]string `json:"nodeSelector"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				Ready bool `json:"ready"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &result) != nil {
		return
	}

	if len(result.Items) == 0 {
		return
	}

	// Header
	fmt.Printf("%s\n", headerStyle.Render(
		fmt.Sprintf("%-20s  %-6s  %-16s  %-12s  %-4s  %-6s  %s",
			"NAME", "BACKEND", "STATUS", "NAMESPACE", "CPU", "MEM", "NODE")))
	fmt.Println(strings.Repeat("─", 100))

	for _, vm := range result.Items {
		name := vm.Metadata.Name
		ns := vm.Metadata.Namespace
		cpu := fmt.Sprintf("%d", vm.Spec.Template.Spec.Domain.CPU.Cores)
		mem := vm.Spec.Template.Spec.Domain.Memory.Guest
		node := "—"
		if n, ok := vm.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"]; ok {
			node = n
		}

		running := vm.Spec.Running != nil && *vm.Spec.Running
		status := stoppedStyle.Render("○ Stopped")
		if vm.Status.Ready {
			status = runningStyle.Render("● Running")
		} else if running {
			status = "◐ Starting"
		}

		// Check proxy status
		proxyIcon := "○"
		if proxyStatus(name, ns) == "on" {
			proxyIcon = "🔵"
		}

		fmt.Printf("%-20s  %-6s  %-16s  %-12s  %-4s  %-6s  %s  ports:%s\n",
			name, "kubevirt", status, ns, cpu, mem, node, proxyIcon)
	}
}

func proxyStatus(name, ns string) string {
	out, err := exec.Command("kubectl", "get", "deploy", name+"-proxy", "-n", ns,
		"-o", "jsonpath={.status.readyReplicas}").Output()
	if err != nil || len(out) == 0 {
		return "off"
	}
	if strings.TrimSpace(string(out)) != "0" {
		return "on"
	}
	return "pending"
}
