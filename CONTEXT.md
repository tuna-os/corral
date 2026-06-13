# Corral — Domain Context

## Concepts

### VM

A virtual machine managed by Corral. Lives in one of two backends: **qemu**
(local host, systemd-managed) or **kubevirt** (on the Talos K8s cluster).
Every VM has a unique **name** (within the registry) and is stored in
`~/.local/share/tailvm/registry.json`.

### vmid

A Proxmox-style numeric VM identifier (range 100–999999999). Assigned to
KubeVirt VMs created through the Proxmox API compatibility layer.
Bidirectionally mapped to K8s VM names via the `corral.io/proxmox-vmid`
label. Pre-existing VMs without the label derive their vmid from a CRC32
hash of their name.

### Backend

Where a VM's compute resources live. Two backends:

- **qemu** — local `qemu-system-x86_64` process managed via systemd user
  units. Networking via user-mode with hostfwd. Access through the host's
  Tailscale IP.
- **kubevirt** — VM runs as a KubeVirt `VirtualMachine` resource on the
  Talos cluster. Managed via `kubectl`/`virtctl`. Access through
  `virtctl` tunnels or port-proxy Service on the tailnet.

### Registry

The file `~/.local/share/tailvm/registry.json` (mode 0600). Maps VM names
to their backend, namespace, cloud-init password, and other metadata. The
single source of truth for local VM state. Live probing is the fallback.

### Plugin

A krew-style extension binary (`corral-<name>`) installed via
`corral plugin install <name>` from the marketplace. Dispatched when
`corral <name>` is invoked and the subcommand isn't a built-in.

### Proxmox API compatibility layer

A package (`pkg/proxmox`) that translates the Proxmox VE REST API
(`/api2/json/...`) onto KubeVirt operations. Served as both:
- A standalone plugin binary (`corral-proxmox`, marketplace)
- Embedded in `corral web` at `/api2/json/...` (always available
  when the web server is running)

Enables Proxmox ecosystem tools (Terraform bpg/proxmox, Ansible,
proxmoxer) to manage KubeVirt VMs without modification.

### Node

A Kubernetes node in the Talos cluster. Exposed in the Proxmox API as a
Proxmox "node" with properties derived from `kubectl get node` output
(CPU capacity, memory, Ready condition).

### Storage class

A Kubernetes StorageClass. Mapped to Proxmox storage types:
- Longhorn (`driver.longhorn.io`) → `lvmthin`
- local-path (`rancher.io/local-path`) → `dir`

### Access control (Proxmox)

A read-only view of K8s RBAC translated into Proxmox shapes:
- **Users**: extracted from ClusterRoleBinding subjects (`root@pam` always
  present, ServiceAccounts mapped to `name@sa`)
- **Groups**: extracted from ClusterRoleBinding group subjects
- **Roles**: four fixed roles (Administrator, Operator, Viewer, NoAccess)
  mapped from the K8s cluster-admin/admin/view/default privilege levels

Auth enforcement is delegated to tailnet membership + K8s RBAC. Proxmox
privilege strings are presentation-only.

### Marketplace

A JSON index at `marketplace/index.json` listing available plugin binaries
with download URLs for linux/amd64 and linux/arm64. Published by CI as
GitHub Release assets.

## Glossary

| Term | Definition |
|---|---|
| vm | Virtual Machine |
| vmid | Proxmox numeric VM identifier |
| backend | qemu or kubevirt |
| registry | `~/.local/share/tailvm/registry.json` |
| plugin | krew-style corral-* binary |
| proxmox api | `/api2/json/` compatibility layer |
| node | K8s cluster node |
| storage class | K8s StorageClass |
| RBAC mapping | K8s ClusterRoles → Proxmox privileges |
