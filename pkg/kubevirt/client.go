package kubevirt

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"strings"

	"github.com/hanthor/tailvm-go/pkg/types"
)

// Client interacts with the Kubernetes cluster via kubectl.
type Client struct {
	Namespace string
}

// NewClient creates a KubeVirt client.
func NewClient(ns string) *Client {
	if ns == "" {
		ns = "default"
	}
	return &Client{Namespace: ns}
}

// VMExists checks if a VirtualMachine exists in the cluster.
func (c *Client) VMExists(name string) bool {
	cmd := exec.Command("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "name")
	return cmd.Run() == nil
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
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, err
	}

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
		}

		vms = append(vms, types.VM{
			Name:      name,
			Backend:   "kubevirt",
			Namespace: ns,
			Status:    status,
			Ready:     vm.Status.Ready,
			Running:   running,
			CPU:       vm.Spec.Template.Spec.Domain.CPU.Cores,
			Mem:       vm.Spec.Template.Spec.Domain.Memory.Guest,
			Node:      node,
			VNC:       c.proxyStatus(name, ns),
		})
	}
	return vms, nil
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

// DeleteVM deletes a KubeVirt VM and its PVCs/DataVolumes.
func (c *Client) DeleteVM(name string) error {
	// Stop first
	virtctl, _ := c.ensureVirtctl()
	if virtctl != "" {
		exec.Command(virtctl, "stop", name, "-n", c.Namespace).Run()
	}

	// Delete VM
	exec.Command("kubectl", "delete", "vm", name, "-n", c.Namespace, "--ignore-not-found").Run()

	// Delete PVCs and DataVolumes
	for _, suffix := range []string{"disk", "data", "iso"} {
		pvc := name + "-" + suffix
		exec.Command("kubectl", "delete", "pvc", pvc, "-n", c.Namespace, "--ignore-not-found").Run()
		exec.Command("kubectl", "delete", "datavolume", pvc, "-n", c.Namespace, "--ignore-not-found").Run()
	}
	return nil
}

// VMInfo returns JSON info about a VM.
func (c *Client) VMInfo(name string) ([]byte, error) {
	return exec.Command("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "json").Output()
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
			"name": "iso",
			"persistentVolumeClaim": map[string]any{"claimName": name + "-iso"},
		})
		disks = append(disks, map[string]any{
			"name":      "iso",
			"cdrom":     map[string]any{"bus": "sata"},
			"bootOrder": 1,
		})
		volumes = append(volumes, map[string]any{
			"name": "rootdisk",
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
		if diskSize != "" {
			volumes = append(volumes, map[string]any{
				"name": "datadisk",
				"persistentVolumeClaim": map[string]any{"claimName": name + "-data"},
			})
			disks = append(disks, map[string]any{
				"name": "datadisk",
				"disk": map[string]any{"bus": "virtio"},
			})
		}
	} else if opts.PVC != "" {
		volumes = append(volumes, map[string]any{
			"name": "rootdisk",
			"persistentVolumeClaim": map[string]any{"claimName": opts.PVC},
		})
		disks = append(disks, map[string]any{
			"name": "rootdisk",
			"disk": map[string]any{"bus": "virtio"},
		})
	} else {
		volumes = append(volumes, map[string]any{
			"name": "rootdisk",
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
	userData := fmt.Sprintf("#cloud-config\npassword: %s\nchpasswd:\n  expire: False\nssh_pwauth: true\n", pwd)
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

	spec := map[string]any{
		"running": false,
		"template": map[string]any{
			"metadata": map[string]any{
				"labels": map[string]any{"kubevirt.io/vm": name},
			},
			"spec": map[string]any{
				"domain": map[string]any{
					"cpu":    map[string]any{"cores": cpu},
					"memory": map[string]any{"guest": fmt.Sprintf("%dMi", memMib)},
					"devices": map[string]any{
						"disks": disks,
					},
				},
				"volumes": volumes,
			},
		},
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
			"port":     p,
			"targetPort": p,
			"name":     fmt.Sprintf("port-%d", p),
			"protocol": "TCP",
		})
	}

	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata": map[string]any{
			"name":        name + "-proxy",
			"namespace":   namespace,
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

	// Build shell script
	var script strings.Builder
	script.WriteString("apk add --no-cache curl socat >/dev/null 2>&1\n")

	hasVNC := false
	for _, p := range ports {
		if p == 5900 {
			hasVNC = true
			break
		}
	}
	if hasVNC {
		script.WriteString(fmt.Sprintf("/tmp/virtctl vnc %s -n %s --proxy-only --address=0.0.0.0 --port=5900 &\n", name, namespace))
	}

	script.WriteString(fmt.Sprintf(`while true; do
  IP=$(curl -sS --cacert /var/run/secrets/kubernetes.io/serviceaccount/ca.crt \
       -H "Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" \
       "https://kubernetes.default.svc/apis/kubevirt.io/v1/namespaces/%s/virtualmachineinstances/%s" | \
       python3 -c "import sys,json; print(json.load(sys.stdin).get('status',{}).get('interfaces',[{}])[0].get('ipAddress',''))" 2>/dev/null)
  if [ -n "$IP" ]; then
`, namespace, name))

	for _, p := range ports {
		if p == 5900 {
			continue // VNC handled above
		}
		script.WriteString(fmt.Sprintf("    socat TCP-LISTEN:%d,fork,reuseaddr TCP:$IP:%d &\n", p, p))
	}
	script.WriteString("    wait\n  fi\n  sleep 5\ndone\n")

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
					"initContainers": []map[string]any{
						{
							"name":  "install-tools",
							"image": "alpine:latest",
							"securityContext": map[string]any{
								"allowPrivilegeEscalation": false,
								"capabilities":            map[string]any{"drop": []string{"ALL"}},
								"seccompProfile":          map[string]any{"type": "RuntimeDefault"},
							},
							"command": []string{"sh", "-c", "apk add --no-cache curl socat >/dev/null 2>&1\ncurl -sSL \"https://github.com/kubevirt/kubevirt/releases/download/v1.8.2/virtctl-v1.8.2-linux-amd64\" -o /tmp/virtctl\nchmod +x /tmp/virtctl"},
							"volumeMounts": []map[string]any{
								{"name": "tools", "mountPath": "/tmp"},
							},
						},
					},
					"containers": []map[string]any{
						{
							"name":  "proxy",
							"image": "alpine:latest",
							"securityContext": map[string]any{
								"allowPrivilegeEscalation": false,
								"capabilities":            map[string]any{"drop": []string{"ALL"}},
								"seccompProfile":          map[string]any{"type": "RuntimeDefault"},
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
