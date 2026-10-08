# VDI plugin — desktop pools

`corral-vdi` is the Phase 1 implementation of
[RFC-0001](rfc/0001-vdi-plugin.md). It gives static desktop pools with manual
(CLI-driven) assignment, and it builds each pool from clones of an
already-built VM. It has no broker, no self-serve web page, and no idle reclaim
yet. Those are later phases, and
[issue #69](https://github.com/tuna-os/corral/issues/69) tracks them (see also
[VDI Epic Status](vdi-epic-status.md)). What's here is real and cluster-tested:
create a pool, hand a member to a user, connect, release it, delete the pool.

## Install

```bash
corral plugin install vdi
```

Or build from source (same repo, no separate checkout):

```bash
go build -o ~/.local/share/corral/plugins/corral-vdi ./cmd/corral-vdi
```

## Mental model

A pool is **not** a new kind of object. It is a set of VMs with a
`corral.dev/vdi-pool=<name>` label. There is no pool CRD, no controller that
watches anything, and no reconciliation loop. `corral vdi pool create` clones
N VMs and labels them; `corral vdi pool list` reads the labels back;
`corral vdi pool delete` deletes the labeled VMs. If you are ever unsure
what a command did, `kubectl get vm -n <ns> -l corral.dev/vdi-pool` shows you
the truth directly. There is no other state to go stale.

A Kubernetes `coordination.k8s.io/v1` Lease named `corral-vdi-<member>` holds
the assignment ownership. Lease creation is first-writer-wins. Corral replaces
a stale Lease with its `resourceVersion`, so concurrent claimants cannot select
the same member.

The Lease records the pool/member in labels,
the identity, acquisition/renewal time, and a one-hour expiry. The
`corral.dev/vdi-assigned-to=<user>` label plus `corral.dev/vdi-claimed-at`
annotation remain presentation state on the VM. Existing Phase 1 pools have
no Lease and remain visible; their first atomic claim creates one. A legacy
`unassign` can clear label-only members, but only its owner can release an
active Lease.

## Prerequisites

- A **golden VM**: an already-built VM that works, which you want to copy.
  Build it the normal way:
  - Desktop Linux: `corral bootc create mydesktop --image ghcr.io/ublue-os/bluefin:latest`
  - Windows: `corral windows create mydesktop --iso <url>` (see the create
    dialog's ISO presets in the web UI, or `docs/api.md`'s note on
    `POST /api/vms` with `windows: true`)
  - Anything else: `corral create mydesktop --kubevirt ...`
- The golden VM's clone needs whatever the source needs — same
  StorageClass availability, same node placement constraints. `corral
  doctor` catches most of this before you find out the hard way.
- **KubeVirt's clone feature** needs a `VolumeSnapshotClass` for
  persistent-disk VMs (the same requirement `corral clone`/snapshot
  already has — `corral doctor` flags a missing one).

## Walkthrough

```bash
# 1. Build (or reuse) a golden VM. This one's a normal corral bootc VM.
corral bootc create golden-desktop --image ghcr.io/ublue-os/bluefin:latest
corral start golden-desktop
# ...customize it however you like (install packages, configure things)...
corral stop golden-desktop   # clone from a stopped VM for a clean disk state

# 2. Create a pool of 3 clones.
corral vdi pool create devpool --from golden-desktop --size 3
#   pool "devpool" created: 3 members
#     devpool-1
#     devpool-2
#     devpool-3

# 3. See what's in it.
corral vdi pool list
#   devpool  (ns/corral-vms, 3 members)
#     devpool-1                free                     stopped
#     devpool-2                free                     stopped
#     devpool-3                free                     stopped

# 4. Hand one to a user. This also starts it if it was stopped.
corral vdi assign devpool alice
#   assigned devpool-1 → alice
#   connect:  corral vdi connect devpool-1

# 5. Connect. Prints every reachable path — pick whichever fits the guest.
corral vdi connect devpool-1
#   VNC (browser or client):  corral web  →  open devpool-1  →  Console
#   RDP (if the guest answers on 3389):  corral viewer devpool-1  (or a native RDP client via virtctl port-forward)
#   SSH (Linux guests):  corral ssh devpool-1

# 6. Native USB Redirection (smartcards, security keys, YubiKeys).
#    List local USB devices available on the client:
corral vdi usb list
#    Redirect a selected device to the running assigned desktop (requires virtctl):
corral vdi usb redir devpool-1 --device 1050:0407 --user alice

# 7. When alice is done, release it. This stops the VM too — pooled
#    desktops don't stay running unclaimed.
corral vdi unassign devpool-1

# 8. Tear the whole pool down when you're finished with it.
corral vdi pool delete devpool
```

## Native USB Redirection

`corral vdi usb redir <member>` redirects a local USB device from the operator's machine straight to the guest via `virtctl usbredir`. This wraps the native USB redirection of KubeVirt, not legacy SPICE.

- **Use cases**: Hardware security keys (YubiKey / FIDO2), smartcards (CAC/PIV), USB tokens, and external peripheral passthrough.
- **Prerequisites & constraints**:
  - Needs the `virtctl` client binary on the local machine.
  - The desktop member must be **assigned**, and it must **run** in KubeVirt.
  - An ownership authorization check (`--user`) ensures that a user can redirect local devices only to their own desktop.
  - KubeVirt VMs support USB redirection; CT and non-KubeVirt backends report actionable unsupported errors.
  - Before it attaches a device, the command displays the security implications (host device exclusivity, migration blockage, guest trust boundaries).


## What "connect" actually does today

`corral vdi connect <member>` prints instructions — it does **not** yet
pick a protocol and open a session for you. That one-click behavior is
Phase 2 territory (see the RFC). It specifically depends on
[ADR-0002](adr/0002-browser-rdp-via-ironrdp.md) phase 2 (in-browser RDP),
which must land first. Then Windows members get the same one-click experience
that VNC already gives Linux members. Until then:

| Guest | How to actually connect |
|---|---|
| Any VM (VNC always works) | `corral web` → open the VM → Console tab (noVNC in the browser) |
| Linux VM with RDP configured | `GET /api/vms/{ns}/{name}/rdp` (or the Summary panel) tells you if 3389 answers; connect with a native RDP client via `virtctl port-forward` |
| Windows VM | Same RDP path as above — `corral-windows`-created VMs expose RDP through the proxy service if you passed `--rdp` at create time |
| Any VM with SSH | `corral ssh <member>` |

## Troubleshooting

- **`golden VM "X" not found`** — the `--from` VM doesn't exist in the
  target namespace. `corral vdi pool create` does not search other
  namespaces. Pass `-n` to match the namespace where the golden VM lives.
- **`timed out ... waiting for the clone to produce VM`** — the clone
  controller of KubeVirt did not produce the target VM within 2 minutes. Check
  `kubectl get virtualmachineclone -n <ns>` for the phase of the clone object.
  A stuck clone almost always has one of two causes. The first is a
  StorageClass/VolumeSnapshotClass issue (see Prerequisites above). The second
  is a PVC on the source VM that is not snapshottable. This is a real failure
  mode that we found live. The RFC's commit history shows the bug that
  motivated the wait/timeout logic.
- **`pool has no free members`** — every member already has a user, or
  another claimant won its Lease race. Either `corral vdi unassign` one, or
  `corral vdi pool create` a bigger pool. There is no live resize yet, so
  delete and recreate the pool, or create a second pool.
- **A claim is stuck after a partial failure** — inspect
  `kubectl get lease -n <ns> corral-vdi-<member> -o yaml`. Corral
  intentionally keeps the Lease if the label update or the VM startup fails.
  This way, a second user does not get a half-configured desktop. The owner can retry
  `corral vdi assign`; after the one-hour expiry an administrator can retry
  and recover the member.
- **`insufficient device capacity for "X"` or `device admission failed`** —
  The golden VM asks for host devices (PCI passthrough, mediated vGPU, SR-IOV).
  But the capacity of the cluster cannot satisfy `size * per_vm_devices` across
  the nodes. `corral vdi pool create` validates the allocatable devices, minus
  the active VMI allocations. It refuses to create the pool up front, before
  any partial clones exist.
- **Assigned member won't start** — `corral vdi assign` shows the
  `virtctl start` error directly. Typical causes are a lack of cluster
  capacity, or a feature-gate that the golden VM's spec needs. The assignment label stays on
  the VM even if the start failed. Run `corral vdi unassign` to back out, and
  retry after you fix the cause.

## GPU & Host-Device Capacity Validation

When a golden VM asks for host devices or GPUs (via `gpus` or `hostDevices` in its spec), `corral vdi pool create` does pre-flight capacity admission:
1. **Device type classification**: Inspects the KubeVirt configuration. It distinguishes between exclusive PCI passthrough (`pciHostDevices`), mediated vGPU devices (`mediatedDevices`), and external device providers (vGPU/SR-IOV).
2. **Allocatable vs. Allocated totals**: Queries the allocatable resources per node, and the active VMIs, to compute the actual remaining capacity.
3. **Concurrency & Topology constraints**: Ensures that the total device count is enough. Also ensures that node placement can satisfy multi-device requirements per VM.
4. **Actionable failure reports**: Refuses impossible pools before any mutation and reports resource shortages with per-node availability breakdowns.

### Hardware Limitations
- **Single-VM Full Passthrough**: Some physical GPUs attach via full PCI passthrough (e.g., consumer GPUs without SR-IOV/vGPU support). Each such GPU is strictly 1:1 exclusive to a single VM instance that runs. A pool of size $N$ needs $N$ distinct physical GPUs that are allocatable across the cluster nodes.
- **Mediated / vGPU Shared Use**: Mediated devices (NVIDIA GRID/vGPU, Intel GVT-g) allow multiple pool members per physical card. The configured profile limit sets the maximum.

## Current limitations (by design, not bugs)

- **No self-serve.** Assignment is a CLI/admin action — there's no
  end-user "get a desktop" page yet. The Lease primitive is ready for the
  later broker, but the broker is not part of this plugin slice.
- **No idle/logout reclaim.** An unassigned member never stays on, because
  unassign always stops it. But nothing notices "alice hasn't touched
  devpool-1 in 3 hours" and reclaims it automatically.
- **No live resize.** To grow or shrink a pool, delete and recreate it. Or
  clone one more member manually and label it by hand.
- **CT-backed pools aren't implemented.** Only VM (KubeVirt) golden
  sources work today. Pools of Containers (CT) are exploratory (RFC's Phase
  4 territory).

None of this is a secret. See [RFC-0001](rfc/0001-vdi-plugin.md) for the
full phased plan, and [docs/vdi-epic-status.md](vdi-epic-status.md) for the
hardware gating and dependency chain. See
[issue #69](https://github.com/tuna-os/corral/issues/69) for the work that
the project tracks next.
