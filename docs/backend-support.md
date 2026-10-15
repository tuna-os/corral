# Backend support matrix

Corral can show several contexts at once. A context is a named connection to
one backend. A change to the default context changes only where an unqualified
command goes. It never hides the rest of the fleet.

| Capability | local QEMU | KubeVirt | Incus | libvirt | Proxmox VE | Corral peer |
|---|---:|---:|---:|---:|---:|---:|
| Aggregate inventory | yes | yes | yes | yes | yes | yes |
| Start, stop, delete | yes | yes | yes | yes | yes | relayed |
| Create | yes | yes | yes | yes | yes | remote API |
| SSH | direct when address is known | direct, then console transport | `incus exec` | not yet; use plain `ssh` | direct when address is known | direct first, relay fallback |
| Browser console | VNC | virtctl VNC | terminal | VNC/virt-viewer | tickets implemented | direct first, relay fallback |
| Snapshots, migration, hotplug | backend-limited | yes | partial, see below | partial, see below | yes | advertised by remote |
| Backend doctor | yes | yes | yes | yes | yes | tracked in #135 |

Use `corral context get`, `corral context use NAME`, and the persistent
`--context NAME` / `--backend NAME` flags. Bare VM names work only when unique;
scripts should use the canonical ID printed by `corral list`.

First-party workflow plugins have a separate support matrix in
[first-party-plugins.md](first-party-plugins.md). Plugins must declare their
supported backends; “installed” is not evidence that a workflow works on every
inventory target.

Tailscale is a first-class endpoint discovery/exposure option, not a required
network. KubeVirt ingress remains implementation-agnostic. Federation tries
advertised/direct guest endpoints first and uses Corral-to-Corral HTTP and
WebSocket relay only when the network cannot reach the guest directly.

Setup guides for the local and remote backends:
[Incus](incus-backend-setup.md), [libvirt](libvirt-backend-setup.md), and
[KubeVirt and Proxmox](kubevirt-proxmox-setup.md).

## Tier 3: enterprise backends

VirtualBox, VMware vSphere, XenServer/XCP-ng, and Nutanix are Tier 3
backends. CI runs their fixture tests only; no live backend runs in CI. See
[enterprise-readiness.md](enterprise-readiness.md) for the tiers and
[RFC-0003](rfc/0003-enterprise-backends.md) for the design.

| Capability | VirtualBox | vSphere | XenServer/XCP-ng | Nutanix |
|---|---:|---:|---:|---:|
| Aggregate inventory | yes | yes | yes | yes |
| Start, stop, restart, delete | yes | yes | yes | yes |
| SSH | NAT forward or Guest Additions address | not yet | not yet | not yet |
| Pause, resume | yes | yes | yes | no |
| Snapshots | yes | yes | yes | yes |
| Clone | yes | yes | yes | yes |
| CPU and memory change | powered off only | yes | not yet | not yet |
| Migration | no, one host | yes | yes | yes |
| Export | OVA, powered off only | not yet | XVA, halted or suspended only | not yet |
| Backend doctor | yes | yes | yes | yes |

[backend-parity.md](backend-parity.md) has the full per-operation matrix.

## Incus and libvirt: snapshots, migration, and hotplug

An operation is *unified* when Corral reaches it through one shared contract
(`pkg/backend` or `pkg/snapshot`). The CLI, the TUI and the web UI all call
that contract. A unified operation behaves the same way on every backend
that supports it. On KubeVirt, all operations in this section have this
property. On Incus and libvirt, only some operations have it.

The table shows what works today. "Not yet" means that the backend can do
the operation, but Corral does not call it yet.

| Operation | CLI | TUI | Web UI |
|---|---|---|---|
| Snapshot create, list, restore, delete | not yet: `corral snapshot` is KubeVirt-only | yes | REST API yes; the tab needs a KubeVirt VolumeSnapshotClass |
| Cold move to another backend (`corral move`) | yes | yes | yes |
| Cold move onto Incus or libvirt | yes | yes | yes, when a context for the destination exists |
| Live migration (`corral migrate`) | not yet | not yet | not yet |
| CPU and memory change (`corral scale`) | not yet | not yet | not yet |
| Disk hotplug (`corral adddisk`, `corral rmdisk`) | not yet | not yet | not yet |
| NIC hotplug (`corral addnic`) | not yet | not yet | not yet |

### Snapshots

The shared snapshot contract (`pkg/snapshot`) has adapters for Incus and
libvirt ([#134](https://github.com/tuna-os/corral/issues/134)):

- **Incus** runs `incus snapshot create`, `incus snapshot restore` and
  `incus snapshot delete`.
- **libvirt** runs `virsh snapshot-create-as --atomic`,
  `virsh snapshot-revert` and `virsh snapshot-delete`.

Each snapshot records its consistency. A snapshot of a stopped instance is
`offline`. A snapshot of an instance that runs is `crash`, because nothing in
the guest stops writes. The list reads this back from the backend: libvirt
records the domain state in `virsh snapshot-info`, and Incus keeps
`volatile.last_state.power` in the snapshot config.

The CLI (`corral snapshot create|ls|restore|rm`), the TUI Snapshots view and
the web Snapshots tab all use the same adapters. The web tab needs no
Kubernetes cluster for Incus and libvirt; only KubeVirt snapshots need a
VolumeSnapshotClass, and only KubeVirt VMs get a snapshot schedule.

A restore refuses an instance that runs, with `stop it first: corral stop NAME`.

### Migration

`corral migrate` is live migration between nodes of one backend. On Incus and
libvirt it stops with `migrate is only supported for KubeVirt VMs`. The
backends can do this (`incus move`, `virsh migrate --live`), but Corral does
not call them yet.

`corral move` is a cold move between backends. It works with Incus and
libvirt as a source and as a destination:

```bash
corral move web --to libvirt --dry-run
corral move web --to incus --to-context lab
```

The guest stops for the whole move and gets a new MAC address. A libvirt
source must use a local URI, because Corral reads the disk file. Incus
profiles and device overrides do not move with the instance.

### Hotplug and resize

`corral scale`, `corral adddisk`, `corral rmdisk` and `corral addnic` stop
with `<command> is only supported for KubeVirt VMs` on Incus and libvirt.
Until Corral supports them, use the native tools:

| Change | Incus | libvirt |
|---|---|---|
| CPU and memory | `incus config set NAME limits.cpu=4 limits.memory=8GiB` | `virsh setvcpus`, `virsh setmem` |
| Add or remove a disk | `incus storage volume attach`, `incus storage volume detach` | `virsh attach-disk`, `virsh detach-disk` |
| Live migration | `incus move` | `virsh migrate --live` |

### Roadmap and tracking

[backend-parity.md](backend-parity.md) lists every gap for each backend,
with the native command that would close it. The code generates that
table from `pkg/backend.Matrix`, and conformance tests keep it correct.

- [#214](https://github.com/tuna-os/corral/issues/214): CLI and web
  commands that check for `kubevirt` directly instead of the backend
  contract. This is the root cause of the CLI and web gaps above.
- [#492](https://github.com/tuna-os/corral/issues/492): `corral move --to`
  help text does not list `incus`.
