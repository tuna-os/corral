# RFC-0001: VDI plugin — Windows/Linux desktop pools on Corral

**Status:** Phases 0 and 1 implemented. Browser RDP via IronRDP/RDCleanPath
and the `corral-vdi` static-pool CLI have shipped. Phases 2–4 remain a
design proposal; this RFC defines their corrected scope but does not commit
their implementation. Setup guide: [docs/vdi.md](../vdi.md), epic status: [docs/vdi-epic-status.md](../vdi-epic-status.md).
**Date:** 2026-07-02
**Updated:** 2026-07-29
**Author:** grilled out of a live session with James Reilly + Claude

## Summary

Add a `corral-vdi` plugin that turns Corral's existing VM/Container machinery
into a small, self-hosted **Virtual Desktop Infrastructure**. It provides pools
of Windows or Linux desktops that Corral assigns to users on request. Users
reach them through the browser, and Corral reclaims them after an explicit
release or under a conservative session policy. It is not a Citrix/Horizon
replacement. It is a homelab-to-small-team-scale VDI that keeps optional
broker/auth concerns in plugins and works through Tailscale or another private
ingress. This follows the scope discipline that keeps Corral's core lean.

## Why now

Corral already has almost every *component* a VDI stack needs, built for
other reasons:

| VDI need | Already exists in Corral as |
|---|---|
| Remote display in the browser | noVNC bridge (`/api/vnc/{ns}/{name}`), xterm.js serial bridge |
| Browser RDP | IronRDP over the RDCleanPath bridge, plus RDP detection and raw websocket transport (ADR-0002 phases 1 and 2) |
| Windows guest provisioning | `corral-windows` plugin — UEFI/TPM/virtio, driver ISO |
| Full desktop Linux images | `corral bootc` — builds Universal Blue/Bluefin/TunaOS desktop images on-cluster from a container image |
| Lightweight ephemeral Linux sessions | Containers (CT) — `pkg/ct`, distrobox-style persistent rootfs |
| Reachability | direct-first console routing, Tailscale exposure, ingress-agnostic KubeVirt Services, and Corral-peer relay fallback |
| Identity | trusted Tailscale identity or the optional OIDC auth-gateway plugin (ADR-0003/ADR-0006) |
| Cluster capability gating | `corral doctor` — GPU/PCI passthrough check, StorageClass checks |
| Replica pools of identical VMs | KubeVirt's own `VirtualMachinePool` CRD |

What's **missing** is the thin layer that turns "a pool of VMs" into "VDI".
That layer is **assignment** (which user gets which desktop) and **lifecycle
policy** (power on demand, reclaim on idle/logout). It also includes **one
connect button** that picks the right protocol per desktop. That layer, not
a new console layer and not a new hypervisor integration, is the actual scope
of this RFC.

## What we're explicitly *not* building

These exclusions come from the research (full brief: link in the issue that
tracks this RFC). They are deliberate, not oversights:

- **Not a new remote-display protocol.** noVNC/RDP-over-websocket stay. SPICE
  is dead upstream. Per a 2025 kubevirt-dev thread, QEMU is on track to drop
  SPICE server support entirely. Do not build on it.
- **Not a general-purpose connection broker as a product.** oVirt's
  engine+portal architecture and Leostream (commercial, KubeVirt-aware since
  2025) are the reference points for the *pattern* (pool + ticket +
  assignment). They are not something to run alongside Corral as a second
  control plane.
- **Not a fix for Windows license rules.** Pooled/multi-session Windows needs
  Enterprise E3/E5 or RDS CALs. This is a customer-facing constraint that this
  RFC flags and then leaves. It is not a technical problem that Corral can
  solve.
- **Not a GPU virtualization layer.** KubeVirt and the GPU Operator from
  NVIDIA already handle vGPU/mediated-device passthrough. `corral doctor`
  already checks for it (this session's GPU/PCI passthrough check). Reuse,
  don't rebuild.

## Prior art, and how this differs

- **oVirt** (community-maintained, with releases still in 2026): proves the
  pool+ticket broker pattern. It is not Kubernetes-native, so it is not
  directly reusable; only the pattern is.
- **Leostream**: commercial, added OpenShift Virtualization/KubeVirt support
  ~end of 2025. It validates "an external broker drives the KubeVirt API" as a
  real architecture. Someone who solves the same problem commercially chose
  it. Closed-source: differentiate from it, don't depend on it.
- **Kasm Workspaces**: as of v1.16, it provisions *real VMs* via
  Harvester/KubeVirt in addition to its container-native workspaces. It
  independently came to the same "containers for ephemeral, VMs for
  persistent" split. This RFC proposes that split for Corral's CT + bootc
  combination. It is the strongest external validation we found that the
  dual-workload approach isn't a hack.
- **Selkies** (Google-originated, actively developed): WebRTC desktop streams
  with GPU acceleration, container-first. It is not a drop-in noVNC
  replacement for VM consoles. It streams from inside a pod, not from a VM
  framebuffer. Its prebuilt container images for desktops are strong
  candidates for what runs *inside* an ephemeral desktop pool on Corral CTs.
  These images include KDE Plasma, Wine/Proton, and GPU passthrough. Reuse the
  images, not the whole project.
- **FOSDEM 2026 "VDI and KubeVirt"** talk (KubeVirt maintainer, USB
  redirection/console work). It proposes the D-Bus display interface of QEMU
  as a pluggable remote-display seam. External bridges (Guacamole, Selkies,
  custom) can then attach, and they do not have to own the whole console
  pipeline. It has no shipped code yet. Track it: if it lands upstream, it may
  be a better long-term seam than `virtctl vnc --proxy-only` for phase 3+
  below.

## Design

### Concepts (candidates for CONTEXT.md once this settles)

- **Desktop Pool** — a named group of identical desktops (VM- or CT-backed).
  A pool has a template (bootc image / Windows ISO+answer-file / CT image), a
  target size, and a reclaim policy. For the VM-backed case, it builds on
  KubeVirt's `VirtualMachinePool`. CT-backed pools use a thin equivalent. No
  upstream CRD exists for pooled pods, so this is new code, but small.
- **Assignment** — an exclusive claim from an authenticated identity to one
  pool member. Identity can come from trusted Tailscale ingress or the OIDC
  auth gateway. VDI consumes the identity contract that results and does not
  depend on either provider directly. Phase 1's labels are presentation
  state, not a concurrency primitive. Self-service assignment must use an
  atomic Kubernetes operation (a per-member `Lease` or resource-versioned
  compare-and-swap). This way, no two simultaneous claims can receive the
  same desktop.
- **Session** — the active connection associated with an Assignment. An open
  websocket to the console proves only that a connection exists. It does not
  prove that the user actively types or moves the mouse. Reclaim policy has
  distinct inputs: disconnect time, explicit release, maximum session
  duration, and an optional activity signal from the guest.
- **Connect** — one button/command that finds the *actual* reachable protocol
  of a desktop and opens the right client path. Corral already has an RDP probe.
  Extend the same idea to answer "is this a VNC-only guest, RDP-capable, or a
  CT with only a terminal". Today the client paths are the existing
  noVNC/xterm.js bridges and in-browser IronRDP. Corral tries the direct path
  first: it uses an advertised guest or ingress endpoint when one is
  reachable. For complicated network topologies, it then falls back to a
  Corral-peer console relay.

### Phased plan

**Phase 0 — in-browser RDP prerequisite. Implemented.**
ADR-0002 phase 2 shipped: the web UI now embeds IronRDP and Corral provides the
RDCleanPath transport that IronRDP needs. Windows and RDP-enabled Linux
desktops can therefore use the same one-click experience in the browser as
VNC desktops.

**Phase 1 — static pools, manual assignment (CLI, no broker yet). Implemented.**
`corral vdi pool create <name> --from <golden-vm> --size N` clones an
*already-built* VM N times via `kubevirt.Client.Clone` and labels the clones
as pool members. You build the golden VM the normal way (`corral bootc`/
`corral-windows`/`corral create`), then customize and stop it.

`corral vdi assign <pool> <user>` hand-wires a claim through a per-member
Kubernetes Lease. The existing K8s label/annotation mirror the Lease as
presentation state. Lease creation is first-writer-wins, and stale recovery
uses a resource-versioned replacement. Existing label-only Phase 1 pools
migrate lazily when their first atomic claim creates a Lease.
`corral vdi connect <member>` prints the existing VNC/RDP/SSH paths for that
member. Full setup guide: [docs/vdi.md](../vdi.md).

The result differs slightly from the first draft above. It uses
`--from <existing-vm>` (clone a golden VM), not `--template <image>` (build N
from scratch). A clone directly reuses the already-tested primitive of
`corral clone`. It also matches how real VDI systems build pools: golden image
once, clone many. This avoids N repeated runs of a full bootc build or Windows
ISO install.

Live verification found a real bug, and we fixed it. `Clone()` returns as
soon as it applies the `VirtualMachineClone` CRD, not once the target VM
exists. On a real cluster, `CreatePool` originally raced ahead and tried to
label a VM that did not exist yet. The fix is a poll-wait (`waitForVM`, 2min
timeout) between clone and label.

**Phase 2 — atomic self-service claims, sessions, and reclaim. Proposed.**
An authenticated user hits "Get a desktop," atomically claims an available
member, and powers it on if necessary. Corral then sends the user to the best
reachable console. Claim creation must be compare-and-swap safe. The existing
list-then-label implementation of Phase 1 is not enough under concurrent
requests.

The first reclaim policy is deliberately conservative:

1. explicit "Release desktop" is authoritative;
2. console disconnect starts a configurable grace period;
3. a reconnect during the grace period cancels the reclaim;
4. a maximum session duration is an optional administrative ceiling; and
5. true input-idle reclaim needs an optional guest/protocol activity signal;
   Corral does not infer it merely from websocket age.

After reclaim, persistent pools stop the VM and retain its disks. Ephemeral
pools destroy and recreate the member from the golden source. CT-backed pools
remain a later sub-slice because they need their own reconciliation primitive.
This phase is the actual VDI broker, not another VM-management command.

**Phase 3 — capacity-aware GPU pools and native USB redirection. Proposed.**
Pool creation inspects the golden VM's host-device requests and compares the
requested pool size with allocatable matching devices. It must distinguish
exclusive passthrough from mediated/vGPU capacity. A generic "GPU present"
doctor result is not enough to promise concurrent desktops.

In this phase, the client type decides how USB redirection works. A native CLI command can
wrap `virtctl usbredir` for smartcards and security keys. Browser USB
redirection is a separate experimental feature. It needs WebUSB permission UX
and a purpose-built bridge. The CLI transport does not imply it, and it is not
part of the initial Phase 3 commitment.

**Phase 4 (exploratory, not committed) — WebRTC streams for ephemeral
Linux pools**. Selkies-style container images inside Corral CTs for
GPU-accelerated, low-latency ephemeral desktops, as an alternative to
noVNC for that specific pool type. The time-boxed
[decision record for the WebRTC spike](../research/vdi-webrtc-spike.md) now
defers implementation. It needs a representative cluster comparison against
VNC before work goes ahead. Track the FOSDEM D-Bus-display proposal before you
commit engineering time here, because it may change the right integration
seam.

## Feasibility, honestly

- **Phases 0 and 1 have shipped**. Phase 1 was low-risk assembly of existing
  Corral machinery (bootc, corral-windows, corral ct, KubeVirt's own
  `VirtualMachinePool`), not new hard problems.
- **Phase 2 claim selection is straightforward, but concurrency is not
  optional**. A Lease or compare-and-swap claim must precede VM startup.
- **Phase 2's genuinely hard part is input-idle detection**. Websocket
  open/close is useful session-presence data, but it is not user activity.
  Start with explicit release and disconnect grace. Add guest cooperation
  only when the operational value is worth the per-OS integration.
- **AMD's current driver/firmware support, not KubeVirt, limits Phase 3's GPU
  story**. We verified this directly against AMD's GIM/SR-IOV driver release
  notes (2026-07). Officially supported hardware is only the MI-series
  Instinct accelerators for datacenters. The one exception is a Radeon PRO
  card for workstations. There are no APUs and no consumer/integrated GPUs at all. The
  Strix Halo APU from AMD in `karnataka` is a full-GPU-passthrough-to-one-VM
  device today, not a multi-tenant vGPU one. An AMD engineer has said that
  client-GPU SR-IOV is "in the roadmap," with no committed timeline. Check
  this again before you commit to Phase 3, and do not assume that it is
  permanently impossible. Either way, this hardware is fine today for "one
  nice, accelerated desktop", but not for a GPU-accelerated multi-user pool.
- **Overall scope**: this is realistically a personal/small-team VDI, not
  an oVirt/Kasm/Leostream competitor. The phased plan fits what one person
  and Corral's existing components can ship in practice. It does not aim for
  feature parity with enterprise VDI.

## Open questions (for the grilling session)

1. **Claim primitive**: use a standard Kubernetes `Lease` per member, or a
   purpose-built Assignment CRD? A Lease gives atomic acquisition and expiry
   and adds no new API. A CRD gives clearer domain state and validation. This
   RFC rules out a local registry or an unguarded ConfigMap, because claims
   must be cluster-visible and concurrency-safe.
2. **CT-backed pool primitive**: KubeVirt gives us `VirtualMachinePool` for
   free; nothing upstream gives us a pooled-pod equivalent. Build our own
   (small: a label-based reconcile loop)? Or is this premature for CT pools
   specifically, and should we start VM-only?
3. **Reclaim defaults**: choose disconnect grace, maximum session duration,
   and whether an administrator may force-release a connected session. Input
   idle remains unavailable without guest/protocol cooperation.
4. **License UX**: does Corral only document the Windows license constraint
   (the current lean)? Or does it actively refuse to create pools above some
   size without an explicit `--i-have-licenses` flag?
5. ~~**Scope of "plugin"**~~ — **resolved for Phase 1**: single
   `corral-vdi` binary (`pool`/`assign`/`unassign`/`connect` subcommands),
   as leaned toward above. Revisit if/when Phase 2's broker becomes a
   genuinely separate, long-lived process, unlike Phase 1's one-shot CLI
   commands. That is a real reason to split, and not mere default caution.
6. **Broker placement**: keep Phase 2's long-lived broker inside
   `corral-vdi`, or expose VDI routes through a peer/gateway protocol? It must
   preserve the lean core and work with both Tailscale and ingress-agnostic
   deployments.

## Sources

The research brief covers the KubeVirt VDI ecosystem, remote-display
protocols, and prior art for session brokers. It also covers the GPU/USB state
of Windows-on-KubeVirt and patterns for Linux desktop pools. See the issue
that tracks this RFC for the complete, sourced version. Key links: [FOSDEM 2026 VDI+KubeVirt talk](https://fosdem.org/2026/schedule/event/CFCCDQ-vdi-and-kubevirt/),
[KubeVirt VirtualMachinePool docs](https://kubevirt.io/user-guide/user_workloads/pool/),
[KubeVirt USB redirection](https://kubevirt.io/user-guide/compute/client_passthrough/).
More links:
[NVIDIA GPU Operator + KubeVirt](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/gpu-operator-kubevirt.html),
[Selkies](https://github.com/selkies-project/selkies),
[Kasm VDI on Kubernetes](https://kasm.com/vdi-kubernetes),
[oVirt project update, Sept 2025](https://blogs.ovirt.org/2025/09/ovirt-project-update/).
