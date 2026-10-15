# VDI plugin — desktop pools

`corral-vdi` is Phase 1 of [RFC-0001](rfc/0001-vdi-plugin.md). It
gives static desktop pools with admin (CLI-driven) assignment, and it builds
each pool from clones of an already-built VM. `corral web` adds the Phase 2
[self-service broker](#self-service-broker): an authenticated user claims,
starts, and releases their own desktop without an admin. Reclaim and
one-click connect are later Phase 2 work, which
[issue #69](https://github.com/tuna-os/corral/issues/69) tracks (see also
[VDI Epic Status](vdi-epic-status.md)). What's here is real and cluster-tested:
create a pool, hand a member to a user, connect, release it, delete the pool.

This page is about pools of VMs. For pools of ephemeral Linux desktops that
Corral builds from a container image, see [CT-backed pools](vdi-ct-pools.md).

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
the identity, acquisition/renewal time, and a one-hour expiry. The VM keeps
presentation state. The `corral.dev/vdi-assigned-to` annotation holds the
exact identity. The label of the same name holds a label-safe copy
(`alice@example.com` becomes `alice_example.com`).
`corral.dev/vdi-claimed-at` holds the claim time.

Existing Phase 1 pools have
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

## Self-service broker

`corral web` serves an optional self-service API on `/api/vdi/`
([ADR-0013](adr/0013-vdi-broker-placement.md)). An authenticated user
claims a desktop from a pool, starts it, inspects it, and releases it. Admins
still create pools with `corral vdi pool create` or `corral vdi ct-pool apply`.
The broker serves pools of VMs and [CT-backed pools](vdi-ct-pools.md).

### Configuration

| Variable | Meaning |
|---|---|
| `CORRAL_VDI_BROKER=1` | Turns the broker on. When it is off, every `/api/vdi/` route answers 404. |
| `CORRAL_VDI_NAMESPACES` | Comma-separated namespaces whose pools the broker serves. Default: Corral's default namespace. The broker ignores pool labels in any other namespace. |
| `CORRAL_ADMINS` | The admin allowlist of [ADR-0003](adr/0003-identity-source.md). Admins may inspect, start, and release any user's desktop. |
| `CORRAL_AUTH_REQUIRED=1` | Recommended. Refuses every anonymous `/api` call, not only the broker's. |

### Permissions

The service account of `corral web` needs these permissions in each served
namespace. `deploy/corral-web.yaml` grants the VM and Lease rules.

- `coordination.k8s.io` `leases`: `get`, `list`, `create`, `update`,
  `delete`. One Lease per member is the claim.
- `kubevirt.io` `virtualmachines`: `get`, `list`, `patch`, and the `start`
  and `stop` subresources, for VM pools.
- For CT pools: `configmaps` `get`/`list`, and the `pods`, `services`, and
  `persistentvolumeclaims` permissions that `corral vdi ct-pool reconcile`
  uses, because a CT release destroys and recreates the member.

### API

A desktop has three stable identifiers. The pool ID is `kind/namespace/pool`,
where `kind` is `vm` or `ct`. The desktop ID is `kind/namespace/member`, and
it stays the same across claims. The session ID changes with every claim of
the member.

| Route | Who | Result |
|---|---|---|
| `GET /api/vdi/pools` | any user | The served pools with member, claimed, and free counts. |
| `POST /api/vdi/pools/{kind}/{ns}/{pool}/claim` | any user | Claims a member for the caller and starts it. A retry returns the desktop the caller already holds. 409 when the pool has no free member. |
| `GET /api/vdi/desktops` | any user | The caller's desktops. `?all=1` lists every user's desktops (admin only). |
| `GET /api/vdi/desktops/{kind}/{ns}/{member}` | holder or admin | One desktop. |
| `POST /api/vdi/desktops/{kind}/{ns}/{member}/start` | holder or admin | Starts the desktop; a running desktop is left alone. |
| `POST /api/vdi/desktops/{kind}/{ns}/{member}/release` | holder or admin | 202 with a task ID (`GET /api/tasks/{id}`). A VM member stops and keeps its disks; a CT member is destroyed and recreated from its image. |

A claim whose member fails to start still succeeds. The response carries
`startError`, the caller keeps the desktop, and a `start` retries. A
release that fails keeps the claim, so the holder can retry it. The task
log (`GET /api/tasklog`) records every claim, start, and release as
`vdi claim`, `vdi start`, `vdi release`, or `vdi force-release` (an admin
released another user's desktop).

### Threat boundaries

- **Identity comes only from the request pipeline.** The broker takes the
  login from Tailscale or from the `corral-auth` gateway
  ([ADR-0006](adr/0006-optional-auth-gateway-plugin.md)). A request body or
  query never names the user, so nobody can claim or release as someone
  else. The headers are trustworthy only if every path to `corral web` goes
  through that proxy; [ADR-0003](adr/0003-identity-source.md) describes the
  guarantee.
- **Anonymous callers and Corral peers get nothing.** A request without an
  identity gets 401. A request with the peer token is a service identity,
  not a user, and gets 403.
- **Ownership is the Lease holder.** Only the holder or an admin may
  inspect, start, or release a desktop. A release names the holder that the
  broker checked. The pool's release primitive refuses a claim that changed
  in between.
- **Open mode has no ownership.** Without `CORRAL_ADMINS`, every caller is
  an admin and may release any desktop. Set an allowlist before you offer
  the broker to more than one person.
- **Console routes predate the broker.** The broker does not yet gate the
  console websockets of a claimed member. The one-click connect work (#229)
  adds that gate. Until then, restrict console access with the ingress or the tailnet
  ACLs if desktops must be private.

### Example

```bash
# Through the Tailscale ingress, curl carries the caller's tailnet identity.
curl -X POST https://corral.example.ts.net/api/vdi/pools/vm/corral-vms/devpool/claim
#   {"id":"vm/corral-vms/devpool-1","sessionId":"5f0c…","poolId":"vm/corral-vms/devpool",
#    "owner":"alice@example.com","phase":"Running","running":true,…}
curl -X POST https://corral.example.ts.net/api/vdi/desktops/vm/corral-vms/devpool-1/release
#   {"desktop":"vm/corral-vms/devpool-1","task":"vdi-release-corral-vms-devpool-1-…"}
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
  capacity, or a feature-gate that the golden VM's spec needs. The claim
  stays on the member when the start fails, so a second user does not get
  it. Retry the start after you fix the cause (the broker's `start` route,
  or `corral start <member>`), or run `corral vdi unassign` to back out.
- **A release fails** — the member stops before its claim ends. If the stop
  fails, the holder keeps the claim and can send the release again.

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

- **Self-service is a web API.** The [broker](#self-service-broker) has no
  "get a desktop" page in the web UI yet. The one-click connect work (#229)
  adds it.
- **No idle/logout reclaim.** An unassigned member never stays on, because
  unassign always stops it. But nothing notices "alice hasn't touched
  devpool-1 in 3 hours" and reclaims it automatically.
- **No live resize.** To grow or shrink a pool, delete and recreate it. Or
  clone one more member manually and label it by hand.
- **CT-backed pools are a separate command.** `corral vdi ct-pool` manages
  ephemeral pools of Containers (CT) with a declared size and a reconciler.
  See [CT-backed pools](vdi-ct-pools.md).

None of this is a secret. See [RFC-0001](rfc/0001-vdi-plugin.md) for the
full phased plan, and [docs/vdi-epic-status.md](vdi-epic-status.md) for the
hardware gating and dependency chain. See
[issue #69](https://github.com/tuna-os/corral/issues/69) for the work that
the project tracks next.
