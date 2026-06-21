# Web UI Proxmox feature-scope rule

The web UI is a *modern, familiar* take on the Proxmox console over a KubeVirt
backend — not a pixel-for-pixel clone, and not bound to ship every PVE feature.
We decide what's "reasonable" to build with three filters, in order:

1. **Order by actual usage.** Features the operator runs weekly (console,
   start/stop, snapshots, live migrate, GPU passthrough, bootc rebuild) get
   built and polished first.
2. **Breadth is the aspiration, not a wall.** PVE-familiar features we don't
   personally use are not excluded — they sink to the bottom of the backlog.
3. **Honesty filter.** A feature ships only if it maps to a real KubeVirt/K8s
   primitive. We never fake a primitive that doesn't exist (no emulated LXC, no
   Proxmox-firewall tab backed by nothing). If it can't be done honestly, it is
   **cut, not stubbed.**

Why: "as many Proxmox features as reasonable" is unbounded and, taken
literally, pushes toward leaky emulation of things KubeVirt has no equivalent
for. A future reader will wonder why a PVE-compatible UI deliberately omits PVE
staples (firewall, Ceph management) — this is the answer: those have no
honest KubeVirt mapping, so we don't pretend.

**Amended:** LXC was originally cut here too, but pods *are* a real primitive,
so LXC has an honest mapping after all. See [ADR-0003](./0003-containers-as-pet-pods.md).
