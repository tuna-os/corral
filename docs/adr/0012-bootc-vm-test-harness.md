# ADR-0012: A bootc VM test harness in corral

**Status**: Accepted
**Date**: 2026-09-12

## Context

Corral could already boot a bootc image and wait for SSH
(`corral create --bootc --wait-ssh`). That answers one question: did it boot.

Real image testing asks more, and the org already does it. TunaOS gates image
promotion with `scripts/iso-e2e.sh` — about four thousand lines of bash that
boots QEMU, tails a serial console for a readiness marker, drives the monitor
socket to type at a console, captures framebuffer dumps on a timer, and
measures each frame's standard deviation with ImageMagick to catch a desktop
that never painted. wootc assembles its own frames into a WebM with ffmpeg for
the same reason. Both projects rebuilt the same harness, in bash, around the
same QEMU monitor that corral already opens for `corral screenshot`.

What those harnesses need and corral did not offer:

- A test account with a password. `bootc install` knows only about root's SSH
  key, so every test that has to log in as a human needs something else.
- Software the test needs but the published image does not ship.
- A hook that runs in the booted guest, whose exit code is the verdict.
- The guest's console. QEMU ran with `-display none` and no serial chardev, so a
  boot that failed before sshd said nothing at all.
- Screenshots as evidence, and a verdict on whether a frame shows anything.
- A machine-readable result, and an exit code per failure class.

## Decision

Add `pkg/vmtest` and `corral vmtest`: a harness that takes an image reference
and hands back a running, tested system.

Four choices are load-bearing.

**Customisation goes in a derived image, never in the published one.** Accounts,
passwords, packages, files and the hook become one thin layer built `FROM` the
reference under test. A spec that asks for nothing builds no layer, so the
default path still tests the published bytes. The alternative — mounting the
built disk and editing it — needs root, loop devices and filesystem-specific
code, and would not work on the composefs images the local builder already
refuses.

**remora generates the layer when it is installed.** `tuna-os/remora` is the
org's own local-layering tool: six package managers, a resolved package
lockfile, `bootc container lint`. Its extension points (`build_files/*.sh`, a
`system_files/` overlay, `packages`, `extra_run`) are the shape this needs, so
corral writes that context and calls `remora generate`. remora's internals are
in `internal/`, so importing it as a library is not possible, and requiring its
binary on every CI runner is not acceptable — hence a built-in generator for the
same context, and `--layer-engine` to choose.

**Readiness has two gates, not one.** SSH by default. A regular expression on
the serial console when the image has no sshd, which is the normal case for a
production desktop image. The serial console is now captured for every local VM
(`-chardev file,append=on`), and `vmtest` installs `console=ttyS0` as a kernel
argument at install time, so the log survives reboots.

**Blankness is measured, not eyeballed.** The framebuffer arrives as a PPM over
QMP and corral already decodes it, so the standard deviation of its luminance is
a few lines of Go rather than a dependency on ImageMagick. The threshold (0.02)
is the one TunaOS calibrated on real runners.

Exit codes are one per failure class. A pipeline can tell "this runner has no
KVM" (2) from "the image does not boot" (6) from "the desktop never drew" (9).

## Consequences

- A bash harness of thousands of lines becomes one command and a YAML file. The
  parts that were ImageMagick and socat are Go over the socket corral opens
  anyway.
- Corral now writes a container image. That is new behaviour for a VM manager,
  and it means podman is needed for any customised run — it already was, because
  `bootc install` runs from the image.
- A run with passwords loosens sshd in the derived image
  (`PasswordAuthentication yes`). This is deliberate and scoped to a disposable
  test VM, and the published image is unaffected.
- `corral create -f` and `corral vmtest -f` read the same field names with one
  difference: `create` runs `provision:` offline in the installed disk,
  `vmtest` runs it in the booted guest, as Lima does. Documented rather than
  unified, because changing `create` would break the pipelines using it.
- The KubeVirt path is untouched. `vmtest` is local QEMU only: it needs the
  monitor socket for frames and keys, and a serial file for the console.
  Extending it to the cluster means a different console and screenshot
  transport, and belongs in its own change.

## Alternatives considered

- **Wrap `iso-e2e.sh`.** It is TunaOS-specific, ISO-first, and bash. The
  mechanisms it proved are worth keeping; the implementation is not.
- **cloud-init.** Most bootc images do not ship it, so a seed ISO would
  configure some images and silently skip others.
- **Editing the built disk.** Needs root, loop devices, and per-filesystem code,
  and fails on exactly the composefs desktop images that matter most.
