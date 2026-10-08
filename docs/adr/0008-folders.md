# ADR-0008: Folders — a fleet hierarchy Corral owns

**Status:** proposed
**Date:** 2026-07-31

## Context

Corral aggregates a heterogeneous fleet. It has QEMU VMs on the workstation,
and KubeVirt VMs and pet-pod CTs on one or more clusters. It also has Incus
instances on remotes, libvirt domains behind SSH URIs, and whole other Corrals
that join as peers. The UIs group that fleet by *where it runs* — datacenter →
node → VM in the web tree, context filters in the TUI.

Operators do not think about their fleet that way. They think in
**application stacks**: "the web stack is `web-prod`, `db-prod`, and the
`files` CT". They think in **SLA tiers**: "these six can reboot whenever;
these two need a window". And they think in **environments**. None of those
line up with a backend, a context, or a node. A stack often spans a cluster
VM, a local dev VM, and a CT.

What exists today does not cover it:

- **Tags** (#tags, `corral.dev/tag.<name>` labels) are KubeVirt-only. A
  qemu VM, an Incus instance, and a libvirt domain cannot carry one. That
  mix is exactly the heterogeneity a stack has. Tags are also flat and
  multi-valued. That is good for a filter, but wrong for "what is the scope
  of this reboot".
- **Contexts** are backend universes. Corral discovers them; an operator
  does not declare them. An operator cannot put two contexts' instances in
  one group.
- **Bulk actions** exist in the web UI, but only over an ad-hoc
  multi-select that vanishes on reload. There is no way to name a group and
  come back to it.

The requested shape is a folder tree: nestable, drag-and-drop, with bulk
actions per folder ("reboot all"). The longer-term prize is explicitly not
in this ADR's scope. A named, durable group is the natural place to hang a
**policy**: a backup schedule, a snapshot retention rule, a permitted
downtime window.

The design question this ADR has to answer is not the UI. It is **where
folder membership lives**. This decides whether folders can span
backends at all, and whether two operators see the same tree. It also
decides whether the future policy layer has anything durable to attach to.

## Decision

### Folders are Corral's own object, keyed by canonical instance reference

A folder is a **path** (`prod`, `prod/web-stack`) and a set of members,
each a `types.InstanceRef` — the identity that already carries peer,
backend, context, namespace, and name. That reference lets a folder hold a
KubeVirt VM, a local qemu VM, an Incus container, and a CT at once. It is
the one identity that every backend already produces.

```yaml
folders:
  - path: prod
  - path: prod/web-stack
    members:
      - kubevirt/talos/corral-vms/web-prod
      - kubevirt/talos/corral-vms/db-prod
      - ct/talos/corral-vms/files
  - path: lab
    members:
      - qemu/local//dev-fedora
```

Decisions that follow from that shape:

- **The path sets the hierarchy, not a parent pointer.** `prod/web-stack` is a
  child of `prod` because of its name. A drag re-parents a folder. To
  re-parent is to rename a prefix: one write, not a tree walk.
- **A folder exists independently of its members.** An empty folder is a
  declared object. So an operator can create it before they drag anything
  into it, and a drag has somewhere to land. This is why the document lists
  folders, and is not a map from instance to folder.
- **One folder per instance.** Folders are a tree, not tags. The scope of
  "reboot this folder" has to be unambiguous. The same is true for any
  future window or retention rule. Tags stay as they are: orthogonal and
  multi-valued, for filters. If an instance is in two stacks, that is an
  error in the model. The UI should refuse it, not represent it.
- **Stale members: tolerate on read, prune on write.** A partial fleet is
  normal for Corral (see the partial-fleet warnings). A stopped or
  unreachable VM, or a VM on a context that is down, must not silently lose
  its folder. The UI shows a member that resolves to nothing as missing.
  Corral drops it only when the operator edits that folder.

### Membership lives in Corral's own state, not in backend labels

Locally, the folder document lives in `~/.config/corral/config.yaml`
alongside contexts and peers. When `corral web` runs in-cluster, the
document lives in a ConfigMap. That is **the same split that image sources
already use**. So there is precedent, a proven pattern, and no new storage
concept.

The scope conversation left this decision to this ADR. This ADR picks it
over the alternatives below because it is the only option that covers the
whole fleet on day one. Backend-native labels can hold a folder for a
KubeVirt VM and a CT. Config keys can do it for Incus, and domain metadata
can do it for libvirt. But a local qemu VM that systemd units manage has
nowhere to put one, and local VMs are half of why Corral exists. A feature
that groups the fleet but silently cannot hold a local VM fails at its one
job.

Here is the plain cost: **the tree is per-Corral, not per-instance.** Two
operators with separate configs see separate trees. And an instance carries
no record of its folder if you look at it through `kubectl`. That is
acceptable, because a folder is an *operator's* view of the fleet, as
contexts and peers already are. The shared case already has an answer in the
in-cluster deployment, where the ConfigMap is the shared tree.

### Bulk actions fan out server-side, with per-member results

`POST /api/folders/{path}/{action}` applies a power action to every member
and returns a per-member outcome. The UI does not loop over per-VM
endpoints. Three reasons:

1. Partial failure is the normal case in a heterogeneous folder. An Incus
   container may refuse an action that is valid for a KubeVirt VM. One
   response that says which members did what is honest; a pile of toasts
   is not.
2. Each instance already has its own capability gates. A fan-out endpoint can
   report "not applicable here", and the UI does not have to derive it
   again.
3. The eventual scheduler needs a server-side executor for exactly this
   operation. If we build it now, the policy layer inherits it and does not
   grow a parallel path.

The same `types.InstanceCapabilities` that the single-instance paths use
also gate folder actions. This slice does **not** offer destructive actions
(delete) as a folder operation.

### Surfaces

- **TUI**: folders group the fleet list and can collapse. The same
  capability-aware action menu opens on a folder to act on its members.
  Keyboard first. This work also adds mouse support, which makes
  click-to-expand and drag-to-move possible later.
- **Web**: folders become a branch of the existing tree. Drag-and-drop
  re-parents a member (a `PUT` of the member's folder path). Each folder
  has its own action toolbar.

## Consequences

- One new package (`pkg/folder`) owns the document, path validation,
  re-parent operations, and membership. The surfaces stay thin over it.
- `GET /api/v1/inventory` gains a folder path per instance, so a client can
  render the tree without a second round trip.
- The Proxmox compat layer has a natural mapping to lean on later. PVE
  **pools** are close to this shape, but this ADR does not claim it.
- Peers: a folder may hold another Corral's instances, since an
  `InstanceRef` carries the peer. The folder document stays local to the
  Corral that renders the tree. Corral does not ask peers about their
  folders.

## Alternatives considered

**Backend-native labels** (`corral.dev/folder=prod/web-stack` on KubeVirt
VMs and CT pods, Incus config keys, libvirt domain metadata). Durable with
the instance and shared between operators for free. We rejected it as the
primary store, because local qemu has no home for it. The feature would be
unavailable exactly where people most often run Corral first. And every
backend needs its own read/write/list path.

**Hybrid** — labels where they exist, config for the rest. Most durable,
but it has two sources of truth to reconcile. In a feature that groups
instances, a reconciliation bug shows up as instances that silently jump
folders. If demand to share grows, Corral can later *mirror* the config
document to labels as a one-way export. The model stays the same.

**Tags as folders** — synthesise a hierarchy from tag values like
`folder/prod/web`. No new storage, but it inherits the KubeVirt-only limit,
allows an instance in two folders, and overloads a filter primitive with
tree semantics.

## Not in scope

Backup and downtime policy. A folder is the object those policies will
attach to. `pkg/snapshot`'s contract (per-instance consistency reports and
typed refusals) is the mechanism they will drive, and the snapsched plugin
will consume both. The design of the policy object is a separate ADR: windows,
retention, and what happens when a member cannot honour it. That design
should not constrain the folder shape beyond two decisions above: "one
folder per instance" and "server-side fan-out". Those decisions exist so
that it can.
