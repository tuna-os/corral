# Containers (CT) as pet pods — amends ADR-0001's LXC cut

ADR-0001 cut LXC as having "no honest KubeVirt primitive." That was wrong:
KubeVirt has no container-VM, but **Kubernetes pods are Linux containers**, so
surfacing containers is not faking a primitive that doesn't exist. We un-cut
LXC and implement a **Container (CT)** as a *pet pod*: a pod with a PVC-backed
persistent rootfs, an init process, and sshd, presented in the Proxmox CT
shape (create / start / stop / console / snapshot-the-PVC). Ordinary K8s
Deployments/pods are **not** presented as CTs — only purpose-built pet pods.

**Privilege model: unprivileged by default, privileged opt-in** — a 1:1 mirror
of PVE's CT "Privileged" checkbox. Most CTs run without `SYS_ADMIN`; full
systemd / cgroup delegation is an explicit opt-in.

**Feasibility (verified against the live cluster):** the namespaces where VMs
and CTs live (`corral-vms`, `corral`, `default`, `kubevirt`, `longhorn-system`)
all enforce `pod-security.kubernetes.io/enforce: privileged`, so privileged
pods schedule there. Talos's strictness is at the **host** level (immutable OS,
no node SSH, locked-down kubelet), **not** namespace Pod Security — which the
operator set permissive so virt-handler/Longhorn can run. So privileged CTs are
genuinely available, not a workaround; unprivileged-default is a least-privilege
choice, not a constraint.

Why this is honest and not a fake: the pet/cattle gap is real, so we reproduce
the pet semantics (persistent rootfs, init, console) rather than relabel
ephemeral workloads. What we do *not* claim: live-memory snapshot, or migration
of running container state (CT snapshot = VolumeSnapshot of the PVC; CT "migrate"
= reschedule to a node that can mount the PVC).

**Images: curated content, shared machinery.** A bootc/VM image is built to be
installed to a disk and booted, not run as a pod with init + sshd as PID 1, so
"any VM image is also a CT" is false. CTs draw from a **curated** set of
corral-owned `ct-*` system-container images (init + sshd baked in), but these
ride the **existing catalog/sources plumbing** and the `just` recipe that
refreshes the catalog from ghcr (commit d710c4b) — a CT image is just a catalog
entry with a `ct: true` capability flag. We curate *which* images, not *how*
catalogs work.

**Separate GUI flow.** The UI exposes a distinct **Create CT** button and
wizard alongside **Create VM** — mirroring PVE's two-button pattern — rather
than a VM/CT toggle inside one wizard. Shared plumbing, separate flow, where
PVE users expect it.

**Runtime mappings:**

- **Networking** — a CT is reached through the **existing port-proxy Service /
  tailnet-proxy** mechanism, the same path KubeVirt VMs already use; the `ct-*`
  images do **not** bake in tailscale and a CT is **not** its own tailnet node.
  Trade-off chosen over "CT joins the tailnet itself": leaner images, no tailnet
  device sprawl per CT, consistent with cluster-VM access. Consequence: a CT is
  reachable but not independently MagicDNS-addressable. (Naming nit: the proxy's
  `<name>-vm.<tailnet>` host is odd for a CT.)
- **Console** — no framebuffer, so no noVNC. CT console = **exec/attach into the
  pod → xterm**, reusing the existing `/api/tty` websocket machinery. Matches
  PVE, where an LXC console is a terminal, not VNC.
- **Resources** — CT cores → pod CPU limit, CT memory → pod memory limit. PVE's
  "swap" field has no honest pod mapping and is dropped.
