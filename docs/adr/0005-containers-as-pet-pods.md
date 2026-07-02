# ADR-0005: Containers (CT) as pet pods

**Status:** accepted (first vertical slice implemented; persistent-rootfs
mode added 2026-07-02)
**Date:** 2026-07-02

## Context

Proxmox VE has two workload types: **VMs** (QEMU/KVM) and **Containers**
(LXC). Corral has only ever had the VM half. #50 asks for a Proxmox-style
Container, matching PVE's shape closely enough that the mental model (and
the Proxmox API compat layer) carries over, while being honest about what
it actually is on Kubernetes: there's no LXC here, no shared-kernel
namespace container runtime distinct from what K8s already runs — a CT is
a Kubernetes **pod**.

The distinguishing design question: is a CT a **cattle pod** (stateless,
disposable, replaced-not-restarted — the K8s-native default) or a **pet
pod** (long-lived, console-able, individually named and addressed, state
persisted across restarts — the VM-like model users actually want when
they reach for "Container" in a Proxmox-shaped tool)? This ADR picks pet
pod, deliberately diverging from Kubernetes convention because that's what
"Container" means in the Proxmox vocabulary this tool mirrors.

## Decision

A CT is a pod with:

- A **PVC-backed persistent volume** for state that survives pod restarts.
- **Unprivileged by default**, privileged opt-in — 1:1 with PVE's
  "Privileged" checkbox (`securityContext.privileged`).
- **Start/stop** semantics like a VM: stopping deletes the pod but keeps
  the PVC; starting recreates the pod referencing the same PVC. No
  Deployment/StatefulSet controller reconciling replica count — corral
  itself is the thing that creates/deletes the pod on start/stop, the same
  create/delete-cycle-against-a-stable-PVC pattern the bootc builder
  already uses for VMs.
- **Console** via `kubectl exec`, not a framebuffer — CTs have no VNC/RDP
  console the way VMs do. Reuses the existing `/api/tty/{ns}/{name}`
  endpoint: `ttyBridge` now checks whether `{name}` is a VM (`virtctl
  console`) or falls back to a CT (`kubectl exec -it <pod> -- <cmd>`, where
  `<cmd>` comes from `ct.ExecCommand` — plain `sh` for unprivileged CTs, a
  re-`chroot` into the persistent rootfs for privileged ones).
- **Networking** via a plain K8s Service selecting the CT pod's own
  labels directly — no proxy Deployment, no socat relay, no VMI-IP-polling
  shell script. Those exist for VMs specifically because a KubeVirt VM's
  pod (virt-launcher) isn't a stable, predictable selector target across
  restarts; a CT's own pod *is* the pod, so a normal Service works.
- **Resources**: `cores` → pod CPU limit/request, `memory` → pod memory
  limit/request. PVE's "swap" setting has no honest Kubernetes mapping and
  is omitted, not faked.

### What "PVC-backed persistent rootfs" means here

The original design language (#50) says "PVC-backed persistent rootfs" —
distrobox-style: enter the container, install packages, leave, come back
later, it's all still there. The literal reading needs the container's
entrypoint to `chroot` into the PVC-mounted directory on startup, copying
the base image's filesystem onto it on first boot.

This turned out **not** to need a curated corral-owned image after all —
Kubernetes lets you override any image's `command`, so the seed+chroot
bootstrap can be generic pod-spec machinery (`pkg/ct`'s `bootstrapScript`)
bolted onto an arbitrary OCI image from outside, rather than baked into
the image itself. The container's own `command` becomes: on first boot,
`cp -a --one-file-system /. $PVC` (copying the image's own filesystem onto
the PVC — `--one-file-system` naturally excludes `/proc`, `/sys`, `/dev`,
and the PVC mount itself, since those are all separate mounts from the
container's own overlay), then `mount --rbind` the kernel pseudo-filesystems
into the copy, then `exec chroot $PVC sh -c 'sleep infinity'`. From then on
the PVC *is* the rootfs.

This is gated behind **Privileged** rather than being unconditional,
because `mount`/`chroot` need `CAP_SYS_ADMIN`/`CAP_SYS_CHROOT` — the same
reason PVE's own unprivileged LXC containers don't get this level of host
access either. Unprivileged CTs keep the original `/data`-only mount
(simple, safe default, PSA-restricted-friendly); privileged CTs get the
full persistent rootfs. It needs a real OS image (debian/ubuntu/fedora —
has `chroot` + coreutils' `cp -a`), not alpine/busybox.

**Console re-entry**: a fresh `kubectl exec` session joins the container's
namespaces but starts from the pre-chroot image root — chroot only changes
the *calling process's* apparent root, it's not a namespace a sibling exec
session inherits. So `/api/tty`'s exec command for a privileged CT
re-`chroot`s on entry (`chroot $PVC sh -c 'exec bash || exec sh'`) rather
than running a plain shell — see `ct.ExecCommand`.

## Consequences

- CTs are a third "backend" in the domain sense (alongside qemu, kubevirt)
  but deliberately **not** a peer in `types.Backend` — that interface
  covers what qemu and kubevirt both genuinely implement (VM lifecycle);
  CTs are pods, not VMs, and forcing them through the same interface would
  either fake VM-shaped operations they don't have (live migration,
  snapshot-as-VM-snapshot) or shrink the interface to the point of not
  being worth sharing. `pkg/ct` is its own package with its own thin
  lifecycle surface (Create/List/Start/Stop/Delete).
- Full rootfs persistence (privileged CTs) works against stock public
  images (tested against `debian:bookworm`) — no curated `ct-*` image is
  needed for the mechanism itself. A curated image is still worthwhile as
  a follow-up (baked-in sshd/init, faster first-boot seed via a pre-shrunk
  base) but is a content task, not something the persistence mechanism is
  blocked on.
- The seed-copy on first boot is a real cost: `cp -a` of the whole image
  onto the PVC takes time and disk proportional to image size, paid once
  per CT (not per Start) since the `.corral-seeded` marker skips it after.
- Snapshot (VolumeSnapshot of the PVC) and migrate (reschedule to a node
  that can mount the PVC) are follow-up slices — the first slice is
  create/start/stop/console/network only, matching #50's own tracer-bullet
  scoping.
