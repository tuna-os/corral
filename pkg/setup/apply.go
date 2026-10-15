// Apply: validate-then-save for one backend.
//
// Every apply re-probes first and refuses to save a backend the scan says
// is absent — a context that cannot work is not onboarding, it is a ticket.
// An existing context is never overwritten without the overwrite path
// (--force on the CLI, Overwrite here), so a re-run cannot eat a working
// PVE host or a tuned libvirt URI.
package setup

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tuna-os/corral/pkg/config"
)

// ErrExists means a context with this name already exists; the caller must
// get explicit confirmation (Overwrite=true) before replacing it.
var ErrExists = errors.New("context already exists (re-run with --force to overwrite)")

// ApplyOptions selects what gets saved.
type ApplyOptions struct {
	// Name is the context name; defaults per backend when empty.
	Name string
	// Value is backend-specific: Incus remote, libvirt URI, kubeconfig
	// context, PVE host.
	Value string
	// Overwrite permits replacing an existing context.
	Overwrite bool
	// MakeDefault selects the saved context as the default destination.
	MakeDefault bool
	// Proxmox extras, stored alongside the host.
	ProxmoxNode        string
	ProxmoxFingerprint string
	ProxmoxInsecure    bool
}

// Apply validates the backend by re-probing, then saves the context.
func Apply(backend string, opts ApplyOptions) error {
	switch backend {
	case "qemu":
		return applyQemu(opts)
	case "incus":
		return applyIncus(opts)
	case "libvirt":
		return applyLibvirt(opts)
	case "kubevirt":
		return applyKubevirt(opts)
	case "proxmox":
		return applyProxmox(opts)
	default:
		return fmt.Errorf("setup does not onboard backend %q (configure it with corral context add)", backend)
	}
}

func applyQemu(opts ApplyOptions) error {
	if r := probeQemu(); r.Verdict == VerdictAbsent {
		return fmt.Errorf("qemu is not usable here: %s (%s)", r.Detail, r.Remedy)
	}
	// The local qemu context is implicit — nothing to store. Selecting it
	// as the default is the only state a qemu onboarding can leave behind.
	if opts.MakeDefault {
		if err := config.SetDefaultBackend("qemu"); err != nil {
			return fmt.Errorf("select qemu default: %w", err)
		}
	}
	return nil
}

func applyIncus(opts ApplyOptions) error {
	if r := probeIncus(); r.Verdict == VerdictAbsent || r.Verdict == VerdictDegraded {
		return fmt.Errorf("incus is not usable here: %s (%s)", r.Detail, r.Remedy)
	}
	remote := opts.Value
	if remote == "" {
		remote = config.IncusRemote()
	}
	name := opts.Name
	if name == "" {
		name = "incus"
	}
	if err := guardOverwrite(name, opts.Overwrite); err != nil {
		return err
	}
	if err := config.AddContext(config.ContextConfig{Name: name, Backend: "incus", Context: remote}); err != nil {
		return err
	}
	return maybeDefault(name, opts.MakeDefault)
}

func applyLibvirt(opts ApplyOptions) error {
	if r := probeLibvirt(); r.Verdict == VerdictAbsent || r.Verdict == VerdictDegraded {
		return fmt.Errorf("libvirt is not usable here: %s (%s)", r.Detail, r.Remedy)
	}
	uri := opts.Value
	if uri == "" {
		uri = config.LibvirtURI()
		if uri == "" {
			uri = "qemu:///system"
		}
	}
	name := opts.Name
	if name == "" {
		name = "libvirt"
	}
	if err := guardOverwrite(name, opts.Overwrite); err != nil {
		return err
	}
	if err := config.AddContext(config.ContextConfig{Name: name, Backend: "libvirt", Context: uri}); err != nil {
		return err
	}
	return maybeDefault(name, opts.MakeDefault)
}

func applyKubevirt(opts ApplyOptions) error {
	if r := probeKubevirt(); r.Verdict == VerdictNotConfigured || r.Verdict == VerdictDegraded {
		return fmt.Errorf("kubevirt is not usable here: %s (%s)", r.Detail, r.Remedy)
	}
	name := opts.Name
	if name == "" {
		name = "kubevirt"
	}
	if err := guardOverwrite(name, opts.Overwrite); err != nil {
		return err
	}
	if err := config.AddContext(config.ContextConfig{Name: name, Backend: "kubevirt", Context: opts.Value}); err != nil {
		return err
	}
	return maybeDefault(name, opts.MakeDefault)
}

func applyProxmox(opts ApplyOptions) error {
	host := opts.Value
	if host == "" {
		return fmt.Errorf("proxmox needs a host (PVE address); the scan never invents one")
	}
	// Re-probe reachability for this host only — configured-or-explicit,
	// never a sweep.
	if err := proxmoxReachable(host); err != nil {
		return fmt.Errorf("PVE host %s unreachable: %v (verify host, port 8006, VPN)", host, err)
	}
	name := opts.Name
	if name == "" {
		name = "pve"
	}
	if err := guardOverwrite(name, opts.Overwrite); err != nil {
		return err
	}
	// Token-from-env preference: a token in the environment is never
	// written to the config file (dotfiles repos are public by accident).
	// The context stores only non-secret endpoint details.
	c := config.ContextConfig{Name: name, Backend: "proxmox", Context: host}
	if opts.ProxmoxNode != "" || opts.ProxmoxFingerprint != "" || opts.ProxmoxInsecure {
		c.Proxmox = &config.ProxmoxContextConfig{
			Node: opts.ProxmoxNode, Fingerprint: opts.ProxmoxFingerprint,
			Insecure: opts.ProxmoxInsecure,
		}
	}
	if tokenFor(name, c) == "" {
		// Saved anyway: the operator sets the env var next, and the host
		// is already validated. The hint tells them exactly which var.
		deferHint = "note: no API token found — set " + tokenEnvFor(name) + " before using this context"
	}
	if err := config.AddContext(c); err != nil {
		return err
	}
	return maybeDefault(name, opts.MakeDefault)
}

// deferHint carries the token reminder out of applyProxmox without changing
// its error contract; the wizard prints it after a successful save.
var deferHint string

// TakeHint returns and clears the pending post-save hint, if any.
func TakeHint() string {
	h := deferHint
	deferHint = ""
	return h
}

func guardOverwrite(name string, overwrite bool) error {
	if _, ok := config.FindContext(name); ok && !overwrite {
		return fmt.Errorf("context %q %w", name, ErrExists)
	}
	return nil
}

func maybeDefault(name string, makeDefault bool) error {
	if !makeDefault {
		return nil
	}
	return config.SetDefaultContext(name)
}

// tokenEnvFor names the per-context token variable, falling back to the
// shared one — the same precedence pkg/proxmoxbe uses.
func tokenEnvFor(contextName string) string {
	return "CORRAL_PROXMOX_TOKEN_" + envSuffix(contextName)
}

func envSuffix(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	return sb.String()
}

// tokenFor reports the effective token without ever persisting it: the
// per-context variable, the shared variable, then the stored field.
func tokenFor(contextName string, c config.ContextConfig) string {
	if t := os.Getenv(tokenEnvFor(contextName)); t != "" {
		return t
	}
	if t := os.Getenv("CORRAL_PROXMOX_TOKEN"); t != "" {
		return t
	}
	if c.Proxmox != nil {
		return c.Proxmox.Token
	}
	return ""
}
