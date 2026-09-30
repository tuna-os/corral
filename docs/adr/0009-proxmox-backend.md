# ADR-0009: Proxmox VE as a backend

**Status:** proposed
**Date:** 2026-07-31

## Context

Corral can already speak Proxmox in one direction. `pkg/proxmox` and the
`corral proxmox` plugin **serve** a subset of the PVE REST API. They translate
`/api2/json/…` onto KubeVirt, so Proxmox-shaped tooling can point at Corral.
This ADR is the **other direction** — a real PVE cluster as a Corral
backend, listed and driven alongside qemu, KubeVirt, Incus, and libvirt.

Do not confuse the two. They do not share code beyond types. The compat layer
answers PVE-shaped questions about Corral's fleet. The backend sends PVE-shaped
questions to somebody else's cluster.

Proxmox is the most-requested backend for the obvious reason. A large share of
the homelab and small-shop world already runs it. Also, Corral took its entire
vocabulary from Proxmox: VMs and Containers, nodes, snapshots, templates, pools,
live migration. The mapping is closer than for any backend Corral already has.

It arrives against a specific constraint that `docs/backend-parity.md` records.
Today, the code reaches the rich operations through `if backend == "kubevirt"`
branches. If Corral adds a sixth arm to 33 switch sites, Proxmox is
"best effort" on arrival. So this ADR specifies the mapping in full, and
sequences the implementation **after** the operation contract that parity work
introduces.

## Decision

### Transport: the HTTPS API with an API token

``https://{host}:8006/api2/json`` with an `Authorization: PVEAPIToken=…` header.
Not `pvesh` over SSH, and not ticket-plus-CSRF login:

- Every other backend shells out to a CLI because that CLI *is* the supported
  interface (`kubectl`, `virtctl`, `incus`, `virsh`). Proxmox's supported
  interface is the REST API; `pvesh` only exists on the node itself.
- API tokens are revocable, scopeable per privilege, and do not expire like
  tickets — the same logic as ADR-0003 on identity.
- This makes it the **first backend that is a Go HTTP client**, not a command
  runner. The `shell.Runner` seam does not apply, so the backend takes an
  `http.RoundTripper` seam instead, and tests use `httptest` the way `pkg/web`'s
  do.

Self-signed certificates are the norm on PVE. The context configuration carries
either a pinned certificate fingerprint or an explicit
`insecure_skip_verify: true` — never a silent skip, and never a global TLS
downgrade.

### Identity: context is a cluster, node is a node, vmid is not the name

| Corral | Proxmox | Notes |
|---|---|---|
| Context | one PVE cluster endpoint | Named in `contexts:` as `backend: proxmox`, carrying host, token, and TLS trust |
| Node | PVE node | Reported per instance, and the target for migration |
| Namespace | *unused* → `""` | PVE has no namespace; the field stays empty rather than being repurposed |
| Name | the VM/CT `name` | PVE names are per-cluster, not unique across clusters, which `InstanceRef` already handles |
| — | `vmid` | Carried as an opaque backend identifier, resolved by name at the edges |

`vmid` is the awkward one. PVE addresses everything by a cluster-wide integer,
Corral addresses everything by name. The backend keeps a name→vmid map
refreshed from `/cluster/resources` and resolves at call time. Bare-name
selectors stay a Corral concern: if two nodes in one cluster hold the same
name, PVE itself refuses, so this adds no ambiguity.

**PVE pools map to folders** (ADR-0008), not to namespaces. A pool is a group
of instances that an operator makes, and that is what a folder is. So the folder
tree gains an import path (`/pools`), and pools do not become a second concept
for groups.

### Feature mapping

Everything Corral ships, and the PVE call behind it. `{n}` is the node,
`{id}` the vmid, and `qemu` becomes `lxc` for containers throughout.

**Inventory and lifecycle**

| Corral operation | Proxmox |
|---|---|
| List | `GET /cluster/resources?type=vm` — one call for the whole cluster, VMs and CTs together, with node, status, cpu, mem, and tags |
| Create (VM) | `POST /nodes/{n}/qemu` — `cores`, `memory`, `scsi0`, `ide2` for an ISO, `net0`, `cipassword`/`sshkeys`/`ciuser` for cloud-init |
| Create (CT) | `POST /nodes/{n}/lxc` — `ostemplate`, `rootfs`, `unprivileged` |
| Start / Stop | `POST /nodes/{n}/qemu/{id}/status/start` / `/shutdown` (`/stop` only as the forced fallback) |
| Restart | `POST …/status/reboot` — a real reboot, not Corral's stop-then-start |
| Pause / Resume | `POST …/status/suspend` / `/resume` |
| Delete | `DELETE /nodes/{n}/qemu/{id}` with `purge=1` so jobs and backups do not dangle |

**Access**

| Corral operation | Proxmox |
|---|---|
| VNC console | `POST …/vncproxy` (`websocket=1`) then `GET …/vncwebsocket?port=&vncticket=` — bridged to the existing noVNC front end exactly as `vncBridge` bridges `virtctl vnc --proxy-only` |
| Serial / shell | `POST …/termproxy` then the same websocket — xterm.js, unchanged |
| SSH | the guest address from `GET …/agent/network-get-interfaces` (VM) or `GET …/interfaces` (CT), then plain `ssh` |
| RDP | the same guest-dependent probe and IronRDP bridge as ADR-0002; PVE plays no part beyond giving the address |

SPICE is out of scope: no browser client Corral can drive.

**Data and shape**

| Corral operation | Proxmox |
|---|---|
| Snapshots | `POST …/snapshot`, `GET …/snapshot`, `POST …/snapshot/{name}/rollback`, `DELETE …/snapshot/{name}` — a new `pkg/snapshot` adapter. Consistency: `Filesystem` when taken with `vmstate=1` or the guest agent's `fs-freeze`, `Crash` for a running VM without either, `Offline` when stopped |
| Migrate | `POST …/migrate` with `target` and `online=1`; `GET …/migrate` for preconditions, which is a better pre-flight than Corral's current `liveMigratable` guess |
| Clone | `POST …/clone`, `full=0` for linked or `1` for full |
| Template | `POST …/template` — PVE has the concept natively, so the mark is real rather than a Corral label |
| CPU / memory | `POST …/config` with `cores`/`memory`; hotplug where the guest has it enabled, restart otherwise — the same honest note the hardware form already shows |
| Disks | `POST …/config` with `scsiN`, `PUT …/resize`, `unlink` to remove |
| GPU | `POST …/config` with `hostpciN` |
| Export / backup | `POST /nodes/{n}/vzdump` then download from the storage — mapping to the existing export task and its progress reporting |
| Tags | the config's own `tags` field, comma-separated — native, unlike the label emulation elsewhere |
| Events | `GET /nodes/{n}/tasks` plus `GET /nodes/{n}/tasks/{upid}/log` |
| Metrics | `GET …/status/current` for the instant, `GET …/rrddata?timeframe=hour` for the CPU sparkline the web UI already draws |

**Containers**

PVE containers are LXC, and they map directly onto Corral's CT concept.
`unprivileged` is 1:1 with the Privileged checkbox that ADR-0005 already models.
`pct exec` equivalents come through `termproxy`. A PVE CT is closer to Corral's
CT than Corral's own pet-pod is to LXC. So this is the one place where the
Proxmox backend is *more* natural than the reference backend.

**Not mapped:** HA groups, firewall rules, replication jobs, Ceph management,
cluster join. Corral does not have those concepts. If Corral invents them for
one backend, parity dies.

### Everything asynchronous returns a UPID

Most PVE mutations do not complete inline; they return a task id
(`UPID:node:…`). The backend treats a UPID as the web layer already does its
own tasks. It registers the UPID with `taskBegin`, polls
`GET /nodes/{n}/tasks/{upid}/status`, and surfaces progress through the existing
task-log endpoint. So on this backend, migration, export, and clone get real
progress reports for free. Note that the progress reports of the *existing*
backends are weaker than what PVE hands over.

### Capabilities are declared by what is implemented

The backend must not get a hardcoded row in `types.CapabilitiesForBackend`.
It declares the operation interfaces that it satisfies, and Corral derives the
capability flags from them. `docs/backend-parity.md` step 2 introduces this
mechanism. Until that
exists, the Proxmox row in `pkg/backend.Matrix` stays entirely `Possible`, which
is exactly what the matrix is for.

What the cluster reports then narrows the per-instance capabilities. No guest
agent means no SSH and no RDP probe. A storage without snapshot support means no
snapshots for instances on it. One node means no migration.

## Consequences

- A new package, `pkg/proxmoxbe`. The name matters: `pkg/proxmox` is the compat
  server and must not become ambiguous. Also a context type, and doctor checks
  for reachability, token validity, TLS trust, and privilege coverage.
- The first backend that needs an HTTP client seam. Tests use `httptest` with
  recorded PVE payloads, not `shell.Fake`.
- The Proxmox compat layer becomes able to front a real Proxmox cluster. That
  is either delightful or a support hazard; it depends on how we document it.
- Peers and Proxmox stack. A Corral that aggregates a PVE cluster can itself be
  a peer. So a PVE instance can appear in another Corral's tree. `InstanceRef`
  already carries this.
- CI: a real PVE cannot run in GitHub Actions. Coverage is `httptest` against
  recorded payloads, plus a documented manual matrix run against a real cluster
  before release. This is the same honesty that `docs/testing.md` applies to KVM
  hardware.

## Alternatives considered

**SSH plus `pvesh`/`qm`/`pct`.** Fits the existing `shell.Runner` seam and every
backend's shape, and needs no token. Rejected: it needs root SSH to a node. It
breaks the moment a migration moves guests away from that same node. It has no
cluster-wide entry point, and it gives up the task ids that the API returns.

**Reuse `pkg/proxmox`'s types for both directions.** Symmetry makes it
attractive, but it is wrong. The compat layer's shapes are what Corral *emits*
to satisfy old clients. If we couple them to what a real PVE *returns*, one
would break the other. Shared code, if any, is a payload package that neither
side owns.

**Terraform/Ansible provider as the transport.** Rejected — a dependency on
somebody else's state model to do stateless operations.

## Not in scope

The operation contract itself. This ADR depends on it and describes what
Proxmox needs from it. But the contract is a parity concern for every backend,
and it belongs in its own change (`docs/backend-parity.md`, step 2).
