# Corral as a CI boot gate for bootc images

Building a bootc OS image proves it *assembles*. It does not prove it
*boots* — bootloader installs, initramfs contents, display-manager wiring,
and compression formats all fail in ways `podman build` can't see. Corral
turns "does this image actually boot?" into one command with an exit code,
which makes it a publish gate: image builds → corral boots it → only then do
tags get promoted or ISOs uploaded.

This page documents that use case on both backends. The reference consumer
is [tuna-os/tunaOS](https://github.com/tuna-os/tunaOS), which gates GHCR tag
promotion on a QEMU boot of every image (see its `docs/PIPELINE.md`), with
the same checks runnable against a KubeVirt cluster for local development.

## The one-liner (QEMU backend, any KVM machine or CI runner)

```bash
corral create gate --bootc ghcr.io/tuna-os/yellowfin:gnome-testing \
  --wait-ssh --timeout 900
# exit 0  → disk built via `bootc install to-disk`, VM booted, root SSH answers
# exit ≠0 → it didn't; fail the pipeline
corral delete gate
```

`--wait-ssh` implies starting the VM and blocks until a root SSH probe over
the forwarded port succeeds. The SSH key is injected at install time with
`--root-ssh-authorized-keys` — the published image is never modified.

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
- **Registry cache**: deploy `deploy/registry-cache.yaml` (a ghcr.io
  pull-through cache) and builders use it automatically — multi-GB desktop
  images pull at LAN speed after the first fetch. `CORRAL_REGISTRY_MIRROR=off`
  disables detection; note the cache is one-upstream-per-instance, so a
  quay-pointed registry:2 cannot serve ghcr content.
- **Interrupted builds**: if the builder finished but the final VM step was
  lost, `corral bootc create --resume <name>` reuses the completed disk PVC
  instead of rebuilding.

## Troubleshooting the gate

| Symptom | Cause / fix |
|---|---|
| VM lands in the UEFI setup menu (UiApp) | disk has no portable bootloader — installs must use `--generic-image`; NVRAM entries written in a builder VM never reach the final VM |
| `501 Unsupported client range` during pull | zstd:chunked partial pulls need multi-range HTTP; GHCR's CDN refuses them and some podman versions don't fall back. Builders set `enable_partial_images = "false"`; if you hit this elsewhere, do the same in `storage.conf` |
| `no space left on device` in `/var/tmp` mid-pull | full pulls stage blobs in `$TMPDIR` before committing to storage — point TMPDIR at a big disk (builders stage on the scratch disk) |
| ostree: `min-free-space-percent '3%' would be exceeded` | target disk too small for the extracted image + reserve — desktop images generally want ≥ 32G |
| SSH never answers but the build reported OK | KubeVirt VMs are created **stopped** — `corral start <name>` first; then check the DM/sshd actually exist in the image |
| Cluster ssh works, CI ssh refused at `127.0.0.1` | expected: without a tailnet the hostfwd binds loopback, which is where `--wait-ssh` probes; interactive `corral ssh` needs the tailnet |

Every row above was hit for real while gating TunaOS images — this table is
field notes, not speculation.
