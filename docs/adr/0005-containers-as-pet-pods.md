# ADR-0005: Containers (CT) as pet pods

**Status:** accepted (first vertical slice implemented)
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
  console`) or falls back to a CT (`kubectl exec -it <pod> -- sh`).
- **Networking** via a plain K8s Service selecting the CT pod's own
  labels directly — no proxy Deployment, no socat relay, no VMI-IP-polling
  shell script. Those exist for VMs specifically because a KubeVirt VM's
  pod (virt-launcher) isn't a stable, predictable selector target across
  restarts; a CT's own pod *is* the pod, so a normal Service works.
- **Resources**: `cores` → pod CPU limit/request, `memory` → pod memory
  limit/request. PVE's "swap" setting has no honest Kubernetes mapping and
  is omitted, not faked.

### What "PVC-backed persistent rootfs" means here

The original design language (#50) says "PVC-backed persistent rootfs."
The literal reading — the container's *entire* root filesystem living on
the PVC, surviving a full re-`docker run` from scratch — needs the
container's entrypoint to `pivot_root`/chroot into the PVC-mounted
directory on startup, copying the base image's filesystem onto it on
first boot. That's baked-image logic: it has to live in the `ct-*` image's
own entrypoint script, not in generic pod-spec machinery corral can bolt
onto an arbitrary OCI image from outside.

This first slice mounts the PVC as a **data volume** (e.g. at `/data`),
not as the literal container rootfs — full-rootfs persistence is
deferred to when the curated `ct-*` images (with the pivot_root
entrypoint logic) actually exist; building and publishing those images is
explicitly out of scope for this slice (a separate, later content-curation
task, not a mechanism gap). Anything not living under the PVC mount point
resets to the image's baked-in state on every stop/start, same as any
plain container — only genuinely different from a stateless pod in that
identity (name, PVC, IP-via-Service) persists across that cycle.

## Consequences

- CTs are a third "backend" in the domain sense (alongside qemu, kubevirt)
  but deliberately **not** a peer in `types.Backend` — that interface
  covers what qemu and kubevirt both genuinely implement (VM lifecycle);
  CTs are pods, not VMs, and forcing them through the same interface would
  either fake VM-shaped operations they don't have (live migration,
  snapshot-as-VM-snapshot) or shrink the interface to the point of not
  being worth sharing. `pkg/ct` is its own package with its own thin
  lifecycle surface (Create/List/Start/Stop/Delete).
- Full rootfs persistence needs curated `ct-*` images this ADR
  deliberately doesn't build — the mechanism is real and tested against a
  stock public image, but "your CT looks like a fresh container on every
  restart except for `/data`" is the honest current behavior, not the
  eventual one.
- Snapshot (VolumeSnapshot of the PVC) and migrate (reschedule to a node
  that can mount the PVC) are follow-up slices — the first slice is
  create/start/stop/console/network only, matching #50's own tracer-bullet
  scoping.
