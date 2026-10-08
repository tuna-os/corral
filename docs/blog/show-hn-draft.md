# Show HN draft

> **Title:** Show HN: Corral – Proxmox-style VM manager for KubeVirt, QEMU, Incus, libvirt and Proxmox, one Go binary
>
> **URL:** https://github.com/tuna-os/corral

Post body (HN first comment, from the author):

---

Hi HN — I built Corral because I love how Proxmox *feels*. But I couldn't
justify a whole second platform next to the Kubernetes cluster I already have.

Corral is a single static Go binary. It gives you the Proxmox experience —
datacenter tree, create wizard, in-browser VNC/serial consoles, VMs and
containers side by side. And it works on top of whatever you've got:

- A cluster: it drives KubeVirt through kubectl/virtctl. No operator, no
  agent, no CRDs of its own to install.
- Only a laptop: the same commands run VMs on local QEMU/KVM as systemd user
  services. The same dashboard shows them under a "local" node.
- The boxes you didn't want to replace: an Incus server, a libvirt host over
  `qemu+ssh://`, a Proxmox VE cluster. Corral reuses the trust you already
  have — an Incus remote, your OpenSSH agent/keys/config, a PVE API token. So there's no second password database to keep in sync. And it never
  mutates kubectl's or Incus's own global config to switch targets.
- Another Corral: `` corral peer add homelab https://corral.example.ts.net ``
  federates a second instance. For example, the one *inside* the cluster, when
  this laptop has no route to the Kubernetes API. Guest access is direct-first.
  If you can already reach the VM over Tailscale or an ingress, consoles and
  `corral ssh` go straight there. They do not hairpin through two Corral
  servers. The Corral-to-Corral HTTP/WebSocket relay is the fallback for the
  networks where that's impossible.
- Tailscale: every VM can join your tailnet on first boot, so `corral ssh`
  works from your phone.

It's one fleet, not five tabs: `corral list`, the TUI and the dashboard
show all of it at once. A context is a *destination* for unqualified commands,
not a mode switch — if you select one, the other machines never disappear.

The fastest way to judge it (no cluster, no kubectl, nothing):

    brew install tuna-os/tap/corral-vm   # or the curl|sh in the README
    corral --demo                     # the TUI against a built-in fake cluster
    corral web --demo                 # the dashboard on 127.0.0.1:8006

`--demo` runs the real code paths against an in-memory fake cluster.
Start/stop/create/delete all work, metrics move, and it's the same binary you'd
point at real infrastructure. I originally built it to develop the UI without a
cluster to burn; it turned out to be the best demo tool I have. The CI smoke
tests also drive the frontend this way.

The part I'm most excited about: bootc integration. Point Corral at a
*bootable container image* (ghcr.io/...) and it runs `bootc install to-disk`
in a builder VM on the cluster. Then it boots the result as a first-class VM.
Your OS is an OCI image in a registry; `corral bootc upgrade` rolls the VM to
the next build. Proxmox structurally can't do that.

The thing that fell out of several backends at once: disk export works on all
of them now (qcow2, raw.gz, or an Incus tarball). It refuses a live guest where
that would hand you a torn image. And `corral move <vm> --to <backend>`
composes that into a cross-backend move — preflight, export, convert, ingest,
verify. It is deliberately *cold* and says so everywhere: the guest stops, and
`migrate` still means the live within-one-backend kind.

The preflight refuses before it touches anything, and it reports every reason
at once: firmware mismatch, disk bus and virtio drivers, free space. It also
reports that the guest comes up with a new MAC and almost certainly a new IP.
The source is left stopped, never deleted, unless you pass `--delete-source`.

All five backends can now be a destination: Incus was the last holdout. It
receives a move when Corral publishes the disk as an Incus image and launches
from it. The alternative was to attach a foreign disk to an empty instance.
That would have looked like it worked, and then behaved unlike every other
Incus instance.

Honest state of things: v0.6.x, about twelve weeks old, one developer plus a
lot of Claude. KubeVirt is still the most exercised path. But local QEMU, Incus
and libvirt have caught up on the basics — inventory, create, lifecycle,
snapshots, export. And local QEMU in the web UI is complete: lifecycle, info,
and a real noVNC console in the browser, no CLI hop.

docs/backend-parity.md lists, per operation, what each backend still can't do.
A generator writes it from the same table the code reads, and CI fails if the
two drift. So "supported" there means a test says so. Windows VMs, GPU
passthrough, scheduled snapshots/backups exist as plugins of mixed maturity.
I'd love feedback on the architecture docs (CONTEXT.md, docs/adr/) as much as
the code.

Apache-2.0. Happy to answer anything.

---

## Submission notes (not part of the post)

- Submit early in the day, US Eastern, Tue–Thu. Have the `--demo` GIF at the
  top of the README before you submit (done).
- Re-check the version and age line immediately before you post. It was
  accurate at v0.6.0 (tagged 2026-08-06) against a repo created 2026-06-10.
  We last refreshed the age line on 2026-09-02.
- Expected pushback to be ready for, each with its answer:
- "why not virt-manager/Cockpit?" Answer: cluster, laptop, Incus, libvirt and
  PVE aggregated as one fleet, tailnet-native, bootc.
- "web UI with no auth?" Answer: binds loopback by default, tailnet identity
  headers when served behind Tailscale, CORRAL_ADMINS gate.
- "KubeVirt is heavy." Answer: true — that's what the qemu backend is for.
- "the non-KubeVirt backends must be stubs." Answer: point at
  docs/backend-parity.md. A generator writes it from the code, and it names
  every remaining gap instead of hiding it.
