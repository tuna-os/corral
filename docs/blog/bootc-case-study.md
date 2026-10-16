# From container image to VM fleet: bootc VMs with Corral

This case study shows one workflow from start to end. The OS is a bootc
container image in a registry. Corral builds a VM from it, tests that it
boots in CI, and moves the VM to the next build of the image. Each command on
this page exists in Corral v0.7.0. We checked each one against
`corral --help` of a build of `main`.

## The problem

A typical VM is mutable. An operator installs it once and then changes it by
hand. After some months, no two VMs are the same, and no file describes the
VM that runs.

A bootc image is different. The OS is an OCI image. A `Containerfile` in git
describes it, and a registry keeps each version. To change the OS, you change
the `Containerfile`, build, and push. Something must then turn that image into
a VM, and later move the VM to the next version. Corral does these steps.

## The workflow

The reference user is [tuna-os/tunaOS](https://github.com/tuna-os/tunaOS). It
publishes bootc images to GHCR and gates each tag on a QEMU boot.

### 1. Create a VM from the image

```bash
corral create dev1 --bootc ghcr.io/tuna-os/yellowfin:gnome --wait-ssh
```

Corral runs `bootc install to-disk` to build the OS disk from the image. On a
KubeVirt cluster, it does this in a builder VM on the cluster. On a laptop, it
uses local QEMU. It then boots the disk as a normal VM. `--wait-ssh` blocks
until SSH answers, and gives a nonzero exit code after `--timeout` seconds
(the default is 600).

To install a kernel argument from the first boot, add `--karg`:

```bash
corral create dev1 --bootc ghcr.io/tuna-os/yellowfin:gnome --karg tunaos.countme=0
```

### 2. Gate the image in CI with `corral vmtest`

If an image builds, that proves that it assembles. It does not prove that it
boots. `corral vmtest` boots the image in a local QEMU VM, runs your checks,
and gives one exit code per failure class:

```bash
corral vmtest gate --bootc "$IMAGE" --rm \
  --user tester --password hunter2 --sudo-user \
  --check 'systemctl is-active sshd' \
  --check 'systemctl --failed --no-legend'
```

Exit code 0 means that the image booted and each check passed. Exit code 2
means that the runner cannot run the test (for example, no podman or no
qemu). Exit code 4 means that `bootc install` failed. Exit codes 6 and 8 mean
that the guest did not become ready, or that a check failed. The full table
is in [the CI boot gate guide](../ci-boot-gate.md#exit-codes).

`--user`, `--password` and `--package` go into a thin layer on top of the
image under test. Corral does not change the published image. Each run writes
`corral-vmtest-out/` with `result.json`, the serial log and the boot
screenshots. Upload that directory from CI, and you can diagnose a failure
without a second run.

For a desktop image with no sshd, gate on the console:

```bash
corral vmtest desk --bootc "$IMAGE" --rm \
  --ready-marker 'Reached target Graphical' --require-paint
```

The pipeline promotes the tag only after `corral vmtest` exits 0.

### 3. Move the VM to the next build

The bootc commands come from the bootc plugin:

```bash
corral plugin install bootc
corral bootc status
corral bootc upgrade dev1
```

`corral bootc status` lists the bootc VMs and the image that Corral built
each one from. `corral bootc upgrade dev1` runs the on-cluster build again
from the latest build of the recorded image. It then swaps the new disk in
and restarts the VM. The VM keeps its size and its network settings. To move
a VM to a different image, use `corral bootc switch dev1 --image <ref>`.

`corral bootc upgrade` operates on KubeVirt VMs. It reads the image that it
recorded on the VM at create time.

## What this gives you

- The OS version is in git and in the registry, not in the VM.
- CI gives a "does it boot" result before a tag reaches users.
- One command moves a KubeVirt VM to the next build.
- `corral create --bootc` works on a laptop (local QEMU) and on a KubeVirt
  cluster.

## Limits

- `corral vmtest` runs on a local QEMU host. It needs podman and qemu, and
  `bootc install` needs root (`--sudo`).
- `corral bootc upgrade` and `corral bootc rebuild` are KubeVirt only.
- `corral bootc upgrade` builds a new disk and swaps it in. It is not an
  in-place `bootc upgrade` in the guest. Keep data that must stay on a
  separate disk (`corral adddisk`).

## More information

- [CI boot gate guide](../ci-boot-gate.md)
- [User guide](../user-guide.md)
- [v0.7.0 release notes](../releases/v0.7.0.md)
