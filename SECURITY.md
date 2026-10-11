# Security Policy

## Supported Versions

Corral is pre-1.0 and ships from `main` plus tagged releases. Only the most
recent release is actively supported with security fixes. If you are pinned
to an older tag, upgrade to the latest release first — the fix will land
there.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, report them privately via GitHub Security Advisories:

1. Go to the [Security tab](https://github.com/tuna-os/corral/security)
2. Click **Report a vulnerability**
3. Provide a detailed description, including steps to reproduce, affected
   backends, and the `corral version` output

You can expect:

- **Acknowledgment** within 48 hours
- **Status update** within 5 business days
- **Resolution timeline** based on severity

We follow coordinated disclosure: report privately, we investigate and fix,
then we publish the advisory after the fix is released.

## Security Model

Corral manages VMs across backends (local QEMU, KubeVirt, Incus, libvirt,
Proxmox VE) and exposes guests over your Tailscale tailnet:

- **Secrets stay local.** Cloud-init passwords live in the registry file
  (`~/.local/share/tailvm/registry.json`, mode 0600). The Tailscale auth key
  comes from config or the `TS_AUTHKEY` environment variable. Backend
  tokens (e.g. Proxmox API tokens) prefer environment variables
  (`CORRAL_PROXMOX_TOKEN`) over the config file so credentials do not land
  in dotfiles repos.
- **No client-side K8s SDK.** Cluster interaction shells out to `kubectl` /
  `virtctl`, so Corral respects your local kubeconfig and never takes
  custody of cluster credentials itself.
- **Tailnet trust.** Guests reachable over the tailnet inherit your tailnet's
  ACLs. Review exposed ports (`corral` port-proxy / per-VM Services) as
  tailnet surface, not LAN surface.
- **Web UI RBAC.** `corral web` runs with the operator's own cluster
  credentials; restrict who can reach its listen address accordingly.

## Scope

In scope: the `corral` binary (CLI, TUI, `corral web`), backend clients
under `pkg/`, and the release workflows that build them.

Out of scope: vulnerabilities in the guest OS images themselves, in
upstream projects (QEMU, KubeVirt, Incus, Proxmox VE, Tailscale), or in
operator infrastructure (cluster RBAC, tailnet ACLs) — though we welcome a
heads-up when Corral's defaults make those worse.
