# 🤠 Corral

**Herd your VMs — and containers — into your tailnet.**

You have VMs in two places: quick ones on your laptop, big ones on the
Kubernetes cluster in the closet. Two sets of tooling, two networking
stories, and none of it reachable from the couch.

Corral fixes that. One command, two backends, and every VM lands inside the
one network all your devices already share — your Tailscale tailnet.

```bash
corral create web --kubevirt --container-disk quay.io/containerdisks/fedora:42
corral ssh web        # from this machine, your laptop, or your phone's terminal
```

VMs are cattle. Stop treating each one like a networking project.

## Why you'll like it

- **Same commands everywhere.** `create` / `start` / `ssh` / `viewer` /
  `clone` / `delete` work identically whether the VM is local QEMU/KVM or
  KubeVirt on your cluster. Corral remembers which is which — you never
  specify it again.
- **Containers (CT) — distrobox on Kubernetes.** Proxmox-style pet pods
  alongside VMs (`corral ct create`). A privileged CT seeds a full root
  filesystem onto its own volume and `chroot`s into it on boot — `apt` /
  `dnf` / `apk` installs and dotfiles survive Stop/Start, the same way a
  real distrobox container survives being stopped and re-entered.
  Unprivileged (default) CTs get a simple `/data`-only mount instead.
- **SSH that just works.** Your public key is injected at create time, a
  fallback password is generated and stored locally, and `corral ssh` picks
  the right path: a Kubernetes API tunnel for cluster VMs, a Tailscale-bound
  port-forward for local ones. Zero config files touched.
- **VMs that join the tailnet themselves.** Drop a Tailscale auth key in
  `~/.config/tailvm/config.yaml` (or `TS_AUTHKEY`) and every cloud-init VM
  runs `tailscale up` on first boot — it shows up as a real machine on your
  tailnet, MagicDNS name and all.
- **Extensions with a marketplace.** Niche features ship as plugins:
  `corral plugin search`, `corral plugin install bootc`, then `corral bootc
  create dev --image ghcr.io/...` builds an OS disk *on the cluster* from a
  bootable container image and boots it as a VM. Browse/install from the web
  UI's **Extensions** tab too. The core binary stays lean.
- **Point-and-shoot TUI.** Run `corral` bare for a Bubble Tea interface:
  pick a VM, hit Start / Stop / SSH / VNC / Delete, or toggle which ports
  (SSH, VNC, RDP, HTTP, …) are published to the tailnet as
  `<name>-vm.your-tailnet.ts.net`.
- **A Proxmox-style web UI.** `corral web` serves a dark, mobile-friendly
  dashboard: datacenter → node → VM tree, live status, create wizard,
  start/stop/restart/pause, **one-click live migration** with a target-node
  picker, **multi-select bulk start/stop**, **tags** (chips + tree filter), a
  per-VM **CPU usage sparkline**, disk **export** (qcow2 or raw.gz),
  **your own image/ISO sources** alongside the built-in catalog (saved in a
  ConfigMap), and *real consoles in the browser* — noVNC graphics and an
  xterm.js serial TTY. It also
  runs **on the cluster itself** (`deploy/corral-web.yaml`), exposed to your
  tailnet by the Tailscale operator. CLI, TUI, and web all share the same state.
- **One static Go binary.** No daemons, no controllers to install, no
  client-side K8s SDK. It drives `kubectl`/`virtctl`/`systemctl` — the tools
  you already trust — and gets out of the way.

## Install

```bash
git clone https://github.com/tuna-os/corral
cd corral
go build -o corral .
install corral ~/.local/bin/
```

Or grab a prebuilt `corral-linux-{amd64,arm64}` from the CI artifacts.

Optional: `corral completion fish | source` (bash/zsh/fish, via Cobra).

Development tasks run through [`just`](https://github.com/casey/just): `just`
lists them — `build`, `test`, `vet`, `ci` (the pre-push gate), and
`regen-catalog` (refresh the Universal Blue / Bluefin / TunaOS bootc catalog
from ghcr, dropping anything not rebuilt in ~60 days).

## Quick start

```bash
# Local VM on this machine (QEMU/KVM, runs as a systemd user service)
corral create scratch --iso ~/Downloads/ubuntu-24.04.iso
corral start scratch            # VNC + SSH bound to this host's Tailscale IP

# Cluster VM (KubeVirt) from a container disk
corral create web --kubevirt --container-disk quay.io/containerdisks/ubuntu:24.04
corral start web
corral ssh web

# Cluster VM from an installer ISO (CDI imports it for you, progress in `corral list`)
corral create bluefin --kubevirt --iso https://download.example/bluefin.iso

# Bootable container → running VM, disk built on-cluster (bootc extension)
corral plugin install bootc
corral bootc create dev --image quay.io/centos-bootc/centos-bootc:stream9
corral start dev && corral ssh dev -u root

# Everything, both backends, one table
corral list

# Container (CT) — distrobox-style persistent rootfs
corral ct create devbox --image docker.io/library/debian:bookworm --privileged
corral ct console devbox
```

## How VMs are reached

| | Local (qemu) | Cluster (kubevirt) |
|---|---|---|
| SSH | host's Tailscale IP, forwarded port | `virtctl ssh` API tunnel — works with zero exposure |
| VNC | host's Tailscale IP, `vnc://…` | `virtctl vnc` proxy |
| Published ports | — (host is already on the tailnet) | per-VM proxy Service tagged `tailscale.com/expose` → `<name>-vm.<tailnet>.ts.net` |
| VM on the tailnet itself | — | automatic via cloud-init when an auth key is configured |

Nothing is ever bound to `0.0.0.0` — local VM ports attach to the host's
Tailscale IP only.

## Extensions & the marketplace

Corral has a krew-style plugin system. Plugins are standalone `corral-<name>`
binaries in `~/.local/share/corral/plugins`; once installed they run as
`corral <name> …`. A curated marketplace (`marketplace/index.json`) lists
installable ones:

```bash
corral plugin search
corral plugin install bootc
corral plugin list
```

The web UI has an **Extensions** tab to browse and install the same plugins.

### The bootc plugin

The flagship extension, `corral bootc`, turns a bootable container image into a
running VM without any local tooling:

1. Corral provisions a block-mode PVC and runs a short-lived **builder VM**
   (not a pod) that runs `bootc install to-disk` onto it — so the VM's own
   kernel does the filesystem work. This is what lets it install images the
   node kernel can't handle, e.g. Universal Blue desktops (bluefin/dakota) that
   need **btrfs + composefs**. The right backend (ostree vs composefs) and
   filesystem are auto-detected from the image; your SSH key is baked in and
   sshd enabled.
2. Build logs stream to your terminal live.
3. The finished disk is self-bootable (GPT + ESP + bootloader), so the final VM
   **UEFI-boots** it — no kernelBoot, no bootloader gymnastics.

`corral bootc rebuild|upgrade|switch` re-bakes the disk from a new image (the
SSH key is re-applied across the `--wipe`). Rebuild your OS in CI, `corral
create` it as a VM in minutes.

**Faster builds:** deploy `deploy/registry-cache.yaml` (an on-cluster
pull-through cache for ghcr.io) and the builder routes image pulls through it
automatically — no config. Disable with `CORRAL_REGISTRY_MIRROR=off`.

### The backup plugin

`corral backup` ships VM disk backups to any S3/R2 bucket via rclone —
on-demand or scheduled entirely in-cluster (no workstation required):

```bash
corral backup create web --dest r2:backups/corral      # export + upload
corral backup restore web-restored --src r2:backups/corral/web-….img.gz --size 20Gi
corral backup list --dest r2:backups/corral

corral backup schedule web --every 24h --keep 7 --to r2:backups/corral  # in-cluster CronJob
corral backup schedules                                                # list schedules
corral backup unschedule web
```

Needs `rclone` configured for your remote (`rclone config`) and `virtctl`.
Scheduled backups fetch `virtctl`/`rclone` inside the CronJob pod at
runtime and mirror your local rclone config into a namespaced Secret — no
bespoke image required.

### The Windows plugin

`corral windows` sets up UEFI/TPM/virtio for a first-class Windows guest —
KubeVirt VMs default to a Linux-tuned devices set that Windows Setup
can't boot from without extra help:

```bash
corral plugin install windows
corral windows create win11 --iso https://example/Win11.iso --cpu 4 --mem 8Gi
```

Imports the installer ISO via CDI, provisions a UEFI+TPM+q35 VM with
Hyper-V enlightenments and the virtio-win driver ISO attached as a second
CD-ROM (so Setup can see the virtio disk/network), and attaches proper
console access.

## Configuration

```yaml
# ~/.config/tailvm/config.yaml
tailscale:
  auth_key: tskey-...   # or export TS_AUTHKEY; flag --ts-authkey overrides
```

`corral config` shows what's active. State lives in
`~/.local/share/tailvm/` (registry + local VM disks) — shared with the
legacy `tailvm` tool, so existing VMs keep working.

Environment overrides (handy for the in-cluster web deployment):

| Variable | Effect |
|---|---|
| `CORRAL_NAMESPACE` | default VM namespace (code default: `corral-vms`) |
| `CORRAL_ADMINS` | comma-separated tailnet logins allowed to mutate; unset = single-user/open (see [Web UI](#web-ui)) |
| `CORRAL_REGISTRY_MIRROR` | override/disable (`off`) the bootc builder's pull-through cache host |
| `CORRAL_BOOTC_BUILD_TIMEOUT` | minutes to wait for a bootc builder VM (default 45) |

## Web UI

```bash
corral web                                  # http://127.0.0.1:8006
corral web --addr "$(tailscale ip -4):8006" # share with your tailnet
```

Or serve it from the cluster (public image built by CI to `ghcr.io/tuna-os/corral`):

```bash
kubectl apply -f deploy/corral-web.yaml
# → https://corral.<tailnet>.ts.net
```

> **Setting up from scratch?** [**Build your own KubeVirt "Proxmox"**](docs/kubevirt-proxmox-setup.md)
> walks through the whole stack — KubeVirt + CDI, the feature gates,
> Longhorn + snapshots, Multus, and deploying Corral.

Tailnet membership *is* the authentication — never bind a public interface.
For **authorization**, set `CORRAL_ADMINS` to a comma-separated list of tailnet
logins: listed users can mutate; everyone else gets a **read-only** UI and
mutating API calls are rejected (403). Unset = single-user/open (the default).
Identity comes from the Tailscale ingress headers — see
[ADR-0003](docs/adr/0003-identity-source.md). Feature roadmap:
[`WEBUI-PLAN.md`](WEBUI-PLAN.md).

## Command reference

```
corral                  TUI (VMs and Containers side by side)
corral web              Proxmox-style web UI [--addr host:port]
corral doctor           cluster health checks, --fix for safe auto-fixes
corral list             all VMs, both backends
corral create <name>    --kubevirt | (default: local qemu)
                        --mem 4G --cpu 2 --disk 20G --iso … --container-disk …
                        --pvc … --node … --cloud-init … --instancetype … --ts-authkey …
                        --storage-class …
corral clone <src> <dst>  [kubevirt] clone a VM's disk + config to a new name
corral plugin           search | install <name> | list | remove <name>   (extensions)
corral start|stop <name>
corral restart <name>   restart a VM
corral pause|unpause    [kubevirt] freeze / resume a running VM
corral scale <name>     [kubevirt] --cpu N --mem 8G (live hotplug when possible)
corral migrate <name>   [kubevirt] --node X  live-migrate to another node
corral adddisk <name>   [kubevirt] --size 10Gi  hotplug a new disk
corral rmdisk <name>    [kubevirt] --volume PVC  detach a hotplugged disk
corral snapshot …       [kubevirt] create | ls | restore | rm
corral ssh <name>       [-u user] [-i key] [-c cmd] [-p port] [--password …]
corral viewer <name>    VNC via xdg-open
corral logs <name>      journald (local) / virt-launcher (cluster)
corral info <name>      raw JSON
corral delete <name>    [-f] removes VM, disks, proxy, registry entry

corral ct create <name>  --image … [--cpu 1] [--mem 512Mi] [--disk 5Gi]
                          [--privileged]  (distrobox-style persistent rootfs)
corral ct list|start|stop|delete|console <name>
```

### KubeVirt feature support & cluster requirements

Corral exposes the Proxmox-style operations above through both the CLI/TUI and
the web UI (editable Hardware tab, Snapshots tab, in-browser consoles). What
actually works depends on the cluster:

- **Change CPU / RAM** — always works. On a genuinely live-migratable VM it is
  hotplugged with no downtime; otherwise Corral applies it in a single
  stop→patch→start. New VMs are created sockets-based with `maxSockets` /
  `maxGuest` headroom so they *can* hotplug.
- **Live migration / live hotplug** — needs `vmRolloutStrategy: LiveUpdate`,
  masquerade networking (Corral sets this), migratable storage (RWX), **and a
  target node with the same CPU vendor**. You cannot live-migrate a running VM
  between an Intel and an AMD host, so on a mixed-vendor cluster Corral detects
  this and falls back to the offline path instead of hanging.
- **Add disk (hotplug)** — needs the `HotplugVolumes` feature gate.
- **Online disk expansion** — needs a StorageClass with
  `allowVolumeExpansion: true`.
- **Snapshots / clone / restore** — need a `VolumeSnapshotClass` for VMs with
  persistent disks (ephemeral container-disk VMs can snapshot their definition
  without one). The web UI greys out controls the cluster can't support.

Full design document: [SPEC.md](SPEC.md).

## Documentation

- **[SPEC.md](SPEC.md)** — full specification (commands, flags, types, backends, registry)
- **[WEBUI-PLAN.md](WEBUI-PLAN.md)** — web UI architecture, Proxmox feature map, constraints
- **[docs/api.md](docs/api.md)** — complete REST API reference
- **[docs/architecture.md](docs/architecture.md)** — package map, design decisions, data flow, build system
- **[docs/kubevirt-proxmox-setup.md](docs/kubevirt-proxmox-setup.md)** — from-scratch KubeVirt + Longhorn + Corral setup guide
- **[docs/testing.md](docs/testing.md)** — testing strategy & plan (unit, integration, E2E)

## Requirements

- Local backend: `qemu-system-x86_64` + KVM, systemd user session
- Cluster backend: `kubectl` context with KubeVirt (+ CDI for ISO import),
  `virtctl`; Tailscale operator for published ports
- `tailscale` on the host; `sshpass` only if you use password SSH

## Why "Corral"?

A corral is one fence that holds the whole herd. Your VMs are the cattle;
your tailnet is the fence. *(Formerly known as `tailvm`.)*
