# 🤠 Corral

**Herd your VMs into your tailnet.**

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

- **Same five commands everywhere.** `create` / `start` / `ssh` / `viewer` /
  `delete` work identically whether the VM is local QEMU/KVM or KubeVirt on
  your cluster. Corral remembers which is which — you never specify it again.
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
  start/stop/restart/pause/migrate, and *real consoles in the browser* —
  noVNC graphics and an xterm.js serial TTY. It also runs **on the cluster
  itself** (`talos-k8s/corral-web.yaml`), exposed to your tailnet by the
  Tailscale operator. CLI, TUI, and web all share the same state.
- **One static Go binary.** No daemons, no controllers to install, no
  client-side K8s SDK. It drives `kubectl`/`virtctl`/`systemctl` — the tools
  you already trust — and gets out of the way.

## Install

```bash
git clone https://github.com/hanthor/corral
cd corral
go build -o corral .
install corral ~/.local/bin/
```

Or grab a prebuilt `corral-linux-{amd64,arm64}` from the CI artifacts.

Optional: `corral completion fish | source` (bash/zsh/fish, via Cobra).

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

1. Corral creates a PVC and runs a privileged Job **using your image as the
   builder** — `bootc install to-filesystem` onto an XFS loopback disk, your
   SSH key baked in, sshd enabled.
2. Build logs stream to your terminal live.
3. The VM kernel-boots directly from the image's own kernel/initramfs
   (auto-detected), so no bootloader gymnastics are needed.

Rebuild your OS in CI, `corral create` it as a VM in minutes.

## Configuration

```yaml
# ~/.config/tailvm/config.yaml
tailscale:
  auth_key: tskey-...   # or export TS_AUTHKEY; flag --ts-authkey overrides
```

`corral config` shows what's active. State lives in
`~/.local/share/tailvm/` (registry + local VM disks) — shared with the
legacy `tailvm` tool, so existing VMs keep working.

## Web UI

```bash
corral web                                  # http://127.0.0.1:8006
corral web --addr "$(tailscale ip -4):8006" # share with your tailnet
```

Or serve it from the cluster (public image built by CI to `ghcr.io/hanthor/corral`):

```bash
kubectl apply -f deploy/corral-web.yaml
# → https://corral.<tailnet>.ts.net
```

> **Setting up from scratch?** [**Build your own KubeVirt "Proxmox"**](docs/kubevirt-proxmox-setup.md)
> walks through the whole stack — KubeVirt + CDI, the feature gates,
> Longhorn + snapshots, Multus, and deploying Corral.

No auth is built in — tailnet membership *is* the auth. Never bind a public
interface. Feature roadmap: [`WEBUI-PLAN.md`](WEBUI-PLAN.md).

## Command reference

```
corral                  TUI
corral web              Proxmox-style web UI [--addr host:port]
corral list             all VMs, both backends
corral create <name>    --kubevirt | (default: local qemu)
                        --mem 4G --cpu 2 --disk 20G --iso … --container-disk …
                        --pvc … --node … --cloud-init … --instancetype … --ts-authkey …
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

## Requirements

- Local backend: `qemu-system-x86_64` + KVM, systemd user session
- Cluster backend: `kubectl` context with KubeVirt (+ CDI for ISO import),
  `virtctl`; Tailscale operator for published ports
- `tailscale` on the host; `sshpass` only if you use password SSH

## Why "Corral"?

A corral is one fence that holds the whole herd. Your VMs are the cattle;
your tailnet is the fence. *(Formerly known as `tailvm`.)*
