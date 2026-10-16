# Corral and the KubeVirt ecosystem

This page tells how Corral relates to KubeVirt. It also gives two drafts that
a person must post. The first is a message to the KubeVirt community. The
second is a pull request that adds Corral to the KubeVirt adopters list.

## Position

Corral is a VM manager with a CLI, a TUI and a web dashboard in the style of
Proxmox VE. KubeVirt is one of its backends. Corral does not replace KubeVirt
and does not extend its API. It is a client of the KubeVirt API.

What Corral does with KubeVirt:

- It drives KubeVirt through `kubectl` and `virtctl`. It installs no operator,
  no agent and no CRDs of its own.
- It creates, starts, stops, pauses, deletes, clones, migrates and snapshots
  `VirtualMachine` objects (`corral create`, `corral migrate`,
  `corral snapshot` and more).
- It hotplugs disks (`corral adddisk`, `corral rmdisk`), bridges secondary
  NICs with Multus (`corral addnic`, `corral networks`), and exposes a VM on
  the LAN with a LoadBalancer Service (`corral lanservice`).
- It manages templates of golden VMs (`corral template`) and collects ephemeral
  VMs (`corral gc`).
- It builds a VM disk from a bootc container image in a builder VM on the
  cluster (`corral create --bootc`).
- It rebuilds that disk from a newer image (`corral bootc upgrade`).
- It shows KubeVirt VMs in one fleet view with VMs on local QEMU, Incus,
  libvirt and Proxmox VE.
- The VDI plugin runs desktop pools on KubeVirt. Its self-service broker is
  Stable and has an end-to-end CI test on kind (see [docs/vdi.md](../vdi.md)).

[docs/backend-parity.md](../backend-parity.md) lists each operation for each
backend. A generator writes it from the code, and CI fails if the two are
different.

## Who it is for

- Teams that run KubeVirt and want a dashboard and a TUI for VM operators.
- People who develop on a laptop with QEMU and deploy to KubeVirt, and want
  the same commands in both places.
- Teams that ship bootc OS images and want to run them as KubeVirt VMs.

## Asks to the KubeVirt community

1. Technical review: is the way that Corral uses the KubeVirt API correct?
   Are there APIs it should use and does not?
2. Feedback from users who try it on their cluster.
3. A row in the KubeVirt adopters list, type "Integration".

---

## Draft: community message

Post to the kubevirt-dev list
(https://groups.google.com/g/kubevirt-dev) and to `#virtualization` on the
Kubernetes Slack (https://kubernetes.slack.com). Use the same text for both.

```text
Subject: Corral: a CLI/TUI/web VM manager that uses KubeVirt as a backend

Hi all,

I work on Corral (https://github.com/tuna-os/corral), an Apache-2.0 VM
manager in one Go binary. It has a CLI, a TUI and a web dashboard in the
style of Proxmox VE. KubeVirt is its main backend.

Corral is a client only. It uses kubectl and virtctl, and installs no
operator and no CRDs. It does VM lifecycle, clones, live migration,
snapshots, disk hotplug, Multus NICs and templates. It can also build a VM
disk from a bootc container image with a builder VM on the cluster.
We released v0.7.0 on 2026-10-15.

You can try the UI without a cluster: `corral web --demo` runs it against
an in-memory fake cluster.

I would like feedback on how Corral uses the KubeVirt API. The per-backend
feature table is at
https://github.com/tuna-os/corral/blob/main/docs/backend-parity.md.
I also plan to open a PR that adds Corral to ADOPTERS.md as an integration.
Please tell me if that is not the correct place.

Thanks,
<name>
```

---

## Draft: adopters pull request

The list is in `ADOPTERS.md` at the root of
[kubevirt/kubevirt](https://github.com/kubevirt/kubevirt/blob/main/ADOPTERS.md).
The note at the top of that file asks for a pull request. Each type is in
alphabetical order. A script makes the website data (`kubevirt/kubevirt.github.io`,
`_data/adopters.yml`) from this file. The logo is a separate pull request
to that repository after the merge.

Add this row in the "Integration" rows, after the `CloudCasa by Catalogic`
row and before the `Isovalent` row:

```markdown
| Integration | Corral | 2026 | [link](https://github.com/tuna-os/corral) | Corral is an open source VM manager (CLI, TUI and web dashboard) that uses KubeVirt as a backend through kubectl and virtctl. It manages VM lifecycle, migration, snapshots, disk hotplug and templates, and builds VMs from bootc container images on the cluster. |
```

**PR title:** `ADOPTERS.md: add Corral as an integration`

**PR body:**

> Adds Corral (https://github.com/tuna-os/corral) to the Integration
> section of ADOPTERS.md, in alphabetical order.
>
> Corral is an Apache-2.0 VM manager with a CLI, a TUI and a web dashboard.
> It uses KubeVirt as a backend through kubectl and virtctl, with no operator
> or CRDs of its own.
>
> ```release-note
> NONE
> ```

KubeVirt needs a DCO sign-off on each commit. Commit with
`git commit -s`.
