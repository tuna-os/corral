# Backend parity

**KubeVirt was first-class and everything else was best effort.** That was true,
it was invisible, and this document is where it stops being either.

The rule now: **if a backend can do something Corral ships, Corral should support
it there**. Not every feature every backend has — parity across backends for the
features Corral has. Where a backend genuinely cannot do a thing, the matrix
records that with a reason instead of silence.

## How this document is kept honest

The table below is **generated from `pkg/backend.Matrix`**, which is the single
source of truth. The conformance tests in `pkg/backend` fail if:

- a matrix cell has no note (a gap nobody can act on),
- `types.CapabilitiesForBackend` advertises a capability that the matrix does not
  mark as shipped (a button that fails on click),
- `types.CapabilitiesForBackend` omits a capability that the matrix marks as
  shipped (a feature the operator cannot reach),
- `pkg/snapshot`'s adapter registry and the matrix disagree,
- **this document's table drifts from the matrix.**

So the numbers here cannot rot silently. Regenerate the table after each change
to the matrix.

Legend: ✅ shipped · 🔨 the backend can do this and Corral does not yet · — the
backend cannot, or it is meaningless there.

| Operation | kubevirt | qemu | incus | libvirt | proxmox |
|---|---|---|---|---|---|
| List / inventory | ✅ | ✅ | ✅ | ✅ | ✅ |
| Create | ✅ | ✅ | ✅ | ✅ | ✅ |
| Start | ✅ | ✅ | ✅ | ✅ | ✅ |
| Stop | ✅ | ✅ | ✅ | ✅ | ✅ |
| Restart | ✅ | ✅ | ✅ | ✅ | ✅ |
| Pause / resume | ✅ | ✅ | ✅ | ✅ | ✅ |
| Delete | ✅ | ✅ | ✅ | ✅ | ✅ |
| SSH | ✅ | ✅ | ✅ | 🔨 | ✅ |
| Serial / shell console | ✅ | 🔨 | ✅ | 🔨 | 🔨 |
| Graphical console (VNC) | ✅ | ✅ | 🔨 | ✅ | 🔨 |
| RDP | ✅ | 🔨 | 🔨 | 🔨 | 🔨 |
| Live CPU / memory | ✅ | ✅ | ✅ | ✅ | ✅ |
| Snapshot / restore | ✅ | ✅ | ✅ | ✅ | ✅ |
| Migrate | ✅ | — | 🔨 | 🔨 | ✅ |
| Clone | ✅ | 🔨 | 🔨 | 🔨 | ✅ |
| Template mark | ✅ | 🔨 | 🔨 | 🔨 | ✅ |
| CPU / memory edit | ✅ | 🔨 | 🔨 | 🔨 | ✅ |
| Add / remove disks | ✅ | 🔨 | 🔨 | 🔨 | ✅ |
| Expand disk | ✅ | 🔨 | 🔨 | 🔨 | ✅ |
| GPU passthrough | ✅ | 🔨 | 🔨 | 🔨 | ✅ |
| Export / backup disk | ✅ | ✅ | ✅ | ✅ | ✅ |
| Events | ✅ | ✅ | — | — | ✅ |
| Tags | ✅ | 🔨 | 🔨 | 🔨 | ✅ |
| Published ports | ✅ | ✅ | 🔨 | — | — |
| Containers (CT) | ✅ | — | ✅ | — | 🔨 |

## What the audit found

Four things that were worse than a missing feature, because each was a claim
Corral made that wasn't true. **The first three now have fixes** (in the same
change as this document). The fourth is the structural one, and it is step 2 of
the work below.

1. ~~**Corral lists every Incus instance twice.**~~ *Fixed.* `pkg/incus.List` returns *all*
   instances as VMs. It reads `Type` from the JSON and then ignores it. Also,
   `pkg/ct.listIncusCTs` returns the same instances again as CTs. So an Incus
   container appears as both a VM and a CT in the fleet. An Incus
   *virtual machine* appears as a CT. This is the single clearest symptom of
   LXC support that nobody ever finished.

2. ~~**The Incus path in `pkg/ct` bypasses the runner seam.**~~ *Fixed —* it now
   goes through `pkg/incus`, which targets the configured remote. Demo mode also
   shows Incus CTs for the first time. `listIncusCTs`,
   `incusExists`, `incusStart`, `incusStop`, and `incusDelete` call
   `exec.Command` directly instead of going through `shell.Runner`. Consequences:
   they are untestable, and they are invisible to demo mode. They always talk to
   the *local* daemon and ignore the configured remote. So nobody can start a CT
   on a remote Incus host, but the VM path on that host can.

3. ~~**Incus instances have no address.**~~ *Fixed —* Corral now reads
   `state.network` and skips loopback and link-local. `List` never reads
   `state.network`, so the IP column is empty for every Incus instance. The
   RDP/SSH probes have nothing to aim at.

4. **Code reaches the rich operations by `switch backend`, not by an interface.**
   `types.Backend` has nine methods. Snapshots, migrate, scale, volumes,
   metrics, clone, template, export, and events all go through
   `if backend == "kubevirt"` branches in `cmd/` and `pkg/web`. There are 33 such
   sites. That is the mechanism by which "best effort" happened: there was no contract
   to fail to satisfy.

`pkg/snapshot` is the counter-example and the template for the fix. It defines an
adapter per backend, reports honestly what each capture achieved, and refuses
with a typed error that carries a remedy. Every backend satisfies it, including
local QEMU. Nobody had to remember to add libvirt — the contract made the gap
visible.

## The work, in the order it should happen

**1. Stop the lies.** *Done:* Incus containers are CTs and Incus virtual
machines are VMs (`incus.Instance.IsContainer`). The CT path targets the
configured remote through `pkg/incus`, and Corral reads the instance address. The
demo fixture now holds both an Incus container and an Incus VM, so the split
stays covered.

**2. Generalise the adapter contract.** *Done:* `pkg/backend/ops.go` defines a
small interface per operation family — `Power`, `Restarter`, `Suspender`,
`Sizer`, `Storer`, `Mover`, `Cloner`, `Templater`, `Tagger`, `Observer`,
`Exporter`, plus `Addresser`. `pkg/backend/adapters.go` holds one adapter per
backend. It is the only place where code translates a backend's own signature. A surface
calls `backend.For(ref)` and asserts the family it needs; it never switches on a
backend name again.

What makes it more than documentation: **Corral derives support from the
assertions.** `Provides(backend, operation)` answers from the adapter's type. A
conformance test fails if the matrix claims an operation that the adapter does
not satisfy. It *also* fails if an adapter satisfies an operation that nobody has
added to the matrix yet. So a new method is how a gap closes. If you forget the
paperwork, the result is a red build, not a silent inconsistency.

Two consequences are worth your attention. `Power` is `Start`/`Stop`/`Delete`
only, and `Restart` has its own interface. The reason: two backends can merely
fake a reboot with a stop and a start. The contract exists so that no backend
can claim a fake. And an adapter must be constructible from a bare
`InstanceRef`: derivation probes the type, never a live connection, so the
mechanism works offline and in tests.

The first surface converted is the TUI's power/pause/migrate path, which was a
per-backend if/else ladder per action. The behavioural win is the refusals. The
ladder's final `else` sent every unknown backend to local QEMU. Its pause and
migrate branches did nothing at all off KubeVirt. Now an unsupported action names
the backend and points here.

**3. Close the gaps, cheapest-first per backend.** The lists below come from the
matrix, so they stay current. The notes name the native mechanism, so none of
these start from a blank page.

**4. Add the Proxmox backend** per ADR-0009. *Done for the operations above:*
`pkg/proxmoxbe` drives a real PVE cluster over its HTTPS API. It deliberately
did **not** add a sixth arm to the `switch` sites. Instead, it registers a
`pkg/snapshot` adapter (the one contract that exists) and satisfies
`types.Backend`, and leaves the rest behind `Client` methods for step 2 to attach.

Its consoles are the honest exception: the ticket code is in place, but the
websocket bridge is not. So the capability flags say no, and the matrix says why.

## Gaps by backend

### qemu — 9 gaps

- **tty** — the serial socket, which is already in the generated unit
- **rdp** — the same probe and bridge over the hostfwd port
- **clone** — qemu-img convert plus a new unit
- **template** — the same mark in the local registry
- **scale** — rewrite the unit and restart
- **volumes** — qemu-img create plus a unit edit
- **expand** — qemu-img resize while stopped
- **gpu** — vfio-pci in the generated unit
- **tags** — the local registry, which already holds per-VM state

### incus — 11 gaps

- **vnc** — `incus console --type=vga` for Incus VMs; the web `vncBridge` handles local, libvirt, and cluster namespaces only
- **rdp** — same, via the instance address
- **migrate** — incus move, including between remotes
- **clone** — incus copy
- **template** — incus publish, or the registry mark
- **scale** — incus config set limits.cpu / limits.memory, live
- **volumes** — `incus storage volume attach`
- **expand** — incus config device set … size
- **gpu** — incus config device add … gpu
- **tags** — instance config `user.corral.tag.<name>`
- **ports** — incus config device add … proxy

### libvirt — 11 gaps

- **ssh** — the domain's address via the guest agent or DHCP leases, then plain ssh. pkg/libvirt has SSH, but the TUI does not offer it, because the capability table omits it
- **tty** — virsh console
- **rdp** — same, via the domain address
- **migrate** — virsh migrate --live to another URI
- **clone** — virt-clone
- **template** — the registry mark
- **scale** — virsh setvcpus / setmem
- **volumes** — virsh attach-disk / detach-disk
- **expand** — virsh blockresize
- **gpu** — hostdev in the domain XML
- **tags** — domain metadata

### proxmox — 4 gaps

- **tty** — termproxy tickets work (pkg/proxmoxbe.TermTicket); the web UI has no websocket bridge for them yet
- **vnc** — vncproxy tickets work (pkg/proxmoxbe.VNCTicket); the web UI has no websocket bridge for them yet
- **rdp** — same, via the guest address
- **containers** — pkg/proxmoxbe.Containers lists them and Create makes them; pkg/ct does not yet surface a non-Kubernetes CT

## Testing parity

Three layers, and each one catches what the others cannot:

- **Conformance** (`pkg/backend`) — the claims agree with each other and with
  this document. Pure data, no cluster.
- **Per-backend unit tests with `shell.Fake`** — each backend issues the right
  native command with the right arguments for each operation. "Does Incus LXC
  work at all" gets its answer here: the tests assert the commands, not the
  daemon's behaviour.
- **Real-backend e2e** — `.github/workflows/e2e.yml` runs kind with emulated
  KubeVirt. `.github/workflows/e2e-incus.yml` runs a real Incus daemon, a real
  libvirt, and local QEMU on one runner. It checks a triple-backend aggregate
  inventory and the snapshot/export/device adapters against the real tools. It
  also asserts the container-versus-VM split in both directions. A container is
  in `ct list` and *not* in `list`; a virtual machine is the other way round.
  Proxmox cannot run in CI at all. As the honest substitute, ADR-0009 records
  `httptest` against recorded payloads plus a documented manual pass. That is
  the same admission that `docs/testing.md` makes about real KVM hardware.
