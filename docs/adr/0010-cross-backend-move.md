# ADR-0010: Moving an instance between backends

**Status:** accepted, implemented
**Date:** 2026-07-31

## Context

Corral is an aggregator of five backends. An operator who runs more than one
eventually wants an instance to change the backend it lives on. A VM prototyped
on a laptop's QEMU belongs on the cluster. An operator retires a Proxmox guest onto
KubeVirt. A KubeVirt VM needs to come back down to libvirt for a hardware
passthrough that the cluster cannot give it.

`migrate` already exists and means something else. It moves a guest between
*nodes of one backend*, live where the backend supports it (ADR-0009's
precondition check, KubeVirt's `VirtualMachineInstanceMigration`). This ADR is
about the other axis, and that axis is necessarily **cold**. There is no shared
memory state between a KubeVirt VMI and a systemd-managed QEMU process. So the
guest stops, its disk moves, and it starts again somewhere else. One name for
both would be a dangerous overload: it could stop someone's production VM when
they expected a live move.

The pieces are already contracts. That is why Corral should do this now, as one
general feature and not as a special case per pair:

- **Disk out** — `pkg/export` (#131) is backend-neutral. It has adapters for
  KubeVirt, QEMU, libvirt, and Incus, and the formats `qcow2` / `raw.gz` /
  `incus-tar`. It reports progress. It puts `snapshot.Consistency` on the
  result, so an export says what it captured. It already refuses a live
  instance where the export would produce a torn image.
- **Disk in** — `pkg/bootc.Target` means "puts a built disk onto a backend as a
  runnable instance". It has implementations for QEMU and libvirt. KubeVirt
  ingests through CDI (`ImportDataVolume` from a URL, `UploadDataVolume` from a
  local file). Proxmox creates with `import-from` on `scsi0`.
- **Everything else** — `pkg/backend`'s operation contract (ADR: parity step 2)
  gives uniform power control. `types.InstanceRef` gives an identity that
  already spans backends.

What is missing is the composition, an honest preflight, and a decision about
which pairs are supported at all.

## Decision

### It is called `move`, never `migrate`

`corral move <instance> --to <context>` and `POST /api/vms/{ns}/{name}/move`.
The distinction matters, and every surface states it: **a move stops
the guest.** Where a backend can migrate within itself, that stays `migrate` and
stays live.

### The pipeline, with the source destroyed last

    preflight → export → convert → ingest → verify → (retire source)

The source instance ends up **stopped, not deleted**. Corral deletes it only
when the operator asks (`--delete-source`, default off). A move can produce an
instance that works on the destination and leave a stopped one behind. That is
a good outcome. A move that deleted the source and then failed to ingest is
unrecoverable. The default therefore leaves both, and the surfaces say so.

### Supported pairs, and the one that is not

| From ↓ To → | qemu | libvirt | kubevirt | proxmox | incus |
|---|---|---|---|---|---|
| **qemu** | — | yes | yes | yes¹ | **no** |
| **libvirt** | yes | — | yes | yes¹ | **no** |
| **kubevirt** | yes | yes | — | yes¹ | **no** |
| **proxmox** | yes | yes | yes | — | **no** |
| **incus (VM)** | yes | yes | yes | yes¹ | — |

¹ Proxmox as a *destination* needs a disk-ingest path; see below.

This table is the intended end state. **The code today covers less.** It
refuses every other cell and does not try to approximate it. See *First slice*
below for exactly which cells are live.

**Incus cannot be a destination.** `bootc.TargetFor` already assessed this and
refused, and the reason still holds. An Incus VM boots from Incus's own image
store. `incus import` takes an Incus backup tarball, not a disk image. A raw
disk on an `--empty` VM leaves the guest without the agent, config drive, and
metadata that Incus expects. In the words of that refusal: *"the result would
look like it worked and then behave unlike every other Incus instance."*

The honest path is to publish Incus images, and that is a separate feature. Incus
remains a fine **source**, because Corral can export the VM's disk out of it.

**Containers are not in this graph at all.** An Incus LXC container and a
pet-pod CT have no disk image. They are a rootfs and a PVC. To turn one into a
VM, you must put a kernel and a bootloader into a filesystem that never had
them. That is a *rebuild*, not a move, and its failure mode is a guest that
imports cleanly and then does not boot. It is out of scope, and `move` refuses
a container by name and does not try something shaped like success.

### Preflight refuses before anything is touched

This step is what makes the move safe and not a gamble. It runs first and changes nothing.
It reports every reason that the move would not work — all of them, not only
the first:

- **Firmware.** A UEFI guest that lands on a BIOS-default target boots to a
  blank screen. The preflight reads the firmware from the source (KubeVirt's
  `firmware.bootloader.efi`, libvirt's `<loader>`, PVE's `bios: ovmf`). It sets
  the same firmware on the destination, or refuses when the destination cannot
  express it.
- **Disk bus and drivers.** Consider a Windows guest that moves from PVE's SATA
  default onto virtio-scsi. Without virtio drivers already in place, it will not
  boot. The
  guest OS is known from the source config where the backend records it. Where
  the backend does not record it, the preflight *warns* and does not assert.
- **Space.** The preflight compares the source's virtual size with the free
  space on the destination and in local scratch. The artifact lands on local
  disk first.
- **Address change.** The guest gets a new MAC and (almost always) a new IP.
  Anything pinned to either breaks. This is a warning, never a refusal, because
  it is the operator's call. But the preflight always says it.
- **Capability.** The destination must have the ingest path at all.
  `pkg/backend` can already answer that question.

`--dry-run` prints the plan and the warnings and exits. The web UI shows the
same list before the button commits.

### Proxmox ingest is the one place ADR-0009's "API only" bends

PVE's API cannot accept a raw disk image on older versions: the upload endpoint
takes `iso`, `vztmpl`, and `backup` content, not `images`. So a move *into*
Proxmox resolves one of three paths at preflight, in order:

1. A storage that advertises the `import` content type (PVE 8.4+) —
   `StorageInfo.Holds("import")` already answers this.
2. A shared storage path that Corral can write to directly.
3. SSH to a node plus `qm importdisk`.

If none is available, the move refuses and names those three options. We say
this loudly because ADR-0009 chose the API precisely so that Corral does not
need SSH. A *destination* move is the one operation that may still need it. An
operator should learn that from a refusal, not from a half-moved VM.

### Configuration travels, deliberately incompletely

Cores, memory, disk size, firmware, guest OS type, tags, and the folder
membership (ADR-0008) follow the instance. What does not follow: the MAC, the
IP, the node placement, backend-specific settings (KubeVirt instancetypes, PVE
HA groups), and anything the destination cannot express. The move reports what
it dropped. Assume that a move silently drops a passthrough device or a pinned
NUMA layout. That is exactly the kind of loss that turns up three weeks later
as a performance mystery.

### The contract

`pkg/move`, in the shape the other backend-neutral contracts already use:

```go
type Plan struct {
    Source, Destination types.InstanceRef
    Steps               []Step
    Warnings            []string
    Refusals            []Refusal   // non-empty means it will not run
    EstimatedBytes      int64
}

type Mover interface {
    Plan(src types.InstanceRef, dst Target) (Plan, error)
    Execute(ctx context.Context, plan Plan, progress ProgressFunc) (Result, error)
}
```

`Ingester` joins `pkg/backend`'s families as the destination half. So "can this
backend receive a disk" becomes a type assertion that the parity matrix derives
from, exactly like every other operation. `bootc.Target` is the existing
implementation of that idea for two backends. If we generalise it here, bootc
and move share a single ingest path, and a second one does not grow.

## First slice: what is actually wired

`pkg/move` and `corral move` exist. They now wire every cell that the table
above promises, except the Incus destination. Corral refuses that one by design:

| From ↓ To → | qemu | libvirt | kubevirt | proxmox | incus |
|---|---|---|---|---|---|
| **kubevirt** | **yes** | **yes** | — | **yes** | no |
| **qemu** | — | **yes** | **yes** | **yes** | no |
| **libvirt** | **yes** | — | **yes** | **yes** | no |
| **proxmox** | no¹ | no¹ | no¹ | — | no |
| **incus (VM)** | **yes** | **yes** | **yes** | **yes** | — |

¹ Proxmox as a *source* needs an export adapter, which `pkg/export` does not
have yet. It is a destination, not yet a source — the mirror image of Incus.

Two things narrow it beyond what the ADR anticipated, each with a refusal that
names the reason:

- **The four ingest paths, and what each costs.** qemu and libvirt delegate to
  `bootc.Target`, which already puts a disk onto them. So a bootc disk and a
  moved disk land the same way. KubeVirt uploads through CDI: `virtctl
  image-upload` creates the DataVolume. The VM then adopts the new PVC as its
  boot disk. It uses `PVC`, never `ImportURL`, since the disk is already in the
  cluster. Proxmox uploads to a storage that advertises the `import` content
  type, and creates with `import-from`. Where no such storage exists, it
  refuses with the three ways forward. That is the bend in ADR-0009 that this
  ADR describes.
- **Firmware travels now.** Before, Corral refused a UEFI guest everywhere but
  libvirt. KubeVirt sets `firmware.bootloader.efi`, and PVE sets `bios: ovmf`
  plus an `efidisk0`. So only qemu still refuses one, because its generated
  systemd unit has no OVMF path. Secure Boot stays off on both. It needs an EFI
  vars volume and a signed bootloader. If Corral enabled it silently, it would
  break exactly the imported guests this serves.
- **Incus is a source, not a destination.** The Incus adapter in `pkg/export`
  grew a `qcow2` format for this. It exports the instance archive to scratch,
  pulls `backup/virtual-machine.img` out of it, and converts. The path through
  the archive, not the storage pool, is deliberate. The pool layout differs per
  driver, usually needs root, and is not reachable at all for a remote
  instance. But `incus export` works the same way everywhere and over the
  network. The archive stays the *native* format, because it is the right
  artifact for a backup (configuration and every volume). The qcow2 is only the
  boot disk. A container archive has no such member. The refusal says so in
  those words, and does not fail later inside `qemu-img`.
- **Ingest takes only qcow2.** `raw.gz` is a disk, but a compressed one. The
  ingest path hands the file to `qemu-img convert`, which does not read gzip.
  Every backend that can export offers qcow2. So Corral loses nothing when it
  names the constraint, and it does not produce an artifact that the
  destination rejects.

### The web surface: drag to propose, never to commit

`POST /api/move/preflight` and `POST /api/move` are two endpoints, not one, and
the split is what makes drag-and-drop safe. The tree in Pool View holds two
kinds of node, and they behave differently by design:

- A drop of a VM onto a **pool** reassigns folder membership. It does not touch
  the VM itself, so it commits immediately. A drag back undoes it.
- A drop of a VM onto a **backend** proposes a move. The drop calls the
  preflight, which changes nothing, so a stray gesture is safe. What comes back
  *is* the dialog: the steps, the warnings, the dropped configuration, and any
  refusals. A refused plan has no confirm button.

The UI renders inert any backend that cannot receive a move, and shows the
reason on hover. It does not accept a drop and refuse afterwards.
`POST /api/move` re-runs the preflight server-side before it commits. So a client
cannot skip the check, and the server catches a plan that went stale between
the drop and the click. A refused preflight is a 200 (the refusals *are* the
answer); a refused commit is a 409 with the same list.

The instance's folder membership follows it to the destination. A move that
silently drops a VM out of the group that an operator organised it into is a
worse surprise than the IP change.

- **Firmware and guest OS come from the source.** Corral does not assume them.
  `move.Inspect` goes through a `backend.Inspector` family: KubeVirt's
  `firmware.bootloader.efi`, libvirt's `firmware='efi'` or an OVMF `<loader>`,
  PVE's `bios: ovmf` and `ostype`. An Incus VM always answers UEFI, because
  Incus boots its VMs under OVMF with no BIOS option. That is the one backend
  where the fact belongs to the backend, not the instance. It also means an
  Incus VM cannot move to qemu until qemu's generated unit grows a firmware
  path. qemu is also the one backend that Corral cannot *ask*, because its unit
  records no firmware. So a qemu source inspects to unknown. That downgrades
  the refusal to the unknown-OS warning, and Corral does not assert BIOS.

Another deviation from the sketch above: `Preflight` takes the `types.VM` that
the caller's inventory already holds, not an `InstanceRef`. That keeps
`pkg/move` out of the listing business, the same shape that `pkg/web`'s folder
actions use. Firmware and guest OS ride alongside it in `move.Source`.
`Inspect` fills them, not the listing. A config read per instance is the right
price at preflight. It is the wrong one on the dashboard's five-second poll.

## Consequences

- The move is slow and resumable-ish: a 40 GiB export, convert, and upload
  takes minutes to hours. It runs as a task with progress. The web UI already
  has a task log, and Proxmox's UPIDs give real progress on that side. A
  failure leaves the source stopped but intact.
- Corral now needs space for local scratch, and the preflight checks it.
- `pkg/export` gains no new API. `pkg/bootc.Target` becomes the seed of
  `backend.Ingester`. That is a refactor with two existing implementations, and
  their e2e tests are already in place.
- CI can cover qemu ⇄ libvirt on the existing `e2e-incus` runner. Both are
  installed there today, and it already exercises the export adapters against
  the real tools. KubeVirt and Proxmox destinations stay unit-tested plus
  manual. That is the same honesty that `docs/backend-parity.md` applies
  elsewhere.

## Alternatives considered

**Call it `migrate --to-backend`.** Rejected: the flag would hide a stop inside
a verb that means "no downtime" everywhere else in the tool.

**`virt-v2v`.** The right tool for VMware and Hyper-V conversions, and a
plausible future dependency for those. Rejected for this ADR, because every
pair here is already qemu-family. The disks are qcow2 or raw and need no guest
conversion. So it would add a heavy dependency to solve a problem that this
particular graph does not have.

**Stream disk-to-disk without local scratch.** Attractive for large disks, and
possible for some pairs (CDI can import from a URL that Corral serves).
Rejected for the first slice: it multiplies the failure modes and needs a
reachable listener. Also, the artifact-on-disk path is the one that is
debuggable when it goes wrong. Worth another look once operators trust the
plain path.

## Not in scope

Live cross-backend migration (there is no shared state to move), container
conversion, and VMware/Hyper-V import. The last is a natural follow-on once
`Ingester` exists, and is where `virt-v2v` would earn its place.
