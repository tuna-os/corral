# ADR-0005: Containers (CT) as pet pods

**Status:** accepted (first vertical slice implemented; persistent-rootfs
mode added on 2026-07-02)
**Date:** 2026-07-02

## Context

Proxmox VE has two workload types: **VMs** (QEMU/KVM) and **Containers**
(LXC). Corral has only ever had the VM half. #50 asks for a Proxmox-style
Container. It must match PVE's shape closely enough that the mental model (and
the compat layer for the Proxmox API) carries over. It must also be honest
about what it is on Kubernetes. There's no LXC here, and no separate container
runtime with shared-kernel namespaces beyond what K8s already runs: a CT is a
Kubernetes **pod**.

The central design question: is a CT a **cattle pod** or a **pet pod**? A
cattle pod is stateless, disposable, and replaced-not-restarted — the K8s-native
default. A pet pod is long-lived, console-able, individually named and
addressed, with state that persists across restarts. That is the VM-like model
that users want when they reach for "Container" in a Proxmox-shaped tool. This
ADR picks the pet pod. It deliberately departs from Kubernetes convention,
because that's what "Container" means in the Proxmox vocabulary this tool
mirrors.

## Decision

A CT is a pod with:

- A **PVC-backed persistent volume** for state that survives pod restarts.
- **Unprivileged by default**, privileged opt-in — 1:1 with PVE's
  "Privileged" checkbox (`securityContext.privileged`).
- **Start/stop** semantics like a VM. Stop deletes the pod but keeps the PVC.
  Start recreates the pod, with a reference to the same PVC. No
  Deployment/StatefulSet controller reconciles a replica count. Instead, corral
  itself creates/deletes the pod on start/stop. This is the same
  create/delete-cycle-against-a-stable-PVC pattern that the bootc builder
  already uses for VMs.
- **Console** via `kubectl exec`, not a framebuffer — CTs have no VNC/RDP
  console the way VMs do. It reuses the existing `/api/tty/{ns}/{name}`
  endpoint. `ttyBridge` now checks whether `{name}` is a VM (`virtctl
  console`). If not, it falls back to a CT (`kubectl exec -it <pod> -- <cmd>`).
  There, `<cmd>` comes from `ct.ExecCommand`: plain `sh` for unprivileged CTs,
  and a re-`chroot` into the persistent rootfs for privileged ones.
- **Networking** via a plain K8s Service that selects the CT pod's own labels
  directly. There is no proxy Deployment, no socat relay, and no shell script
  that polls for the VMI IP. Those exist for VMs specifically because a
  KubeVirt VM's pod (virt-launcher) isn't a stable, predictable selector target
  across restarts. A CT's own pod *is* the pod, so a normal Service works.
- **Resources**: `cores` → pod CPU limit/request, `memory` → pod memory
  limit/request. PVE's "swap" setting has no honest Kubernetes mapping, so
  Corral omits it and does not fake it.

### What "PVC-backed persistent rootfs" means here

The original design language (#50) says "PVC-backed persistent rootfs" —
distrobox-style: enter the container, install packages, leave, come back
later, it's all still there. The literal interpretation needs the container's
entrypoint to `chroot` into the PVC-mounted directory on startup. On first
boot, the entrypoint also copies the base image's filesystem onto it.

This turned out **not** to need a curated corral-owned image after all.
Kubernetes lets you override any image's `command`. So the seed+chroot
bootstrap can be generic pod-spec machinery (`pkg/ct`'s `bootstrapScript`).
Corral bolts it onto an arbitrary OCI image from outside, and does not bake it
into the image itself.

The container's own `command` then does three things. On first boot, it runs
`cp -a --one-file-system /. $PVC` to copy the image's own filesystem onto the
PVC. `--one-file-system` naturally excludes `/proc`, `/sys`, `/dev`, and the
PVC mount itself, because those are all separate mounts from the container's
own overlay. Next, `mount --rbind` puts the kernel pseudo-filesystems into the
copy. Last, it runs `exec chroot $PVC sh -c 'sleep infinity'`. From then on
the PVC *is* the rootfs.

The **Privileged** checkbox gates this, so it is not unconditional. The reason
is that `mount`/`chroot` need `CAP_SYS_ADMIN`/`CAP_SYS_CHROOT`. For the same
reason, PVE's own unprivileged LXC containers don't get this level of host
access either. Unprivileged CTs keep the original `/data`-only mount
(simple, safe default, PSA-restricted-friendly); privileged CTs get the
full persistent rootfs. It needs a real OS image (debian/ubuntu/fedora —
has `chroot` + coreutils' `cp -a`), not alpine/busybox.

**Console re-entry**: a fresh `kubectl exec` session joins the container's
namespaces, but it starts from the pre-chroot image root. A chroot only changes
the apparent root of the *process that calls it*. It is not a namespace that
passes to a sibling exec session. So `/api/tty`'s exec command for a privileged
CT does a re-`chroot` on entry (`chroot $PVC sh -c 'exec bash || exec sh'`),
and does not start a plain shell. See `ct.ExecCommand`.

## Consequences

- CTs are a third "backend" in the domain sense (alongside qemu, kubevirt).
  But they are deliberately **not** a peer in `types.Backend`. That interface
  covers what qemu and kubevirt both genuinely do (VM lifecycle). CTs are pods,
  not VMs. If Corral forced them through the same interface, one of two things
  would happen. Either the interface would fake VM-shaped operations that CTs
  don't have (live migration, snapshot-as-VM-snapshot). Or the interface would
  shrink to the point where it is not worth the effort. `pkg/ct` is its own
  package with its own thin lifecycle surface (Create/List/Start/Stop/Delete).
- Full rootfs persistence (privileged CTs) works against stock public
  images (tested against `debian:bookworm`). The mechanism itself needs no
  curated `ct-*` image. A curated image is still worthwhile as a follow-up
  (baked-in sshd/init, faster first-boot seed via a pre-shrunk base). But that
  is a content task, and the persistence mechanism does not wait for it.
- The seed-copy on first boot is a real cost. `cp -a` of the whole image onto
  the PVC takes time and disk proportional to image size. Each CT pays this
  once (not per Start), because the `.corral-seeded` marker skips it after that.
- Snapshot (VolumeSnapshot of the PVC) and migrate (reschedule to a node
  that can mount the PVC) are follow-up slices. The first slice is
  create/start/stop/console/network only. This matches #50's own
  tracer-bullet scope.
