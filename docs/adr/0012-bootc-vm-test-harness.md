# ADR-0012: A bootc VM test harness in corral

**Status**: Accepted
**Date**: 2026-09-12

## Context

Corral could already boot a bootc image and wait for SSH
(`corral create --bootc --wait-ssh`). That answers one question: did it boot.

A test of a real image asks more, and the org already asks it. TunaOS gates the
promotion of its images with `scripts/iso-e2e.sh`. That is about four thousand
lines of bash. It boots QEMU and reads a serial console for a readiness marker.
It drives the monitor socket to type at a console. It dumps the framebuffer on
a timer, then measures each frame's standard deviation with ImageMagick.

That last step catches a desktop that never painted. wootc assembles its own
frames into a WebM with ffmpeg, for the same reason. Both projects rebuilt the
same harness, in bash, around the same QEMU monitor that corral already opens
for `corral screenshot`.

Those harnesses need six things corral did not offer:

- A test account with a password. `bootc install` knows only about root's SSH
  key. Every test that logs in as a human needs more than that.
- Software the test needs and the published image does not ship.
- A hook that runs in the booted guest, whose exit code is the verdict.
- The guest's console. QEMU ran with `-display none` and no serial chardev, so
  a boot that failed before sshd said nothing at all.
- Screenshots as evidence, and a verdict on what a frame shows.
- A machine-readable result, and one exit code per failure class.

## Decision

Add `pkg/vmtest` and `corral vmtest`. It takes an image reference and hands
back a system that runs and answers.

Four choices carry the design.

**A derived image holds the customisation. The published image does not
change.** Accounts, passwords, packages, files and the hook become one thin
layer on top of the reference under test. A spec that asks for nothing builds
no layer, so the default path still tests the published bytes. The alternative
was a mount of the built disk and an edit in place. That needs root, loop
devices, and code per filesystem. For the same reason, the local builder
refuses any image that uses composefs.

**remora generates the layer where an operator installed it.**
`tuna-os/remora` is the org's own tool for local layers: six package managers,
a resolved package lockfile, and `bootc container lint`. Its extension points
are `build_files/*.sh`, a `system_files/` overlay, `packages` and `extra_run`.
That is the shape this needs, so corral writes such a context and calls
`remora generate`. remora keeps its code in `internal/`, so corral cannot
import it as a library. A demand for its binary on every CI runner is also too
much. So corral keeps a built-in generator for the same context, and
`--layer-engine` selects one.

**Readiness has two gates, not one.** SSH answers for most images. A regular
expression on the serial console answers for the rest, which is the normal case
for a production desktop image. Every local VM now writes its console to a file
(`-chardev file,append=on`), and `vmtest` installs `console=ttyS0` as a kernel
argument at install time. The log therefore survives a reboot.

**corral measures blankness. Nobody has to look.** The framebuffer arrives as a
PPM over QMP, and corral already decodes it. The standard deviation of its
luminance is a few lines of Go, not a dependency on ImageMagick. TunaOS
calibrated the threshold (0.02) on real runners.

Each failure class gets its own exit code. A pipeline can then tell "this
runner has no KVM" (2) from "the image does not boot" (6) from "the desktop
never drew" (9).

## Consequences

- One command and one YAML file replace a bash harness of thousands of lines.
  The parts that were ImageMagick and socat are now Go over the socket corral
  opens anyway.
- Corral now writes a container image. That is new behaviour for a VM manager.
  A customised run needs podman, which `bootc install` already needed.
- A run with passwords also loosens sshd in the derived image
  (`PasswordAuthentication yes`). This is deliberate, and it applies to a
  disposable test VM. It does not touch the published image.
- `corral create -f` and `corral vmtest -f` read the same field names, with one
  difference. `create` runs `provision:` offline in the installed disk;
  `vmtest` runs it in the booted guest, as Lima does. The docs state the
  difference. A change to `create` would break the pipelines that use it.
- The KubeVirt path does not change. `vmtest` drives local QEMU only: it needs
  the monitor socket for frames and keys, and a file for the console. A cluster
  version needs a different transport for both, and its own change.

## Alternatives considered

- **A wrapper around `iso-e2e.sh`.** It is TunaOS-specific, ISO-first, and
  bash. Its mechanisms are worth a port. Its implementation is not.
- **cloud-init.** Most bootc images do not ship it. A seed ISO would configure
  some images and skip others in silence.
- **An edit of the built disk.** It needs root, loop devices, and code per
  filesystem. It also fails on the composefs desktop images that matter most.
