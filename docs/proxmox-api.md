# Proxmox VE API compatibility layer

Corral serves a useful subset of the [Proxmox VE REST
API](https://pve.proxmox.com/pve-docs/api-viewer/) (`/api2/json/…`),
translated live onto KubeVirt. Proxmox ecosystem tools — the Terraform
`bpg/proxmox` provider, Ansible `community.general.proxmox_*` modules,
`proxmoxer`, monitoring scripts — can list, create, start/stop, and delete
KubeVirt VMs without knowing corral exists.

Implementation: `pkg/proxmox` (verified against `bpg/proxmox` v0.109.0).
ADR: [adr/0001-k8s-rbac-to-proxmox-privileges.md](adr/0001-k8s-rbac-to-proxmox-privileges.md).

## Two ways to run it

1. **Mounted in `corral web`** — the dashboard server mounts the handler at
   `/api2/json/` automatically (`pkg/web/server.go`), so a single
   `corral web` (or the on-cluster deployment) speaks both the corral REST
   API and the Proxmox API on the same port (8006 — Proxmox's own port,
   deliberately).
2. **Standalone plugin** — `corral plugin install proxmox`, then
   `corral proxmox serve [--addr :8006] [--cert … --key …]`. The
   `bpg/proxmox` Terraform provider requires TLS, hence the cert flags.

## Architecture

Three modules, all behind the `shell.Runner` seam (hermetically testable
with `shell.Fake` — see `server_test.go` and `server_compat_test.go`, which
pin response *shapes* against what `bpg/proxmox` expects):

| Module | Role |
|---|---|
| `server.go` | HTTP routing, auth, the Proxmox `{"data": …}` envelope |
| `queries.go` | kubectl JSON adapters: `NodeQuery`, `StorageQuery`, `RBACQuery`, `VMIDLabelQuery` |
| `translate.go` | Pure mapping functions: vmid hashing, status vocab, memory parsing, UPIDs, role tables |

### VMID mapping

Proxmox addresses VMs by numeric vmid; KubeVirt by name. Two-way resolution:

- VMs **created through this API** get a `corral.io/proxmox-vmid=<vmid>`
  label, so the requested vmid round-trips exactly.
- **Pre-existing VMs** get a deterministic fallback id:
  `100 + crc32(name) % 899999900` (`VmidFor`). No state to store;
  collisions are theoretically possible but irrelevant at homelab scale.

### Tasks (UPIDs)

Proxmox operations are async and return a UPID; corral's operations are
synchronous. Mutating endpoints return a fabricated UPID and
`GET /nodes/{node}/tasks/{upid}/status` always reports
`stopped`/`exitstatus: OK`. Tools that poll task status complete instantly.

### Auth

`authorized()` accepts both styles Proxmox tooling speaks, against a single
shared secret:

- API token header `Authorization: PVEAPIToken=user@realm!id=SECRET`
  (Terraform's stateless path)
- Ticket cookie `PVEAuthCookie=PVE:user:SECRET`, issued by
  `POST /access/ticket` when the login password equals the secret

Set the secret with `corral proxmox serve --token <secret>` or the
`CORRAL_PROXMOX_TOKEN` env var (also respected by the handler mounted in
`corral web`). With no secret configured the API is **open** — acceptable
only because corral deployments are gated by tailnet membership. Never
expose it off the tailnet.

## Endpoint inventory

| Endpoint | Behavior |
|---|---|
| `POST /access/ticket` | Issues a ticket; checks password against the shared secret if set |
| `GET /version` | Static `8.2.0` (`repoid: corral-kubevirt`) |
| `GET /nodes` | K8s nodes → Proxmox nodes (Ready → online) |
| `GET /nodes/{n}/status` | Per-node CPU/memory capacity |
| `GET /nodes/{n}/storage` | StorageClasses → Proxmox storage (longhorn → lvmthin, else dir) |
| `GET /nodes/{n}/time`, `/dns`, `/hosts` | Plausible static answers |
| `GET /cluster/resources` | VMs (`type=qemu`) + nodes, filterable with `?type=` |
| `GET /nodes/{n}/qemu` | VM list for a node (unplaced/stopped VMs show on every node) |
| `POST /nodes/{n}/qemu` | Create: maps vmid, name, cores, memory (MB); labels the VM with its vmid |
| `GET …/qemu/{vmid}/status/current` | Status, cpus, maxmem, guest-agent flag |
| `POST …/qemu/{vmid}/status/{action}` | start / stop / shutdown / reset / reboot (stop ≙ graceful KubeVirt stop) |
| `GET …/qemu/{vmid}/config` | name, cores, memory (MB), ostype l26, virtio net stub |
| `DELETE …/qemu/{vmid}` | Full corral delete (VM + disks + snapshots + proxy resources) |
| `POST …/qemu/{vmid}/vncproxy`, `/termproxy` | Stub tickets pointing at corral's websocket bridges (see gaps) |
| `GET /access/users`, `/groups`, `/roles` | K8s ClusterRoleBindings → Proxmox users/groups; fixed role table (ADR-0001) |
| `GET /pools`, `/cluster/ha/groups/`, `/nodes/{n}/lxc` | Valid empty answers (no pools/HA-groups/LXC) |
| anything else under `/api2/json/` | Proxmox-shaped 404 + `[proxmox-gap]` stderr log for gap discovery |

The catch-all gap log is the discovery mechanism: run a new tool against
corral, grep the server log for `[proxmox-gap]`, and you have the exact list
of endpoints it needs.

## Known gaps / room for improvement

Reviewed 2026-06-12. Fixed in this pass:

- ~~`cmd/corral-proxmox` duplicated all of `pkg/proxmox` (~1,000 lines,
  case-renamed)~~ — deleted; the plugin now only wraps
  `proxmox.NewHandler`.
- ~~`Server.token` existed but nothing could set it~~ — now wired to
  `--token` / `CORRAL_PROXMOX_TOKEN`.

Remaining, roughly by value:

1. **Create ignores disks.** `POST /nodes/{n}/qemu` maps only
   vmid/name/cores/memory — `scsi0`/`virtio0`/`ide2` (disk and CD-ROM
   specs) are dropped, so an API-created VM has no boot source. Mapping
   `storage:size` to a blank PVC and `iso` media to the CDI ISO path would
   make Terraform-provisioned VMs actually bootable.
2. **`PUT/POST …/config` not implemented.** Terraform updates (cpu/memory
   resize) 404. Corral already has `Scale()` — wiring it in is cheap.
3. **vncproxy/termproxy tickets aren't connectable** by stock Proxmox
   clients: they expect a TCP VNC endpoint plus the `vncwebsocket`
   endpoint, while corral exposes its own websocket bridges
   (`/api/vnc/{ns}/{name}`). Implementing
   `GET …/qemu/{vmid}/vncwebsocket` as a passthrough to the existing
   bridge would let noVNC-based tools connect for real.
4. **Metrics are zeros.** `uptime`, `cpu`, `mem`, `maxdisk` are 0 in
   node and VM rows; dashboards render but look idle. Could be populated
   from metrics-server when present (corral web already has that
   plumbing).
5. **Snapshot endpoints missing.** Corral supports VM snapshots; Proxmox
   tools use `GET/POST …/qemu/{vmid}/snapshot`. Another cheap win.
6. **`findVM` lists all VMs up to twice per request** (label lookup, then
   crc32 fallback). Fine at homelab scale; an index/cache would help at
   hundreds of VMs.
7. **vmid collisions are unhandled** for unlabeled VMs (crc32 truncation).
   Worst case two VMs answer to the same vmid; first match wins.
