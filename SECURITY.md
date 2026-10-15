# Security Policy

## Supported Versions

Corral is pre-1.0 and ships from `main` plus tagged releases. We give
security fixes only to the most recent release. If you use an older tag,
upgrade to the latest release first. The fix lands there.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Instead, report them privately through GitHub Security Advisories:

1. Go to the [Security tab](https://github.com/tuna-os/corral/security)
2. Click **Report a vulnerability**
3. Give a detailed description, with steps to reproduce, affected
   backends, and the `corral version` output

You can expect:

- **Acknowledgment** within 48 hours
- **Status update** within 5 business days
- **Resolution timeline** based on severity

We follow coordinated disclosure. You report privately, and we investigate
and fix. We publish the advisory after we release the fix.

## Security Model

Corral manages VMs across backends (local QEMU, KubeVirt, Incus, libvirt,
Proxmox VE) and exposes guests over your Tailscale tailnet:

- **Secrets stay local.** Cloud-init passwords live in the registry file
  (`~/.local/share/corral/registry.json`, mode 0600). The Tailscale auth key
  comes from config or the `TS_AUTHKEY` environment variable. For backend
  tokens, use environment variables such as `CORRAL_PROXMOX_TOKEN`. Then
  your credentials stay out of dotfiles repos.
- **No client-side K8s SDK.** Corral runs `kubectl` and `virtctl` for
  cluster work. Thus Corral uses your local kubeconfig and never keeps
  cluster credentials itself.
- **Tailnet trust.** Guests on the tailnet get the ACLs of your tailnet.
  Treat exposed ports (`corral` port-proxy and per-VM Services) as
  tailnet surface, not LAN surface.
- **Web UI access.** On a workstation, `corral web` uses the cluster
  credentials of the operator. Limit who can reach its listen address.
  In a cluster, the `corral-web` service account uses the RBAC in
  `deploy/`. That ClusterRole grants only the verbs that the code uses.

## Scope

In scope: the `corral` binary (CLI, TUI, `corral web`), backend clients
under `pkg/`, and the release workflows that build them.

Out of scope: vulnerabilities in guest OS images and in upstream projects
(QEMU, KubeVirt, Incus, Proxmox VE, Tailscale). Operator infrastructure
(cluster RBAC, tailnet ACLs) is also out of scope. But tell us when the
defaults of Corral make those worse.
