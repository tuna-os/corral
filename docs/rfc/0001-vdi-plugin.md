# RFC-0001: VDI plugin — Windows/Linux desktop pools on Corral

**Status:** Phase 1 implemented (`corral-vdi`, `pkg/vdi` — pool create/list/
delete, assign/unassign, connect). Phases 0/2/3/4 still draft/seeking
review. Setup guide: [docs/vdi.md](../vdi.md).
**Date:** 2026-07-02
**Author:** grilled out of a live session with James Reilly + Claude

## Summary

Add a `corral-vdi` plugin that turns Corral's existing VM/Container machinery
into a small, self-hosted **Virtual Desktop Infrastructure**: pools of
Windows or Linux desktops, assigned to users on request, reached through the
browser, reclaimed when idle. Not a Citrix/Horizon replacement — a homelab-
to-small-team-scale VDI that fits in one Go binary and a tailnet, the same
scope discipline that's kept Corral itself lean.

## Why now

Corral already has almost every *component* a VDI stack needs, built for
other reasons:

| VDI need | Already exists in Corral as |
|---|---|
| Remote display in the browser | noVNC bridge (`/api/vnc/{ns}/{name}`), xterm.js serial bridge |
| RDP detection + raw transport | `GET /api/vms/{ns}/{name}/rdp` port probe, `/api/rdp/{ns}/{name}` websocket bridge (ADR-0002 phase 1) |
| Windows guest provisioning | `corral-windows` plugin — UEFI/TPM/virtio, driver ISO |
| Full desktop Linux images | `corral bootc` — builds Universal Blue/Bluefin/TunaOS desktop images on-cluster from a container image |
| Lightweight ephemeral Linux sessions | Containers (CT) — `pkg/ct`, distrobox-style persistent rootfs |
| Tailnet-only exposure | every VM/CT gets a `tailscale.com/expose` Service already |
| Cluster capability gating | `corral doctor` — GPU/PCI passthrough check, StorageClass checks |
| Replica pools of identical VMs | KubeVirt's own `VirtualMachinePool` CRD |

What's **missing** is the thin layer that turns "a pool of VMs" into "VDI":
**assignment** (which user gets which desktop), **lifecycle policy**
(power on demand, reclaim on idle/logout), and **one connect button** that
picks the right protocol per desktop. That layer — not new console
plumbing, not a new hypervisor integration — is the actual scope of this
RFC.

## What we're explicitly *not* building

Grounded in the research (full brief: link in the tracking issue) — these
are deliberate exclusions, not oversights:

- **Not a new remote-display protocol.** noVNC/RDP-over-websocket stay. SPICE
  is dead upstream (QEMU is dropping SPICE server support entirely as of a
  2025 kubevirt-dev thread) — do not build on it.
- **Not a general-purpose connection broker product.** oVirt's engine+portal
  architecture and Leostream (commercial, KubeVirt-aware since 2025) are the
  reference points for the *pattern* (pool + ticket + assignment), not
  something to run alongside Corral as a second control plane.
- **Not solving Windows licensing.** Pooled/multi-session Windows needs
  Enterprise E3/E5 or RDS CALs — a customer-facing constraint this RFC flags
  and moves on from, not a technical problem Corral can solve.
- **Not GPU virtualization plumbing.** KubeVirt + NVIDIA's GPU Operator
  already handle vGPU/mediated-device passthrough; `corral doctor` already
  checks for it (this session's GPU/PCI passthrough check). Reuse, don't
  rebuild.

## Prior art, and how this differs

- **oVirt** (community-maintained, still shipping releases into 2026):
  proves the pool+ticket broker pattern. Non-Kubernetes-native — not
  reusable directly, pattern only.
- **Leostream**: commercial, added OpenShift Virtualization/KubeVirt support
  ~end of 2025. Validates "external broker drives KubeVirt API" as a real,
  chosen architecture by someone solving the same problem commercially.
  Closed-source — differentiate from, don't depend on.
- **Kasm Workspaces**: as of v1.16 provisions *real VMs* via Harvester/
  KubeVirt in addition to its container-native workspaces — independently
  converging on the same "containers for ephemeral, VMs for persistent"
  split this RFC proposes for Corral's CT + bootc combination. Strongest
  external validation found that the dual-workload approach isn't a hack.
- **Selkies** (Google-originated, actively developed): GPU-accelerated
  WebRTC desktop streaming, container-first. Not a drop-in noVNC
  replacement for VM consoles (streams from inside a pod, not a VM
  framebuffer) but its prebuilt desktop container images (KDE Plasma,
  Wine/Proton, GPU passthrough) are a strong candidate for what runs
  *inside* a Corral CT-based ephemeral desktop pool — reuse the images,
  not the whole project.
- **FOSDEM 2026 "VDI and KubeVirt"** talk (KubeVirt maintainer, USB
  redirection/console work): proposes QEMU's D-Bus display interface as a
  pluggable remote-display seam so external bridges (Guacamole, Selkies,
  custom) can attach without owning the whole console pipeline. No shipped
  code yet — track this, because if it lands upstream it may be a better
  long-term seam than `virtctl vnc --proxy-only` for phase 3+ below.

## Design

### Concepts (candidates for CONTEXT.md once this settles)

- **Desktop Pool** — a named group of identical desktops (VM- or CT-backed),
  a template (bootc image / Windows ISO+answer-file / CT image), a target
  size, and a reclaim policy. Built on KubeVirt's `VirtualMachinePool` for
  the VM-backed case; a thin equivalent for CT-backed pools (no upstream
  CRD exists for pooled pods — this is new code, small).
- **Assignment** — a claim ticket: user identity (from the same Tailscale
  identity source as `CORRAL_ADMINS` today, see ADR-0003) → pool member →
  expiry. Stored the same way the registry stores VM metadata today
  (`~/.local/share/tailvm/registry.json` pattern, or a K8s-native
  ConfigMap/CRD if this needs to survive the CLI process — leaning CRD,
  since assignment state needs to be visible cluster-wide, not just to
  whichever machine ran `corral`).
- **Connect** — one button/command that resolves a desktop's *actual*
  reachable protocol (RDP probe already exists; extend the same idea to
  "is this a VNC-only guest, RDP-capable, or a CT with just a terminal")
  and opens the right client path — today: existing noVNC/xterm.js bridges
  plus native-RDP-via-port-forward; tomorrow: ADR-0002 phase 2's in-browser
  IronRDP once that lands, so RDP desktops get the same one-click
  in-browser experience VNC already has.

### Phased plan

**Phase 0 — finish the prerequisite that's already an open ADR.**
Ship ADR-0002 phase 2 (in-browser IronRDP via RDCleanPath) *before* phase 2
below. A VDI product where Windows desktops require a native RDP client
while Linux desktops are one-click-in-browser is a bad first impression —
this is sequencing, not new scope.

**Phase 1 — static pools, manual assignment (CLI, no broker yet). Implemented.**
`corral vdi pool create <name> --from <golden-vm> --size N` clones an
*already-built* VM (built the normal way — `corral bootc`/`corral-windows`/
`corral create` — then customized and stopped) N times via
`kubevirt.Client.Clone`, labeled as pool members. `corral vdi assign <pool>
<user>` hand-wires a claim (a K8s label/annotation, nothing fancier).
`corral vdi connect <member>` prints the existing VNC/RDP/SSH paths for
that member. Full setup guide: [docs/vdi.md](../vdi.md).

Landed slightly differently than first drafted above: `--from <existing-vm>`
(clone a golden VM) rather than `--template <image>` (build N from
scratch) — cloning reuses `corral clone`'s already-tested primitive
directly and matches how real VDI systems build pools (golden image once,
clone many), instead of re-running a full bootc build or Windows ISO
install N times. A real bug was found and fixed during live verification:
`Clone()` returns as soon as the `VirtualMachineClone` CRD is applied, not
once the target VM actually exists — `CreatePool` originally raced ahead
and tried to label a VM that didn't exist yet on a real cluster. Fixed
with a poll-wait (`waitForVM`, 2min timeout) between clone and label.

**Phase 2 — self-serve claim + reclaim.**
A minimal web page: authenticated user (Tailscale identity) hits "Get a
desktop," gets assigned an available pool member (or one is powered on if
scaled-to-zero), redirected straight into its console. Idle/logout
detection reclaims it (power off or destroy-and-recreate, per pool policy —
ephemeral CT pools destroy-and-recreate on every claim for a clean slate;
persistent bootc pools just power off and keep state). This is the actual
"VDI" moment — everything before this is infrastructure for it.

**Phase 3 — GPU pools + USB redirection.**
Wire `corral doctor`'s existing GPU/PCI passthrough check into pool
creation (`--gpu` flag, refuses to create the pool if the doctor check
says the cluster can't back it). Add `virtctl usbredir` support for
smartcard/security-key redirection on Windows pools (independent of SPICE,
confirmed still maintained).

**Phase 4 (exploratory, not committed) — WebRTC streaming for ephemeral
Linux pools.** Selkies-style container images inside Corral CTs for
GPU-accelerated, low-latency ephemeral desktops, as an alternative to
noVNC for that specific pool type. Track the FOSDEM D-Bus-display proposal
before committing engineering time here — it may change the right
integration seam.

## Feasibility, honestly

- **Phase 1 is low-risk** — it's assembly of existing, already-tested
  Corral machinery (bootc, corral-windows, corral ct, KubeVirt's own
  `VirtualMachinePool`), not new hard problems.
- **Phase 2's hard part is reclaim/idle-detection**, not claim/assignment.
  noVNC/RDP give Corral no "user went idle" signal for free. Cheapest
  honest starting point: track websocket-bridge connection open/close as
  an activity proxy (imperfect, needs zero guest cooperation) rather than
  a bare session TTL (kicks people mid-session) or an in-guest agent (real
  extra engineering, per-OS).
- **Phase 0 (in-browser RDP) has been "planned, not implemented" since
  ADR-0002** — ship it before layering VDI-specific work on top, and treat
  its still-unstarted status as a real signal about effort, not a
  formality to wave through.
- **Phase 3's GPU story is constrained by AMD's current driver/firmware
  support, not by KubeVirt.** Verified directly against AMD's GIM/SR-IOV
  driver release notes (2026-07): officially supported hardware is
  exclusively MI-series Instinct datacenter accelerators plus one Radeon
  PRO workstation card — no APUs, no consumer/integrated GPUs, at all.
  `karnataka`'s AMD Strix Halo APU is a full-GPU-passthrough-to-one-VM
  device today, not a multi-tenant vGPU one. An AMD engineer has said
  client-GPU SR-IOV is "in the roadmap," no committed timeline — worth
  rechecking before committing to Phase 3, not assuming permanently
  impossible. Fine for "one nice accelerated desktop" today either way,
  not a GPU-accelerated multi-user pool on this hardware.
- **Overall scope**: this is realistically a personal/small-team VDI, not
  an oVirt/Kasm/Leostream competitor — the phased plan is sized for what
  one person plus Corral's existing components can actually ship, not for
  matching enterprise VDI feature parity.

## Open questions (for the grilling session)

1. **Assignment storage**: CRD vs. ConfigMap vs. reusing the registry
   pattern — needs to be visible/queryable cluster-wide (web UI, any CLI
   invocation, any node), which argues CRD, but that's a bigger commitment
   (new API type, controller-ish reconciliation) than Corral has taken on
   before. Worth a dedicated grilling pass.
2. **CT-backed pool primitive**: KubeVirt gives us `VirtualMachinePool` for
   free; nothing upstream gives us a pooled-pod equivalent. Build our own
   (small — a label-based reconcile loop) or is this premature for CT pools
   specifically vs. starting VM-only?
3. **Reclaim triggers**: idle timeout (needs an in-guest or protocol-level
   "last activity" signal — noVNC/RDP don't hand this to Corral for free)
   vs. explicit logout vs. session TTL. Each has different guest-side
   requirements.
4. **Licensing UX**: does Corral just document the Windows licensing
   constraint (current lean) or actively refuse to create pools above some
   size without an explicit `--i-have-licenses` flag?
5. ~~**Scope of "plugin"**~~ — **resolved for Phase 1**: single
   `corral-vdi` binary (`pool`/`assign`/`unassign`/`connect` subcommands),
   as leaned toward above. Revisit if/when Phase 2's broker becomes a
   genuinely separate long-running process (unlike Phase 1's one-shot CLI
   commands) — that's a real reason to split, not just default caution.

## Sources

Full research brief (KubeVirt VDI ecosystem, remote-display protocols,
session-brokering prior art, Windows-on-KubeVirt GPU/USB state, Linux
desktop-pooling patterns) — see the tracking issue for the complete,
sourced version. Key links: [FOSDEM 2026 VDI+KubeVirt talk](https://fosdem.org/2026/schedule/event/CFCCDQ-vdi-and-kubevirt/),
[KubeVirt VirtualMachinePool docs](https://kubevirt.io/user-guide/user_workloads/pool/),
[KubeVirt USB redirection](https://kubevirt.io/user-guide/compute/client_passthrough/),
[NVIDIA GPU Operator + KubeVirt](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/gpu-operator-kubevirt.html),
[Selkies](https://github.com/selkies-project/selkies),
[Kasm VDI on Kubernetes](https://kasm.com/vdi-kubernetes),
[oVirt project update, Sept 2025](https://blogs.ovirt.org/2025/09/ovirt-project-update/).
