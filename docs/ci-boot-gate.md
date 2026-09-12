# Corral as a CI boot gate for bootc images

A build of a bootc OS image proves that it *assembles*. It does not prove that
it *boots*. Bootloader installs, initramfs contents, display-manager wiring and
compression formats all fail in ways `podman build` cannot see. Corral turns
"does this image boot?" into one command with an exit code. That makes it a
publish gate: the image builds, corral boots it, and only then does the
pipeline promote a tag or upload an ISO.

This page documents that use case on both backends. The reference consumer is
[tuna-os/tunaOS](https://github.com/tuna-os/tunaOS). It gates every promotion
of a GHCR tag on a QEMU boot of the image (see its `docs/PIPELINE.md`). The
same checks run against a KubeVirt cluster for local development.

Start with `corral vmtest`, below. It is the full harness. It customises the
image, boots it, collects the evidence, and hands the VM over for your tests.
`corral create --wait-ssh` is the smaller gate: one exit code, no artifacts. It
is still there for a pipeline that only asks "did it boot".

## `corral vmtest` — the whole job in one command

`corral vmtest` builds the disk, boots it, waits for the guest, and runs your
assertions. It then leaves the VM up, so the next step can test it. It also
adds what a test needs and the published image does not have: accounts,
passwords, packages, files, and a hook for the first boot.

```bash
corral vmtest gate --bootc ghcr.io/tuna-os/yellowfin:gnome-testing \
  --user tester --password hunter2 --sudo-user \
  --package jq \
  --check 'systemctl is-active sshd' \
  --check 'systemctl --failed --no-legend' \
  --video
# exit 0 → the image booted, the hook passed, every check passed
ssh -i corral-vmtest-out/ssh/id_ed25519 -p 2242 tester@127.0.0.1
corral delete gate
```

Add `--rm` for a pure gate, where nothing runs after it.

### What it writes

Every run fills an artifact directory (`--artifacts`, default
`corral-vmtest-out/`). Upload it from CI and a failure is diagnosable without
a second run:

| File | What it tells you |
|---|---|
| `result.json` | the whole run: verdict, boot time, each check, each frame |
| `serial.log` | the guest console, from the firmware to the readiness marker |
| `frames/f*.png` | one screenshot per `--screenshot-interval` during the boot |
| `ready.png` / `failure.png` | the screen at the moment the run ended |
| `timelapse.webm` | the boot as a video (`--video`, needs ffmpeg) |
| `diagnostics/` | failed units, `bootc status`, journal warnings |
| `layer/Containerfile` | exactly what the run added to the image |
| `ssh/id_ed25519` | the run's own keypair |

### Exit codes

One code per failure class, so a pipeline can branch on the answer:

| Code | Meaning |
|---|---|
| 0 | passed |
| 1 | the spec cannot run (no image, a bad regular expression) |
| 2 | this host cannot run it (no podman, no qemu, no loop device) |
| 3 | the layer build failed (a package that does not exist) |
| 4 | the disk build failed (`bootc install`) |
| 5 | the VM did not start |
| 6 | the guest never became ready |
| 7 | the post-boot hook failed |
| 8 | a check failed |
| 9 | the guest painted nothing, and `--require-paint` was set |

Code 2 is the important one. It separates a broken runner from a broken image,
which is the distinction a red pipeline usually hides.

### Users, passwords and packages: the derived layer

Anything the spec asks for goes into a thin image layer built **on top of** the
reference under test. The published image is never modified. With no
customisation asked for, corral builds no layer at all and boots the image
exactly as published.

The layer adds:

- **Accounts.** `--user tester --password hunter2 --sudo-user` creates the
  account, sets the password, and grants sudo with no password. The home
  directory lands in `/var/home`. On a bootc system `/home` is a symlink into
  `/var`, and only the image's `/var` reaches the installed disk. corral hashes
  every password on the host, so no plain password reaches the image.
- **Packages.** `--package jq` installs with the base image's own package
  manager. corral reads the image filesystem to find out which one it is — dnf,
  zypper, apt, pacman, or apk.
- **A hook for the first boot.** `--post-boot ./firstboot.sh` runs in the
  booted guest as a systemd oneshot unit. Its exit code is the run's verdict.
  Its output reaches the artifact directory, and its markers reach the serial
  console. A guest that never answers SSH therefore still reports.

Where a host has [remora](https://github.com/tuna-os/remora), corral asks
remora to generate the layer's Containerfile. remora is the same project's tool
for local layers. It knows six package managers. It resolves a package
lockfile, so an unchanged rebuild costs nothing. It also lints the result. Pick
one engine with `--layer-engine remora|builtin`.

### Images with no sshd

A production desktop image ships sshd in a disabled state. No SSH probe can
gate such an image. Gate on the console instead:

```bash
corral vmtest desk --bootc "$IMAGE" --ready-marker 'Reached target Graphical' \
  --require-paint
```

`--ready-marker` waits for a regular expression on the guest's serial console.
`--require-paint` fails the run when the last frame is blank: the standard
deviation of its luminance is at or under 0.02. A guest that boots and never
draws is the failure an SSH probe hides. Without this flag, no exit code
catches it and somebody has to look at a picture.

You can also drive the console keyboard directly, which is the only way into a
LUKS passphrase prompt or a greeter:

```bash
corral screenshot desk -o greeter.png
corral type desk 'correct horse battery staple' --enter
corral key desk ctrl alt f2
```

### The spec file (Lima-shaped)

Everything above fits in a file, and corral reads the Lima field names it
shares:

```yaml
# verify.yaml
bootc: ghcr.io/tuna-os/yellowfin:gnome-testing
cpus: 4
memory: 4GiB
disk: 32GiB
timeout: 20m

users:
  - name: tester
    password: hunter2
    sudo: true

packages: [jq, htop]
extraRun:
  - dnf config-manager --set-enabled crb

files:
  - path: /etc/corral-test.conf
    content: |
      test=1
    mode: "0644"

provision:
  - mode: image            # runs at build time, in the layer
    script: systemctl mask systemd-resolved
  - mode: system           # runs in the booted guest (Lima's own meaning)
    script: |
      systemctl is-system-running --wait

checks:
  - systemctl is-active sshd
  - bootc status --format json

screenshots:
  interval: 5s
  video: true
  requirePaint: false
```

```bash
corral vmtest gate -f verify.yaml
```

A flag beats the file, and only when you pass it. An unknown field in the file
is an error, not a warning: a misspelled `packages:` that installs nothing
wastes the whole run.

### GitHub Actions

```yaml
jobs:
  boot-gate:
    runs-on: ubuntu-24.04        # hosted runners have KVM
    steps:
      - name: Enable KVM
        run: |
          echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' \
            | sudo tee /etc/udev/rules.d/99-kvm4all.rules
          sudo udevadm control --reload-rules && sudo udevadm trigger --name-match=kvm

      - name: Install corral
        run: go install github.com/tuna-os/corral@latest

      - name: Boot gate
        run: |
          sudo -E "$(which corral)" vmtest gate --bootc "$IMAGE" \
            --check 'systemctl --failed --no-legend' \
            --video --rm

      - name: Upload the evidence
        if: always()
        uses: actions/upload-artifact@v7
        with:
          name: boot-gate
          path: corral-vmtest-out/
```

`sudo` is not optional: `bootc install` partitions a disk and installs a
bootloader. Run corral as root, or pass `--sudo` to let it call podman through
sudo itself.

## The smaller gate: `corral create --wait-ssh`

```bash
corral create gate --bootc ghcr.io/tuna-os/yellowfin:gnome-testing \
  --wait-ssh --timeout 900
# exit 0  → disk built via `bootc install to-disk`, VM booted, root SSH answers
# exit ≠0 → it didn't; fail the pipeline
corral delete gate
```

`--wait-ssh` starts the VM, then blocks until a root SSH probe over the
forwarded port succeeds. bootc install injects the SSH key with
`--root-ssh-authorized-keys`, and nothing changes the published image.

### Locally built images

Images in root podman storage work directly — no registry round-trip:

```bash
sudo podman build -t localhost/myos:test .
corral create gate --bootc localhost/myos:test --wait-ssh
```

(The install runs from `containers-storage:` when the ref is present
locally; `localhost/` refs error early with a `podman save | sudo podman
load` hint if the image is only in your rootless store.)

### Declarative form (Lima-style YAML)

Note that `corral create` runs `provision:` scripts **offline**, chrooted into
the installed disk. `corral vmtest` follows Lima's own meaning and runs them in
the booted guest, unless you mark one `mode: image`.

Corral reads Lima YAML natively; `bootc:` plus `provision:` covers the
common CI need — enable sshd or drop test hooks **chrooted into the
installed disk before first boot**, without touching the published image:

```yaml
# verify.yaml
bootc: ghcr.io/tuna-os/yellowfin:gnome-testing
cpus: 4
memory: 4GiB
disk: 32GiB
provision:
  - mode: system
    script: |
      #!/bin/sh
      systemctl enable sshd
```

```bash
corral create gate -f verify.yaml --wait-ssh --timeout 900
```

### GitHub Actions recipe

```yaml
jobs:
  boot-gate:
    runs-on: ubuntu-latest        # hosted runners have KVM
    steps:
      - name: Enable KVM
        run: |
          echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' \
            | sudo tee /etc/udev/rules.d/99-kvm4all.rules
          sudo udevadm control --reload-rules && sudo udevadm trigger --name-match=kvm

      - name: Install corral
        run: go install github.com/tuna-os/corral@latest

      - name: Boot gate
        run: |
          corral create gate --bootc "$IMAGE" --wait-ssh --timeout 900
          corral delete gate
```

## Beyond "it boots": health checks over SSH

Once `--wait-ssh` returns, the VM is a normal SSH target — assert whatever
"working" means for your image:

```bash
check() { corral ssh gate -u root -c "$1"; }
check "systemctl is-active graphical.target"   # desktop reached
check "systemctl is-active gdm"                # right display manager
check "systemctl --failed --no-legend"         # empty, or a known allowlist
check "bootc status --format json | jq -r '.status.booted.image.image.image'"
```

## KubeVirt backend (clusters, heavy images, local dev parity)

```bash
corral bootc create gate --image ghcr.io/tuna-os/yellowfin:gnome -n corral-vms
# or: corral create gate --kubevirt --bootc <image> [-s <storage-class>]
corral start gate      # bootc creates the VM stopped
```

The build runs in a builder VM **on the cluster** (`bootc install to-disk`
onto a PVC), which is the only way to install images whose filesystems the
node kernel can't handle (btrfs/composefs on Talos, for example). Notes
that matter in practice:

- **Storage**: disk PVCs are Filesystem-mode file-backed disks, so any
  provisioner works — including `local-path`. Block-mode provisioners are
  not required.
- **Registry cache**: deploy `deploy/registry-cache.yaml`, a pull-through cache
  for ghcr.io. Builders then use it on their own, and a desktop image of several
  gigabytes pulls at LAN speed after the first fetch.
  `CORRAL_REGISTRY_MIRROR=off` turns the detection off. One instance serves one
  upstream, so a registry:2 aimed at quay cannot serve content from ghcr.
- **Interrupted builds**: where the builder finished but the final VM step was
  lost, `corral bootc create --resume <name>` reuses the disk PVC that the
  builder completed. It does not build the disk again.

## Troubleshooting the gate

| Symptom | Cause / fix |
|---|---|
| VM lands in the UEFI setup menu (UiApp) | disk has no portable bootloader — installs must use `--generic-image`; NVRAM entries written in a builder VM never reach the final VM |
| `501 Unsupported client range` during pull | zstd:chunked partial pulls need multi-range HTTP; GHCR's CDN refuses them and some podman versions don't fall back. Builders set `enable_partial_images = "false"`; if you hit this elsewhere, do the same in `storage.conf` |
| `no space left on device` in `/var/tmp` mid-pull | full pulls stage blobs in `$TMPDIR` before committing to storage — point TMPDIR at a big disk (builders stage on the scratch disk) |
| ostree: `min-free-space-percent '3%' would be exceeded` | target disk too small for the extracted image + reserve — desktop images generally want ≥ 32G |
| SSH never answers but the build reported OK | KubeVirt VMs are created **stopped** — `corral start <name>` first; then check the DM/sshd actually exist in the image |
| Cluster ssh works, CI ssh refused at `127.0.0.1` | expected: without a tailnet the hostfwd binds loopback, which is where `--wait-ssh` probes; interactive `corral ssh` needs the tailnet |
| `vmtest` exits 2 | the runner cannot host a VM at all: no podman, no qemu, no `/dev/loop-control`, or corral is not root. The message names the missing one |
| `Re-exec in host mountns: ... Permission denied` | `bootc install` needs the host's mount namespace, and a container inside a container does not have one. Run corral on the runner, not in a container on it |
| `vmtest` exits 6 and `serial.log` is empty | the guest never reached the bootloader, or the VM predates console capture. Recreate it — the console karg is installed by `vmtest` itself, so a VM built another way may not have one |
| `vmtest` exits 6 and the console stops in dracut | an ostree install on the wrong filesystem. Composefs images need btrfs, and the local builder refuses them for that reason — build those on a KubeVirt context |
| `vmtest` exits 7 | the first-boot hook failed. Its own output is in `result.json` under `hook.log`, and on the console between the `CORRAL_POSTBOOT_FAIL` and `CORRAL_VM_READY` markers |
| `vmtest` exits 9 | the guest booted and painted nothing. Look at `ready.png` and the last frames: a greeter that crashed looks exactly like this |

Every row above was hit for real while gating TunaOS images — this table is
field notes, not speculation.
