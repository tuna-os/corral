package kubevirt

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hanthor/corral/pkg/types"
)

// LastPassword holds the cloud-init password from the most recent GenerateVM call.
var LastPassword string

// LoadSSHPublicKey reads the first available SSH public key from ~/.ssh/.
func LoadSSHPublicKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub", "id_ecdsa.pub"} {
		key, err := os.ReadFile(filepath.Join(home, ".ssh", name))
		if err == nil && len(key) > 0 {
			return strings.TrimSpace(string(key))
		}
	}
	return ""
}

// Client interacts with the Kubernetes cluster via kubectl.
type Client struct {
	Namespace string
}

// DefaultNamespace is the default namespace for KubeVirt VMs.
const DefaultNamespace = "tailvm"

// EnsureNamespace creates the namespace if it doesn't exist and labels it
// for privileged pods (needed by the bootc builder Job).
func EnsureNamespace(ns string) {
	if ns == "" {
		ns = DefaultNamespace
	}
	exec.Command("kubectl", "create", "ns", ns).Run() // no-op if it exists
	exec.Command("kubectl", "label", "ns", ns,
		"pod-security.kubernetes.io/enforce=privileged", "--overwrite").Run()
}

// NewClient creates a KubeVirt client.
func NewClient(ns string) *Client {
	if ns == "" {
		ns = DefaultNamespace
	}
	return &Client{Namespace: ns}
}

// VMExists checks if a VirtualMachine exists in the cluster.
func (c *Client) VMExists(name string) bool {
	cmd := exec.Command("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "name")
	return cmd.Run() == nil
}

// DataVolumeStatus returns the import progress for a VM's ISO DataVolume.
func DataVolumeStatus(name, ns string) string {
	out, err := exec.Command("kubectl", "get", "datavolume", name+"-iso", "-n", ns, "-o", "json").Output()
	if err != nil {
		return ""
	}
	var dv struct {
		Status struct {
			Phase    string `json:"phase"`
			Progress string `json:"progress"`
		} `json:"status"`
	}
	if json.Unmarshal(out, &dv) != nil {
		return ""
	}
	switch dv.Status.Phase {
	case "Succeeded":
		return "✓ ready"
	case "ImportInProgress", "ImportScheduled":
		if dv.Status.Progress != "" {
			return "↓ " + dv.Status.Progress
		}
		return "↓ importing"
	case "Pending", "PVCBound", "WaitForFirstConsumer":
		return "↓ queued"
	default:
		return "↓ " + dv.Status.Phase
	}
}

// ListVMs returns all KubeVirt VMs with status information.
func (c *Client) ListVMs() ([]types.VM, error) {
	out, err := exec.Command("kubectl", "get", "vms", "-A", "-o", "json").Output()
	if err != nil {
		return nil, err
	}

	var result struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Running  *bool `json:"running"`
				Template struct {
					Spec struct {
						Domain struct {
							CPU struct {
								Cores   int `json:"cores"`
								Sockets int `json:"sockets"`
								Threads int `json:"threads"`
							} `json:"cpu"`
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
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, err
	}

	vmis := vmiStatusIndex()
	vendors := nodeVendors()

	var vms []types.VM
	for _, vm := range result.Items {
		name := vm.Metadata.Name
		ns := vm.Metadata.Namespace
		running := vm.Spec.Running != nil && *vm.Spec.Running
		node := "—"
		if n, ok := vm.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"]; ok {
			node = n
		}

		status := "○ Stopped"
		if vm.Status.Ready {
			status = "● Running"
		} else if running {
			status = "◐ Starting"
		} else if iso := DataVolumeStatus(name, ns); iso != "" && iso != "✓ ready" {
			status = iso
		}

		cpu := vm.Spec.Template.Spec.Domain.CPU
		v := types.VM{
			Name:      name,
			Backend:   "kubevirt",
			Namespace: ns,
			Status:    status,
			Ready:     vm.Status.Ready,
			Running:   running,
			CPU:       totalVCPU(cpu.Sockets, cpu.Cores, cpu.Threads),
			Mem:       vm.Spec.Template.Spec.Domain.Memory.Guest,
			Node:      node,
			VNC:       c.proxyStatus(name, ns),
		}
		// Overlay live VMI facts (actual node, IP, migratability, agent).
		// LiveMigratable reflects REAL viability: KubeVirt's condition AND a
		// same-CPU-vendor target node (live migration can't cross Intel/AMD).
		if vmi, ok := vmis[ns+"/"+name]; ok {
			if vmi.Node != "" {
				v.Node = vmi.Node
			}
			v.IP = vmi.IP
			v.LiveMigratable = vmi.LiveMigratable && hasMigrationTarget(vmi.Node, vendors)
			v.AgentConnected = vmi.AgentConnected
		}
		vms = append(vms, v)
	}
	return vms, nil
}

func totalVCPU(sockets, cores, threads int) int {
	if sockets == 0 {
		sockets = 1
	}
	if cores == 0 {
		cores = 1
	}
	if threads == 0 {
		threads = 1
	}
	return sockets * cores * threads
}

type vmiStatus struct {
	Node           string
	IP             string
	LiveMigratable bool
	AgentConnected bool
}

// vmiStatusIndex returns live per-VMI facts keyed by "namespace/name".
func vmiStatusIndex() map[string]vmiStatus {
	out, err := exec.Command("kubectl", "get", "vmis", "-A", "-o", "json").Output()
	if err != nil {
		return nil
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				NodeName   string `json:"nodeName"`
				Interfaces []struct {
					IPAddress string `json:"ipAddress"`
				} `json:"interfaces"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &res) != nil {
		return nil
	}
	idx := make(map[string]vmiStatus, len(res.Items))
	for _, it := range res.Items {
		s := vmiStatus{Node: it.Status.NodeName}
		if len(it.Status.Interfaces) > 0 {
			s.IP = it.Status.Interfaces[0].IPAddress
		}
		for _, c := range it.Status.Conditions {
			switch c.Type {
			case "LiveMigratable":
				s.LiveMigratable = c.Status == "True"
			case "AgentConnected":
				s.AgentConnected = c.Status == "True"
			}
		}
		idx[it.Metadata.Namespace+"/"+it.Metadata.Name] = s
	}
	return idx
}

func (c *Client) proxyStatus(name, ns string) string {
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

// StartVM starts a KubeVirt VirtualMachine.
func (c *Client) StartVM(name string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}
	cmd := exec.Command(virtctl, "start", name, "-n", c.Namespace)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// StopVM stops a KubeVirt VirtualMachine.
func (c *Client) StopVM(name string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}
	cmd := exec.Command(virtctl, "stop", name, "-n", c.Namespace)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// DeleteVM deletes a KubeVirt VM and its PVCs/DataVolumes/proxy resources.
func (c *Client) DeleteVM(name string) error {
	// Stop first
	virtctl, _ := c.ensureVirtctl()
	if virtctl != "" {
		exec.Command(virtctl, "stop", name, "-n", c.Namespace).Run()
	}

	// Delete VM
	exec.Command("kubectl", "delete", "vm", name, "-n", c.Namespace, "--ignore-not-found").Run()

	// Delete PVCs and DataVolumes
	for _, suffix := range []string{"disk", "data", "iso", "bootc-disk"} {
		pvc := name + "-" + suffix
		exec.Command("kubectl", "delete", "pvc", pvc, "-n", c.Namespace, "--ignore-not-found").Run()
		exec.Command("kubectl", "delete", "datavolume", pvc, "-n", c.Namespace, "--ignore-not-found").Run()
	}

	// Delete hotplug disks and snapshots labeled for this VM
	exec.Command("kubectl", "delete", "pvc", "-n", c.Namespace,
		"-l", "corral.dev/vm="+name, "--ignore-not-found").Run()
	exec.Command("kubectl", "delete", "vmsnapshot", "-n", c.Namespace,
		"-l", "corral.dev/vm="+name, "--ignore-not-found").Run()

	// Delete proxy resources if any
	DeleteProxy(name, c.Namespace)
	return nil
}

// Logs tails the virt-launcher pod logs for a VM.
func (c *Client) Logs(name string) error {
	cmd := exec.Command("kubectl", "logs", "-n", c.Namespace,
		"-l", "vm.kubevirt.io/name="+name, "-c", "compute", "--tail=100", "-f")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// VMInfo returns JSON info about a VM.
func (c *Client) VMInfo(name string) ([]byte, error) {
	return exec.Command("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "json").Output()
}

// SSH opens an SSH session to a VM via virtctl ssh.
func (c *Client) SSH(name, username, identityFile, command string, port int, password string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}

	args := []string{"ssh", "--namespace=" + c.Namespace, "--username=" + username}
	if identityFile != "" {
		args = append(args, "--identity-file="+identityFile)
	}
	if command != "" {
		args = append(args, "--command="+command)
	}
	if port != 22 && port != 0 {
		args = append(args, fmt.Sprintf("--port=%d", port))
	}
	args = append(args,
		"--local-ssh-opts=-o StrictHostKeyChecking=no",
		"--local-ssh-opts=-o UserKnownHostsFile=/dev/null",
	)
	args = append(args, "vm/"+name)

	if password != "" {
		return runWithSSHPass(password, virtctl, args...)
	}

	cmd := exec.Command(virtctl, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runWithSSHPass runs a command with sshpass for password-based auth.
func runWithSSHPass(password, bin string, args ...string) error {
	sshpass, err := exec.LookPath("sshpass")
	if err != nil {
		return fmt.Errorf("sshpass not found (needed for password auth) — install: brew install sshpass")
	}
	allArgs := append([]string{"-p", password, bin}, args...)
	cmd := exec.Command(sshpass, allArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Viewer launches VNC viewer using virtctl proxy + xdg-open.
func (c *Client) Viewer(name string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}

	xdg, _ := exec.LookPath("xdg-open")
	if xdg != "" {
		// Find free port
		port := c.findFreePort()
		proxy := exec.Command(virtctl, "vnc", name, "-n", c.Namespace, "--proxy-only", fmt.Sprintf("--port=%d", port))
		proxy.Stdout = os.Stderr
		proxy.Stderr = os.Stderr
		if err := proxy.Start(); err != nil {
			return err
		}
		// Launch xdg-open
		exec.Command(xdg, fmt.Sprintf("vnc://localhost:%d", port)).Start()
		fmt.Fprintf(os.Stderr, "VNC: vnc://localhost:%d (proxy PID: %d)\n", port, proxy.Process.Pid)
		return proxy.Wait()
	}

	// Fallback: virtctl vnc directly
	cmd := exec.Command(virtctl, "vnc", name, "-n", c.Namespace)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *Client) findFreePort() int {
	for p := 5901; p < 5910; p++ {
		// Simple check — in production use net.Dial
		if !c.portInUse(p) {
			return p
		}
	}
	return 5901
}

func (c *Client) portInUse(port int) bool {
	cmd := exec.Command("sh", "-c", fmt.Sprintf("ss -tln | grep -q ':%d '", port))
	return cmd.Run() == nil
}

func (c *Client) ensureVirtctl() (string, error) {
	path, err := exec.LookPath("virtctl")
	if err != nil {
		return "", fmt.Errorf("virtctl not found — install: brew install kubevirt-cli")
	}
	return path, nil
}

// GenerateVM creates a KubeVirt VirtualMachine manifest.
func GenerateVM(opts types.CreateOpts) map[string]any {
	name := opts.Name
	ns := opts.Namespace
	if ns == "" {
		ns = "default"
	}
	mem := opts.Mem
	if mem == "" {
		mem = "4G"
	}
	cpu := opts.CPU
	if cpu == 0 {
		cpu = 2
	}
	diskSize := opts.Disk
	if diskSize == "" {
		diskSize = "20G"
	}

	volumes := []map[string]any{}
	disks := []map[string]any{}

	hasISO := opts.ISO != ""
	hasContainer := opts.ContainerDisk != ""

	if hasISO {
		// ISO as CD-ROM (bootOrder 1)
		volumes = append(volumes, map[string]any{
			"name":                  "iso",
			"persistentVolumeClaim": map[string]any{"claimName": name + "-iso"},
		})
		disks = append(disks, map[string]any{
			"name":      "iso",
			"cdrom":     map[string]any{"bus": "sata"},
			"bootOrder": 1,
		})
		volumes = append(volumes, map[string]any{
			"name":                  "rootdisk",
			"persistentVolumeClaim": map[string]any{"claimName": name + "-disk"},
		})
		disks = append(disks, map[string]any{
			"name":      "rootdisk",
			"disk":      map[string]any{"bus": "virtio"},
			"bootOrder": 2,
		})
	} else if hasContainer {
		volumes = append(volumes, map[string]any{
			"name":          "containerdisk",
			"containerDisk": map[string]any{"image": opts.ContainerDisk},
		})
		disks = append(disks, map[string]any{
			"name": "containerdisk",
			"disk": map[string]any{"bus": "virtio"},
		})
		// Extra persistent data disk only when --disk is explicitly requested;
		// CreateVM creates the matching PVC under the same condition.
		if opts.Disk != "" {
			volumes = append(volumes, map[string]any{
				"name":                  "datadisk",
				"persistentVolumeClaim": map[string]any{"claimName": name + "-data"},
			})
			disks = append(disks, map[string]any{
				"name": "datadisk",
				"disk": map[string]any{"bus": "virtio"},
			})
		}
	} else if opts.PVC != "" {
		volumes = append(volumes, map[string]any{
			"name":                  "rootdisk",
			"persistentVolumeClaim": map[string]any{"claimName": opts.PVC},
		})
		disks = append(disks, map[string]any{
			"name": "rootdisk",
			"disk": map[string]any{"bus": "virtio"},
		})
	} else {
		volumes = append(volumes, map[string]any{
			"name":                  "rootdisk",
			"persistentVolumeClaim": map[string]any{"claimName": name + "-disk"},
		})
		disks = append(disks, map[string]any{
			"name": "rootdisk",
			"disk": map[string]any{"bus": "virtio"},
		})
	}

	// Cloud-init
	pwd := opts.CloudInitPassword
	if pwd == "" {
		pwd = randomPassword()
	}
	LastPassword = pwd

	userData := fmt.Sprintf("#cloud-config\npassword: %s\nchpasswd:\n  expire: False\nssh_pwauth: true\n", pwd)
	if opts.SSHPublicKey != "" {
		userData += fmt.Sprintf("ssh_authorized_keys:\n  - %s\n", opts.SSHPublicKey)
	}
	// Join the tailnet on first boot. Skipped when the extra cloud-init
	// already declares runcmd — two runcmd keys would be invalid YAML.
	if opts.TailscaleAuthKey != "" && !strings.Contains(opts.CloudInitExtra, "runcmd:") {
		userData += fmt.Sprintf(`runcmd:
  - ['sh', '-c', 'command -v tailscale >/dev/null 2>&1 || curl -fsSL https://tailscale.com/install.sh | sh']
  - ['tailscale', 'up', '--auth-key=%s', '--hostname=%s', '--ssh']
`, opts.TailscaleAuthKey, name)
	}
	if opts.CloudInitExtra != "" {
		userData += opts.CloudInitExtra
	}
	volumes = append(volumes, map[string]any{
		"name": "cloudinitdisk",
		"cloudInitNoCloud": map[string]any{
			"userData": userData,
		},
	})
	disks = append(disks, map[string]any{
		"name": "cloudinitdisk",
		"disk": map[string]any{"bus": "virtio"},
	})

	memMib := parseMem(mem)

	domain := map[string]any{
		"devices": map[string]any{
			"disks": disks,
			// masquerade (NAT) binding is required for live migration;
			// the default bridge binding pins the VM to its node.
			"interfaces": []map[string]any{
				{"name": "default", "masquerade": map[string]any{}},
			},
		},
	}
	// An instancetype supplies CPU/memory (and hotplug headroom); only set the
	// domain cpu/memory when not using one.
	if opts.InstanceType == "" {
		domain["cpu"] = cpuSpec(cpu)
		domain["memory"] = memSpec(memMib)
	}

	spec := map[string]any{
		"running": false,
		"template": map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{"kubevirt.io/vm": name},
			},
			"spec": map[string]any{
				"domain": domain,
				"networks": []map[string]any{
					{"name": "default", "pod": map[string]any{}},
				},
				"volumes": volumes,
			},
		},
	}

	if opts.InstanceType != "" {
		spec["instancetype"] = map[string]any{
			"kind": "VirtualMachineClusterInstancetype",
			"name": opts.InstanceType,
		}
	}
	if opts.Preference != "" {
		spec["preference"] = map[string]any{
			"kind": "VirtualMachineClusterPreference",
			"name": opts.Preference,
		}
	}

	if opts.Node != "" {
		spec["template"].(map[string]any)["spec"].(map[string]any)["nodeSelector"] = map[string]any{
			"kubernetes.io/hostname": opts.Node,
		}
	}

	return map[string]any{
		"apiVersion": "kubevirt.io/v1",
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels":    map[string]any{"tailvm": name},
		},
		"spec": spec,
	}
}

// GeneratePVC creates a PersistentVolumeClaim manifest.
func GeneratePVC(name, namespace, size string) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"accessModes": []string{"ReadWriteOnce"},
			"resources": map[string]any{
				"requests": map[string]any{"storage": size},
			},
		},
	}
}

// GenerateDataVolume creates a CDI DataVolume to import an ISO from URL.
func GenerateDataVolume(name, namespace, isoURL string) map[string]any {
	return map[string]any{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "DataVolume",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"source": map[string]any{
				"http": map[string]any{"url": isoURL},
			},
			"pvc": map[string]any{
				"accessModes": []string{"ReadWriteOnce"},
				"resources": map[string]any{
					"requests": map[string]any{"storage": "6Gi"},
				},
			},
		},
	}
}

// GenerateProxyService creates the unified proxy Service with Tailscale annotation.
func GenerateProxyService(name, namespace string, ports []int) map[string]any {
	svcPorts := []map[string]any{}
	for _, p := range ports {
		svcPorts = append(svcPorts, map[string]any{
			"port":       p,
			"targetPort": p,
			"name":       fmt.Sprintf("port-%d", p),
			"protocol":   "TCP",
		})
	}

	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":      name + "-proxy",
			"namespace": namespace,
			"annotations": map[string]string{
				"tailscale.com/expose":   "true",
				"tailscale.com/hostname": name + "-vm",
			},
		},
		"spec": map[string]any{
			"type": "ClusterIP",
			"selector": map[string]string{
				"app": "tailvm-proxy",
				"vm":  name,
			},
			"ports": svcPorts,
		},
	}
}

// GenerateProxyDeployment creates the proxy Deployment that forwards all ports.
func GenerateProxyDeployment(name, namespace string, ports []int) map[string]any {
	containerPorts := []map[string]any{}
	for _, p := range ports {
		containerPorts = append(containerPorts, map[string]any{
			"containerPort": p,
			"name":          fmt.Sprintf("port-%d", p),
			"protocol":      "TCP",
		})
	}

	hasVNC := false
	for _, p := range ports {
		if p == 5900 {
			hasVNC = true
			break
		}
	}

	// Build shell script
	var script strings.Builder
	script.WriteString("apk add --no-cache curl socat jq >/dev/null 2>&1\n")
	if hasVNC {
		script.WriteString(fmt.Sprintf("/tmp/virtctl vnc %s -n %s --proxy-only --address=0.0.0.0 --port=5900 &\n", name, namespace))
	}

	script.WriteString(fmt.Sprintf(`while true; do
  IP=$(curl -sS --cacert /var/run/secrets/kubernetes.io/serviceaccount/ca.crt \
       -H "Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" \
       "https://kubernetes.default.svc/apis/kubevirt.io/v1/namespaces/%s/virtualmachineinstances/%s" | \
       jq -r '.status.interfaces[0].ipAddress // empty' 2>/dev/null)
  if [ -n "$IP" ]; then
`, namespace, name))

	for _, p := range ports {
		if p == 5900 {
			continue // VNC handled above
		}
		script.WriteString(fmt.Sprintf("    socat TCP-LISTEN:%d,fork,reuseaddr TCP:$IP:%d &\n", p, p))
	}
	script.WriteString("    wait\n  fi\n  sleep 5\ndone\n")

	// virtctl is only needed for the VNC proxy
	var initContainers []map[string]any
	if hasVNC {
		initContainers = append(initContainers, map[string]any{
			"name":  "install-tools",
			"image": "alpine:latest",
			"securityContext": map[string]any{
				"allowPrivilegeEscalation": false,
				"capabilities":             map[string]any{"drop": []string{"ALL"}},
				"seccompProfile":           map[string]any{"type": "RuntimeDefault"},
			},
			"command": []string{"sh", "-c", "apk add --no-cache curl >/dev/null 2>&1\ncurl -sSL \"https://github.com/kubevirt/kubevirt/releases/download/v1.8.2/virtctl-v1.8.2-linux-amd64\" -o /tmp/virtctl\nchmod +x /tmp/virtctl"},
			"volumeMounts": []map[string]any{
				{"name": "tools", "mountPath": "/tmp"},
			},
		})
	}

	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      name + "-proxy",
			"namespace": namespace,
			"labels": map[string]string{
				"app": "tailvm-proxy",
				"vm":  name,
			},
		},
		"spec": map[string]any{
			"replicas": 1,
			"selector": map[string]any{
				"matchLabels": map[string]string{
					"app": "tailvm-proxy",
					"vm":  name,
				},
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]string{
						"app": "tailvm-proxy",
						"vm":  name,
					},
				},
				"spec": map[string]any{
					"serviceAccountName": "tailvm-" + name + "-proxy",
					"securityContext": map[string]any{
						"seccompProfile": map[string]any{"type": "RuntimeDefault"},
					},
					"initContainers": initContainers,
					"containers": []map[string]any{
						{
							"name":  "proxy",
							"image": "alpine:latest",
							"securityContext": map[string]any{
								"allowPrivilegeEscalation": false,
								"capabilities":             map[string]any{"drop": []string{"ALL"}},
								"seccompProfile":           map[string]any{"type": "RuntimeDefault"},
							},
							"command": []string{"sh", "-c", script.String()},
							"ports":   containerPorts,
							"volumeMounts": []map[string]any{
								{"name": "tools", "mountPath": "/tmp", "readOnly": true},
							},
						},
					},
					"volumes": []map[string]any{
						{"name": "tools", "emptyDir": map[string]any{}},
					},
				},
			},
		},
	}
}

// ExposedPorts returns the currently exposed proxy ports for a VM.
func ExposedPorts(name, ns string) []int {
	out, err := exec.Command("kubectl", "get", "svc", name+"-proxy", "-n", ns, "-o", "json").Output()
	if err != nil {
		return nil
	}
	var svc struct {
		Spec struct {
			Ports []struct {
				Port int `json:"port"`
			} `json:"ports"`
		} `json:"spec"`
	}
	if json.Unmarshal(out, &svc) != nil {
		return nil
	}
	var ports []int
	for _, p := range svc.Spec.Ports {
		ports = append(ports, p.Port)
	}
	return ports
}

// ApplyProxy creates/updates the proxy resources for a VM.
func ApplyProxy(name, ns string, ports []int) error {
	// RBAC
	rbac := GenerateProxyRBAC(name, ns)
	if err := applyManifest(rbac); err != nil {
		return fmt.Errorf("proxy RBAC: %w", err)
	}
	// Service
	svc := GenerateProxyService(name, ns, ports)
	svcData, _ := json.Marshal(svc)
	if err := applyManifest(string(svcData)); err != nil {
		return fmt.Errorf("proxy service: %w", err)
	}
	// Deployment
	deploy := GenerateProxyDeployment(name, ns, ports)
	deployData, _ := json.Marshal(deploy)
	if err := applyManifest(string(deployData)); err != nil {
		return fmt.Errorf("proxy deployment: %w", err)
	}
	return nil
}

// DeleteProxy removes all proxy resources for a VM.
func DeleteProxy(name, ns string) error {
	for _, kind := range []string{"deploy", "svc", "sa", "role", "rolebinding"} {
		rname := name + "-proxy"
		if kind != "deploy" && kind != "svc" {
			rname = "tailvm-" + name + "-proxy"
		}
		exec.Command("kubectl", "delete", kind, rname, "-n", ns, "--ignore-not-found").Run()
	}
	return nil
}

// GenerateProxyRBAC creates RBAC resources for the proxy.
func GenerateProxyRBAC(name, ns string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: tailvm-%s-proxy
  namespace: %s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: tailvm-%s-proxy
  namespace: %s
rules:
  - apiGroups: ["subresources.kubevirt.io"]
    resources: ["virtualmachineinstances/vnc"]
    verbs: ["get"]
  - apiGroups: ["kubevirt.io"]
    resources: ["virtualmachineinstances"]
    verbs: ["get"]
  - apiGroups: ["kubevirt.io"]
    resources: ["virtualmachineinstances/portforward"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: tailvm-%s-proxy
  namespace: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: tailvm-%s-proxy
subjects:
  - kind: ServiceAccount
    name: tailvm-%s-proxy
`, name, ns, name, ns, name, ns, name, name)
}

// Apply marshals a manifest object and pipes it to kubectl apply.
func Apply(obj map[string]any) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return applyManifest(string(data))
}

// CreateVM provisions the namespace, disks, and VirtualMachine described by opts.
// Used by both the CLI create command and the web UI.
func CreateVM(opts types.CreateOpts) error {
	ns := opts.Namespace
	if ns == "" {
		ns = DefaultNamespace
		opts.Namespace = ns
	}
	EnsureNamespace(ns)

	name := opts.Name
	hasISO := opts.ISO != ""
	hasContainer := opts.ContainerDisk != ""
	hasPVC := opts.PVC != ""
	diskSize := opts.Disk
	if diskSize == "" {
		diskSize = "20G"
	}
	sc := PreferredStorageClass()

	if hasISO {
		if err := Apply(GenerateDataVolume(name+"-iso", ns, opts.ISO)); err != nil {
			return fmt.Errorf("creating ISO DataVolume: %w", err)
		}
		fmt.Fprintf(os.Stderr, "ISO DataVolume: %s-iso (importing from %s)\n", name, opts.ISO)
		if err := Apply(GeneratePVCWithClass(name+"-disk", ns, diskSize, sc)); err != nil {
			return fmt.Errorf("creating boot PVC: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Boot PVC: %s-disk (%s)\n", name, diskSize)
	} else if hasContainer && opts.Disk != "" {
		if err := Apply(GeneratePVCWithClass(name+"-data", ns, opts.Disk, sc)); err != nil {
			return fmt.Errorf("creating data PVC: %w", err)
		}
	} else if !hasPVC && !hasContainer {
		if err := Apply(GeneratePVCWithClass(name+"-disk", ns, diskSize, sc)); err != nil {
			return fmt.Errorf("creating boot PVC: %w", err)
		}
	}

	if err := Apply(GenerateVM(opts)); err != nil {
		return fmt.Errorf("creating VM: %w", err)
	}
	return nil
}

func applyManifest(yaml string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// cpuSpec builds a hotplug-ready CPU topology: one core/thread per socket so
// that live CPU updates (which scale `sockets`) map 1:1 to vCPUs, with
// maxSockets headroom for hotplug. Harmless without LiveUpdate enabled.
func cpuSpec(cpu int) map[string]any {
	if cpu < 1 {
		cpu = 1
	}
	max := cpu * 4
	if max < 4 {
		max = 4
	}
	return map[string]any{
		"sockets":    cpu,
		"cores":      1,
		"threads":    1,
		"maxSockets": max,
	}
}

// memSpec sets guest memory plus maxGuest headroom so memory can be hotplugged
// live (up to 4× the initial size). Harmless without LiveUpdate enabled.
func memSpec(memMib int) map[string]any {
	if memMib < 1 {
		memMib = 1
	}
	return map[string]any{
		"guest":    fmt.Sprintf("%dMi", memMib),
		"maxGuest": fmt.Sprintf("%dMi", memMib*4),
	}
}

func parseMem(s string) int {
	upper := strings.ToUpper(s)
	var val int
	if strings.HasSuffix(upper, "G") {
		fmt.Sscanf(s, "%d", &val)
		return val * 1024
	}
	if strings.HasSuffix(upper, "M") {
		fmt.Sscanf(s, "%d", &val)
		return val
	}
	// Raw number → treat as MiB
	fmt.Sscanf(s, "%d", &val)
	return val
}

func randomPassword() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}
