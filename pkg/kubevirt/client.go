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
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hanthor/corral/pkg/shell"
	"github.com/hanthor/corral/pkg/types"
)

// LastPassword holds the cloud-init password from the most recent GenerateVM call.
var LastPassword string

// mergeCloudInit combines the generated #cloud-config user-data with the
// user-supplied extra YAML. A raw string append produces duplicate top-level
// keys (e.g. two ssh_authorized_keys:) — invalid cloud-config that cloud-init
// mishandles silently. Lists merge by concatenation (generated entries first),
// maps merge per key, scalars are replaced by the extra's value. Extra that
// isn't parseable YAML falls back to the old append.
func mergeCloudInit(base, extra string) string {
	var b, e map[string]any
	if yaml.Unmarshal([]byte(base), &b) != nil || b == nil {
		return base + extra
	}
	if yaml.Unmarshal([]byte(extra), &e) != nil || e == nil {
		return base + extra
	}
	for k, v := range e {
		if bl, ok := b[k].([]any); ok {
			if el, ok := v.([]any); ok {
				b[k] = append(bl, el...)
				continue
			}
		}
		if bm, ok := b[k].(map[string]any); ok {
			if em, ok := v.(map[string]any); ok {
				for mk, mv := range em {
					bm[mk] = mv
				}
				continue
			}
		}
		b[k] = v
	}
	out, err := yaml.Marshal(b)
	if err != nil {
		return base + extra
	}
	return "#cloud-config\n" + string(out)
}

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
	Runner    shell.Runner // injected for tests; defaults to shell.Real
}

func (c *Client) runner() shell.Runner {
	if c.Runner != nil {
		return c.Runner
	}
	if defaultClientRunner != nil {
		return defaultClientRunner
	}
	return shell.Real{}
}

// defaultClientRunner is a package-level runner override for tests.
// Set it to intercept all client calls without modifying each NewClient call site.
var defaultClientRunner shell.Runner

// SetDefaultRunner overrides the runner for all kubevirt clients (for unit tests).
func SetDefaultRunner(r shell.Runner) { defaultClientRunner = r }

// DefaultNamespace is the default namespace for KubeVirt VMs. Override with
// CORRAL_NAMESPACE (fallback for existing deployments that predate the rename).
var DefaultNamespace = defaultNamespace()

func defaultNamespace() string {
	if ns := os.Getenv("CORRAL_NAMESPACE"); ns != "" {
		return ns
	}
	return "corral-vms"
}

// EnsureNamespace creates the namespace if it doesn't exist and labels it
// for privileged pods (needed by the bootc builder Job).
func EnsureNamespace(ns string) {
	if ns == "" {
		ns = DefaultNamespace
	}
	runPkg("kubectl", "create", "ns", ns) // no-op if it exists
	runPkg("kubectl", "label", "ns", ns,
		"pod-security.kubernetes.io/enforce=privileged", "--overwrite")
}

// NewClient creates a KubeVirt client using the real os/exec runner.
func NewClient(ns string) *Client {
	if ns == "" {
		ns = DefaultNamespace
	}
	return &Client{Namespace: ns}
}

// NewClientWithRunner creates a KubeVirt client with a custom Runner (for tests).
func NewClientWithRunner(ns string, r shell.Runner) *Client {
	c := NewClient(ns)
	c.Runner = r
	return c
}

// VMExists checks if a VirtualMachine exists in the cluster.
func (c *Client) VMExists(name string) bool {
	_, err := c.runner().Run("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "name")
	return err == nil
}

// DataVolumeStatus returns the import progress for a VM's ISO DataVolume.
func DataVolumeStatus(name, ns string) string {
	out, err := runPkg("kubectl", "get", "datavolume", name+"-iso", "-n", ns, "-o", "json")
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
	out, err := c.runner().Run("kubectl", "get", "vms", "-A", "-o", "json")
	if err != nil {
		return nil, err
	}
	li := launcherRunningIndex()
	launcherRunning := func(name, ns string) bool { return li[ns+"/"+name] }
	return parseVMList(out, vmiStatusIndex(), nodeVendors(), c.proxyStatus, DataVolumeStatus, launcherRunning)
}

// parseVMList turns `kubectl get vms -o json` output into []types.VM. Pure
// except for the injected helpers (proxy status, ISO import status, launcher
// running), so the state-derivation logic is unit-testable. Keep it free of
// exec/IO.
//
// launcherRunningFn reports whether a VM's virt-launcher pod is Running. It's
// the truth source for kernel-boot (bootc) VMs, whose VMI status — phase
// included — freezes on KubeVirt versions where the kernelBootStatus checksum
// (a uint32) trips the CRD's int32 validation, so printableStatus is stuck.
// See docs note in HANDOFF; pass a func returning false to disable.
func parseVMList(out []byte, vmis map[string]vmiStatus, vendors map[string]string,
	proxyStatusFn, isoStatusFn func(name, ns string) string,
	launcherRunningFn func(name, ns string) bool) ([]types.VM, error) {
	var result struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
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
							Memory   struct{ Guest string } `json:"memory"`
							Firmware struct {
								KernelBoot *json.RawMessage `json:"kernelBoot"`
							} `json:"firmware"`
						} `json:"domain"`
						NodeSelector map[string]string `json:"nodeSelector"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				Ready           bool   `json:"ready"`
				PrintableStatus string `json:"printableStatus"`
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
		// Derive state from KubeVirt's authoritative printableStatus — spec.running
		// is empty on VMs that use spec.runStrategy (the newer field).
		ps := vm.Status.PrintableStatus
		running := ps == "Running" || ps == "Paused" || ps == "Migrating" || ps == "Stopping"
		node := "—"
		if n, ok := vm.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"]; ok {
			node = n
		}

		// Kernel-boot (bootc) rescue: a frozen VMI status leaves printableStatus
		// stuck at a transitional value even though the guest is up. Trust the
		// launcher pod instead. Only overrides transitional states — never a
		// genuine Stopped (the VM controller cleans up the pod on stop).
		kernelBoot := vm.Spec.Template.Spec.Domain.Firmware.KernelBoot != nil
		transitional := ps == "Starting" || ps == "Scheduling" || ps == "Scheduled" || ps == "Provisioning" || ps == ""
		if !running && kernelBoot && transitional && launcherRunningFn != nil && launcherRunningFn(name, ns) {
			running = true
			ps = "Running"
		}

		status := statusLabel(ps)
		if !running && !vm.Status.Ready {
			if iso := isoStatusFn(name, ns); iso != "" && iso != "✓ ready" {
				status = iso
			}
		}

		cpu := vm.Spec.Template.Spec.Domain.CPU
		v := types.VM{
			Name:       name,
			Backend:    "kubevirt",
			Namespace:  ns,
			Status:     status,
			Ready:      vm.Status.Ready,
			Running:    running,
			CPU:        totalVCPU(cpu.Sockets, cpu.Cores, cpu.Threads),
			Mem:        vm.Spec.Template.Spec.Domain.Memory.Guest,
			Node:       node,
			VNC:        proxyStatusFn(name, ns),
			IsTemplate: vm.Metadata.Labels["corral.dev/template"] == "true",
			Bootc:      kernelBoot,
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

// statusLabel maps KubeVirt's printableStatus to a Corral status string.
func statusLabel(ps string) string {
	switch ps {
	case "Running":
		return "● Running"
	case "Paused":
		return "⏸ Paused"
	case "Migrating":
		return "⇄ Migrating"
	case "Starting", "Provisioning", "WaitingForVolumeBinding":
		return "◐ " + ps
	case "Stopping", "Terminating":
		return "◌ " + ps
	case "Stopped", "":
		return "○ Stopped"
	default:
		return "○ " + ps
	}
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
// launcherRunningIndex maps "ns/vm" → true when the VM's virt-launcher pod is
// Running with its compute container ready. Used to rescue kernel-boot VMs
// whose VMI status is frozen (see parseVMList). One list call per refresh.
func launcherRunningIndex() map[string]bool {
	out, err := runPkg("kubectl", "get", "pods", "-A",
		"-l", "kubevirt.io=virt-launcher", "-o", "json")
	if err != nil {
		return nil
	}
	var res struct {
		Items []struct {
			Metadata struct {
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					Name  string `json:"name"`
					Ready bool   `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &res) != nil {
		return nil
	}
	idx := map[string]bool{}
	for _, p := range res.Items {
		vm := p.Metadata.Labels["vm.kubevirt.io/name"]
		if vm == "" || p.Status.Phase != "Running" {
			continue
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name == "compute" && cs.Ready {
				idx[p.Metadata.Namespace+"/"+vm] = true
			}
		}
	}
	return idx
}

func vmiStatusIndex() map[string]vmiStatus {
	out, err := runPkg("kubectl", "get", "vmis", "-A", "-o", "json")
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
	_, err = c.runner().Run(virtctl, "start", name, "-n", c.Namespace)
	return err
}

// StopVM stops a KubeVirt VirtualMachine.
func (c *Client) StopVM(name string) error {
	virtctl, err := c.ensureVirtctl()
	if err != nil {
		return err
	}
	_, err = c.runner().Run(virtctl, "stop", name, "-n", c.Namespace)
	return err
}

// DeleteVM deletes a KubeVirt VM and its PVCs/DataVolumes/proxy resources.
func (c *Client) DeleteVM(name string) error {
	// Stop first
	virtctl, _ := c.ensureVirtctl()
	if virtctl != "" {
		c.runner().Run(virtctl, "stop", name, "-n", c.Namespace)
	}

	// Delete VM
	c.runner().Run("kubectl", "delete", "vm", name, "-n", c.Namespace, "--ignore-not-found")

	// Delete PVCs and DataVolumes
	for _, suffix := range []string{"disk", "data", "iso", "bootc-disk"} {
		pvc := name + "-" + suffix
		c.runner().Run("kubectl", "delete", "pvc", pvc, "-n", c.Namespace, "--ignore-not-found")
		c.runner().Run("kubectl", "delete", "datavolume", pvc, "-n", c.Namespace, "--ignore-not-found")
	}

	// Delete hotplug disks and snapshots labeled for this VM
	c.runner().Run("kubectl", "delete", "pvc", "-n", c.Namespace,
		"-l", "corral.dev/vm="+name, "--ignore-not-found")
	c.runner().Run("kubectl", "delete", "vmsnapshot", "-n", c.Namespace,
		"-l", "corral.dev/vm="+name, "--ignore-not-found")

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
	return c.runner().Run("kubectl", "get", "vm", name, "-n", c.Namespace, "-o", "json")
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
	path, err := c.runner().LookPath("virtctl")
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
		// May hold several keys (e.g. resolved from a GitHub account),
		// newline-separated — each becomes its own list entry.
		userData += "ssh_authorized_keys:\n"
		for _, k := range strings.Split(opts.SSHPublicKey, "\n") {
			if k = strings.TrimSpace(k); k != "" {
				userData += fmt.Sprintf("  - %s\n", k)
			}
		}
	}
	// Tailnet access is provided by the Tailscale operator proxy (ApplyProxy),
	// not by joining from inside the guest — so no in-guest tailscale here.
	if opts.CloudInitExtra != "" {
		userData = mergeCloudInit(userData, opts.CloudInitExtra)
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
			"labels":    map[string]any{"corral": name},
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

// GenerateBootDataVolume creates a CDI DataVolume that imports a qcow2/raw disk
// image from a URL into a bootable PVC (sized, optional StorageClass).
func GenerateBootDataVolume(name, namespace, url, size, storageClass string) map[string]any {
	if size == "" {
		size = "20G"
	}
	dv := GenerateDataVolume(name, namespace, url)
	pvc := dv["spec"].(map[string]any)["pvc"].(map[string]any)
	pvc["resources"].(map[string]any)["requests"].(map[string]any)["storage"] = size
	if storageClass != "" {
		pvc["storageClassName"] = storageClass
	}
	return dv
}

// ProxyTags, when set, tags exposed VM devices on the tailnet
// (tailscale.com/tags annotation), e.g. "tag:corral-vm".
var ProxyTags string

func proxyAnnotations(name string) map[string]string {
	a := map[string]string{
		"tailscale.com/expose":   "true",
		"tailscale.com/hostname": name + "-vm",
	}
	if ProxyTags != "" {
		a["tailscale.com/tags"] = ProxyTags
	}
	return a
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
			"name":        name + "-proxy",
			"namespace":   namespace,
			"annotations": proxyAnnotations(name),
		},
		"spec": map[string]any{
			"type": "ClusterIP",
			"selector": map[string]string{
				"app": "corral-proxy",
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
				"app": "corral-proxy",
				"vm":  name,
			},
		},
		"spec": map[string]any{
			"replicas": 1,
			"selector": map[string]any{
				"matchLabels": map[string]string{
					"app": "corral-proxy",
					"vm":  name,
				},
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]string{
						"app": "corral-proxy",
						"vm":  name,
					},
				},
				"spec": map[string]any{
					"serviceAccountName": "corral-" + name + "-proxy",
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
	out, err := runPkg("kubectl", "get", "svc", name+"-proxy", "-n", ns, "-o", "json")
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

// applyProxyManifest applies one proxy manifest with retries. The proxy Role
// grants only vmi get + vnc get (both held by corral-web, so the RBAC
// privilege-escalation check passes); retries cover transient apiserver/webhook
// blips during the post-build churn.
func applyProxyManifest(label, manifest string) error {
	var err error
	for attempt := 0; attempt < 12; attempt++ {
		if err = applyManifest(manifest); err == nil {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("proxy %s: %w", label, err)
}

// ApplyProxy creates/updates the proxy resources for a VM.
func ApplyProxy(name, ns string, ports []int) error {
	if err := applyProxyManifest("RBAC", GenerateProxyRBAC(name, ns)); err != nil {
		return err
	}
	svc, _ := json.Marshal(GenerateProxyService(name, ns, ports))
	if err := applyProxyManifest("service", string(svc)); err != nil {
		return err
	}
	deploy, _ := json.Marshal(GenerateProxyDeployment(name, ns, ports))
	if err := applyProxyManifest("deployment", string(deploy)); err != nil {
		return err
	}
	return nil
}

// DeleteProxy removes all proxy resources for a VM.
func DeleteProxy(name, ns string) error {
	for _, kind := range []string{"deploy", "svc", "sa", "role", "rolebinding"} {
		rname := name + "-proxy"
		if kind != "deploy" && kind != "svc" {
			rname = "corral-" + name + "-proxy"
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
  name: corral-%s-proxy
  namespace: %s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: corral-%s-proxy
  namespace: %s
rules:
  - apiGroups: ["subresources.kubevirt.io"]
    resources: ["virtualmachineinstances/vnc"]
    verbs: ["get"]
  - apiGroups: ["kubevirt.io"]
    resources: ["virtualmachineinstances"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: corral-%s-proxy
  namespace: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: corral-%s-proxy
subjects:
  - kind: ServiceAccount
    name: corral-%s-proxy
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
	hasImport := opts.ImportURL != ""
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
	} else if hasImport {
		// CDI imports the qcow2/raw image straight into the boot disk PVC.
		if err := Apply(GenerateBootDataVolume(name+"-disk", ns, opts.ImportURL, diskSize, sc)); err != nil {
			return fmt.Errorf("creating import DataVolume: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Importing %s → %s-disk (%s)\n", opts.ImportURL, name, diskSize)
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

// applyManifest pipes a YAML manifest string to kubectl apply.
func applyManifest(yaml string) error {
	out, err := getApplyRunner().RunStdin(yaml, "kubectl", "apply", "-f", "-")
	if err != nil {
		// RunStdin returns combined stdout+stderr; surface it so callers don't
		// just see "exit status 1".
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

// The package shell-out runners are swapped by tests (SetApplyRunner /
// SetPackageRunner) while background goroutines (e.g. async ApplyProxy from a
// create handler) read them, so guard access with a mutex — otherwise `go test
// -race` flags the read/write.
var (
	runnerMu             sync.RWMutex
	applyRunner          shell.Runner = shell.Real{} // runner used by Apply
	defaultPackageRunner shell.Runner = shell.Real{} // runner for package-level kubectl
)

func getApplyRunner() shell.Runner { runnerMu.RLock(); defer runnerMu.RUnlock(); return applyRunner }
func getPackageRunner() shell.Runner {
	runnerMu.RLock()
	defer runnerMu.RUnlock()
	return defaultPackageRunner
}

// SetApplyRunner overrides the runner for Apply (for unit tests).
func SetApplyRunner(r shell.Runner) { runnerMu.Lock(); defer runnerMu.Unlock(); applyRunner = r }

// SetPackageRunner overrides the runner for package-level kubectl calls (for unit tests).
func SetPackageRunner(r shell.Runner) {
	runnerMu.Lock()
	defer runnerMu.Unlock()
	defaultPackageRunner = r
}

// runPkg is a helper for package-level functions to use the testable runner.
func runPkg(name string, args ...string) ([]byte, error) {
	return getPackageRunner().Run(name, args...)
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
