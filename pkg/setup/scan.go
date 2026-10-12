// Package setup scans the local host for usable compute backends and saves
// the contexts the user confirms — the `corral setup` onboarding path.
//
// Scope is deliberately narrow: host-local probes only (qemu, incus,
// libvirt), plus reachability for already-configured kubevirt/proxmox
// targets. It never sweeps the network and never dials a host the user has
// not configured — a setup wizard that phones strangers is a scanner, and
// this is not one.
package setup

import (
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/tuna-os/corral/pkg/config"
	"github.com/tuna-os/corral/pkg/shell"
)

// Verdict is what the scan concluded about one backend.
type Verdict string

const (
	// VerdictReady means the backend works now.
	VerdictReady Verdict = "READY"
	// VerdictDegraded means pieces exist but something needs attention
	// (missing acceleration, unreachable daemon, token absent).
	VerdictDegraded Verdict = "DEGRADED"
	// VerdictAbsent means the backend's tooling is not installed.
	VerdictAbsent Verdict = "ABSENT"
	// VerdictNotConfigured means there is nothing to probe — no kubeconfig,
	// no PVE context — so the backend is skipped, not failed.
	VerdictNotConfigured Verdict = "NOT_CONFIGURED"
)

// BackendResult is the scan outcome for one backend.
type BackendResult struct {
	Backend    string
	Verdict    Verdict
	Detail     string
	Remedy     string
	Configured bool
}

// Report is the full host scan, in backend-probe order.
type Report struct {
	Results []BackendResult
}

// ByBackend returns the result for a backend, or nil.
func (r Report) ByBackend(backend string) *BackendResult {
	for i := range r.Results {
		if r.Results[i].Backend == backend {
			return &r.Results[i]
		}
	}
	return nil
}

// Ranked orders results for the wizard: READY first (probe order), then
// DEGRADED, ABSENT, NOT_CONFIGURED. The first READY entry is the only
// default candidate — at most one default, ever.
func (r Report) Ranked() []BackendResult {
	out := append([]BackendResult(nil), r.Results...)
	rank := map[Verdict]int{
		VerdictReady: 0, VerdictDegraded: 1,
		VerdictAbsent: 2, VerdictNotConfigured: 3,
	}
	// Stable: ties keep probe order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && rank[out[j].Verdict] < rank[out[j-1].Verdict]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

var runner shell.Runner = shell.Real{}

// SetRunner overrides the command runner (tests inject shell.Fake here).
func SetRunner(r shell.Runner) { runner = r }

// kvmStat reports /dev/kvm usability; a seam so tests need no device node.
var kvmStat = func() error {
	_, err := os.Stat("/dev/kvm")
	return err
}

// proxmoxReachable checks one configured PVE host. A variable so tests never
// touch the network — and so the unconfigured path can prove it never dials.
var proxmoxReachable = func(host string) error {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, "8006"), 3*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

// GroupRemedy tells the user how to fix a permission-denied device or
// socket. Group membership, never sudo: running Corral as root to reach a
// daemon is how homelab operators end up with root-owned config files.
func GroupRemedy(group string) string {
	return "permission denied — add yourself to the group and log out/in: " +
		"usermod -aG " + group + " $USER (then log out and back in; do not run corral with sudo)"
}

func isPermissionErr(err error, out []byte) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EACCES) || errors.Is(err, os.ErrPermission) {
		return true
	}
	return strings.Contains(strings.ToLower(string(out)+err.Error()), "permission denied")
}

// Scan probes every backend and returns the report. Read-only: it never
// writes config and never contacts unconfigured hosts.
func Scan() Report {
	return Report{Results: []BackendResult{
		probeQemu(),
		probeIncus(),
		probeLibvirt(),
		probeKubevirt(),
		probeProxmox(),
		unprobed("virtualbox", "VirtualBox", "corral context add --backend virtualbox"),
		unprobed("vsphere", "vSphere", "configure a vsphere context, then corral doctor --context NAME"),
		unprobed("xen", "Xen", "configure a xen context, then corral doctor --context NAME"),
		unprobed("nutanix", "Nutanix", "configure a nutanix context, then corral doctor --context NAME"),
	}}
}

// unprobed covers backends the scan does not probe: local-but-unowned
// (virtualbox) or remote-only (vsphere/xen/nutanix), where any contact would
// mean dialing a host on the scan's own authority. Configured means "present,
// health via doctor"; unconfigured means "not set up".
func unprobed(backend, label, hint string) BackendResult {
	for _, c := range config.Contexts() {
		if c.Backend == backend {
			return BackendResult{Backend: backend, Verdict: VerdictDegraded,
				Configured: true,
				Detail:     label + " context " + c.Name + " is configured (health checked by corral doctor, not the setup scan)",
				Remedy:     "corral doctor --context " + c.Name}
		}
	}
	return BackendResult{Backend: backend, Verdict: VerdictNotConfigured,
		Detail: label + " is not configured", Remedy: hint}
}

func probeQemu() BackendResult {
	r := BackendResult{Backend: "qemu", Configured: true}
	if _, err := runner.LookPath("qemu-system-x86_64"); err != nil {
		r.Verdict = VerdictAbsent
		r.Detail = "qemu-system-x86_64 is not installed"
		r.Remedy = "install qemu-kvm (dnf/apt install qemu-kvm qemu-img)"
		return r
	}
	version := ""
	if out, err := runner.Run("qemu-system-x86_64", "--version"); err == nil {
		if line, _, _ := strings.Cut(string(out), "\n"); line != "" {
			version = " (" + strings.TrimSpace(line) + ")"
		}
	}
	if err := kvmStat(); err != nil {
		r.Verdict = VerdictDegraded
		if isPermissionErr(err, nil) {
			r.Detail = "qemu is installed but /dev/kvm is not usable by you" + version
			r.Remedy = GroupRemedy("kvm")
		} else {
			r.Detail = "qemu is installed but /dev/kvm is missing — VMs run without acceleration" + version
			r.Remedy = "enable virtualization in firmware, or use software emulation as-is"
		}
		return r
	}
	r.Verdict = VerdictReady
	r.Detail = "qemu works with /dev/kvm acceleration" + version
	return r
}

func probeIncus() BackendResult {
	r := BackendResult{Backend: "incus", Configured: true}
	if _, err := runner.LookPath("incus"); err != nil {
		r.Verdict = VerdictAbsent
		r.Detail = "incus is not installed"
		r.Remedy = "install incus, then corral setup to add the local remote"
		return r
	}
	if out, err := runner.Run("incus", "query", "local:/1.0"); err != nil {
		r.Verdict = VerdictDegraded
		r.Detail = "incus is installed but the local daemon did not answer"
		r.Remedy = strings.TrimSpace(string(out)+"; "+err.Error()) + "; verify `incus remote list` and that your user can reach the socket (" + GroupRemedy("incus") + " when permission is denied)"
		if isPermissionErr(err, out) {
			r.Remedy = GroupRemedy("incus")
		}
		return r
	}
	out, err := runner.Run("incus", "info", "local:")
	info := strings.ToLower(string(out))
	// "virtual-machine" is the instance type; a bare "instance_types" line
	// (e.g. "instance_types: container") is not VM capability, and neither
	// is every qemu mention — but a qemu driver with no explicit type list
	// is still a VM host.
	if err != nil || !(strings.Contains(info, "virtual-machine") || strings.Contains(info, "qemu")) {
		r.Verdict = VerdictDegraded
		r.Detail = "incus daemon answers but did not advertise VM/QEMU support"
		r.Remedy = "enable KVM on this host, or use incus for containers only"
		return r
	}
	r.Verdict = VerdictReady
	r.Detail = "incus local remote answers with VM support"
	return r
}

// libvirtURIs is the local daemon plus every configured libvirt URI —
// configured remotes are fair game, invented ones are not.
func libvirtURIs() []string {
	uris := []string{"qemu:///system"}
	for _, c := range config.Contexts() {
		if c.Backend == "libvirt" && c.Context != "" && c.Context != uris[0] {
			uris = append(uris, c.Context)
		}
	}
	return uris
}

func probeLibvirt() BackendResult {
	r := BackendResult{Backend: "libvirt", Configured: true}
	if _, err := runner.LookPath("virsh"); err != nil {
		r.Verdict = VerdictAbsent
		r.Detail = "virsh is not installed"
		r.Remedy = "install libvirt clients (dnf/apt install libvirt-client)"
		return r
	}
	var lastErr string
	for _, uri := range libvirtURIs() {
		out, err := runner.Run("virsh", "-c", uri, "connect")
		if err != nil {
			lastErr = strings.TrimSpace(string(out) + "; " + err.Error())
			if isPermissionErr(err, out) {
				r.Verdict = VerdictDegraded
				r.Detail = "libvirt daemon at " + uri + " refused permission"
				r.Remedy = GroupRemedy("libvirt")
				return r
			}
			continue
		}
		if out, err := runner.Run("virsh", "-c", uri, "nodeinfo"); err != nil {
			lastErr = strings.TrimSpace(string(out) + "; " + err.Error())
			continue
		}
		r.Verdict = VerdictReady
		r.Detail = "libvirt answers at " + uri
		return r
	}
	r.Verdict = VerdictDegraded
	r.Detail = "virsh is installed but no libvirt daemon answered"
	r.Remedy = "start libvirtd and verify the URI" + lastErrSuffix(lastErr)
	return r
}

func lastErrSuffix(lastErr string) string {
	if lastErr == "" || lastErr == ";" {
		return ""
	}
	return " (" + lastErr + ")"
}

func probeKubevirt() BackendResult {
	r := BackendResult{Backend: "kubevirt"}
	if !config.KubeconfigPresent() {
		r.Verdict = VerdictNotConfigured
		r.Detail = "no kubeconfig found — kubevirt needs a cluster"
		r.Remedy = "set KUBECONFIG or place config at ~/.kube/config"
		return r
	}
	r.Configured = true
	rn := runner
	if ctx := kubevirtContext(); ctx != "" {
		rn = shell.WithKubeContext(runner, ctx)
	}
	if out, err := rn.Run("kubectl", "get", "--raw", "/livez", "--request-timeout=3s"); err != nil {
		r.Verdict = VerdictDegraded
		r.Detail = "kubeconfig exists but the Kubernetes API did not answer"
		r.Remedy = strings.TrimSpace(string(out)+"; "+err.Error()) + "; verify kubeconfig, context, VPN, and credentials"
		return r
	}
	if _, err := rn.LookPath("virtctl"); err != nil {
		r.Verdict = VerdictDegraded
		r.Detail = "cluster answers but virtctl is not installed"
		r.Remedy = "install virtctl for console and SSH commands"
		return r
	}
	r.Verdict = VerdictReady
	r.Detail = "Kubernetes API answers"
	return r
}

// kubevirtContext returns the configured kubevirt context, if any — the
// probe binds to it rather than whatever kubectl would pick.
func kubevirtContext() string {
	for _, c := range config.Contexts() {
		if c.Backend == "kubevirt" && c.Context != "" {
			return c.Context
		}
	}
	return ""
}

func probeProxmox() BackendResult {
	r := BackendResult{Backend: "proxmox"}
	var targets []config.ContextConfig
	for _, c := range config.Contexts() {
		if c.Backend == "proxmox" {
			targets = append(targets, c)
		}
	}
	if len(targets) == 0 {
		r.Verdict = VerdictNotConfigured
		r.Detail = "no proxmox context configured — the scan never invents PVE hosts"
		r.Remedy = "corral setup to add one (host + API token)"
		return r
	}
	r.Configured = true
	fallback := r
	fallback.Verdict = VerdictDegraded
	for _, t := range targets {
		if err := proxmoxReachable(t.Context); err != nil {
			fallback.Detail = "PVE host " + t.Context + " (" + t.Name + ") unreachable: " + err.Error()
			fallback.Remedy = "verify the host, port 8006, and VPN"
			continue
		}
		if tokenFor(t.Name, t) == "" {
			fallback.Detail = "PVE host " + t.Context + " answers, but no API token is set"
			fallback.Remedy = "set " + tokenEnvFor(t.Name) + " (preferred; never stored in the config file)"
			continue
		}
		r.Verdict = VerdictReady
		r.Detail = "PVE host " + t.Context + " (" + t.Name + ") answers with a token configured"
		return r
	}
	return fallback
}
