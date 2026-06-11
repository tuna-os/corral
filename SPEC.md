# Corral — Specification

**Version:** 1.0 (2026-06-10)
**Binary:** `corral`
**Module:** `github.com/hanthor/corral`

> **Naming:** Corral is the Go rewrite of the legacy Python `tailvm`. The
> command is `corral`; on-disk and in-cluster identifiers deliberately keep
> the `tailvm` prefix (`~/.local/share/tailvm/`, `~/.config/tailvm/`,
> `tailvm-<name>.service` units, the `tailvm` namespace and labels) so both
> tools see the same VMs and nothing breaks during the transition.

## 1. Purpose

`corral` is a single-binary CLI + TUI that manages virtual machines across two
backends and exposes them over a Tailscale tailnet:

| Backend | Where the VM runs | How it's reached |
|---|---|---|
| **qemu** | Locally, as a systemd user service | VNC + SSH bound to the host's Tailscale IP |
| **kubevirt** | On the Talos K8s cluster (bihar/karnataka) | `virtctl` tunnels, or a proxy Service annotated `tailscale.com/expose` |

A third creation mode, **bootc**, builds a bootable-container disk on-cluster
and runs it as a KubeVirt VM.

Design constraints:

- **No client-side K8s SDK.** All cluster interaction shells out to `kubectl`
  and `virtctl`, so the binary stays small and respects the local kubeconfig.
- **Backend transparency.** After `create`, every command (`start`, `ssh`,
  `viewer`, ...) auto-detects the backend from the registry, falling back to
  live probing.
- **Secrets stay local.** Cloud-init passwords live in the registry file
  (mode 0600); the Tailscale auth key comes from config/env (seeded from
  Bitwarden by the dotfiles Ansible role).

## 2. Command reference

```
corral                      # no args → interactive TUI
corral list                 # unified table, both backends
corral create <name> [flags]
corral start  <name>
corral stop   <name>
corral restart <name>                  # kubevirt: virtctl restart; qemu: stop+start
corral pause|unpause <name>            # kubevirt only
corral scale  <name> [--cpu N] [--mem 8G]   # kubevirt: live hotplug or offline reboot
corral migrate <name> [--node X]       # kubevirt: live-migrate (needs same-vendor target)
corral adddisk <name> [--size 10Gi]    # kubevirt: hotplug a new disk
corral rmdisk  <name> --volume <pvc>   # kubevirt: detach a hotplugged disk
corral snapshot create|ls|restore|rm <name> [snap]   # kubevirt
corral delete <name> [-f|--force]      # confirms unless --force
corral info   <name>                   # JSON (VM manifest / metadata.json)
corral viewer <name>                   # VNC viewer via xdg-open
corral ssh    <name> [-u user] [-i key] [-c cmd] [-p port] [--password p]
corral logs   <name>                   # journalctl (qemu) / virt-launcher logs (kubevirt)
corral config                          # show config + Tailscale auth key status
corral web [--addr host:port]          # Proxmox-style web UI (default 127.0.0.1:8006)
corral completion <shell>              # cobra built-in
```

### 2.0 KubeVirt operations & cluster requirements

The scale/migrate/snapshot/hotplug operations (CLI, TUI, and web UI) depend on
cluster capabilities. Corral detects them and gates the UI rather than failing:

- **CPU/RAM change** — always works; live-hotplugged when the VM is genuinely
  live-migratable (`vmRolloutStrategy: LiveUpdate`, masquerade net, migratable
  storage, **and a same-CPU-vendor target node**), else a single offline
  stop→patch→start. New VMs get `cpu.maxSockets` + `memory.maxGuest` headroom.
- **Live migration** — impossible between different CPU vendors (e.g. Intel↔AMD);
  Corral returns a clear error instead of leaving a migration stuck in Scheduling.
- **Add disk** — needs the `HotplugVolumes` feature gate.
- **Online expand** — needs a StorageClass with `allowVolumeExpansion: true`.
- **Snapshots/clone** of persistent-disk VMs — need a `VolumeSnapshotClass`.

### 2.1 `create` flags

| Flag | Backend | Meaning |
|---|---|---|
| `--kubevirt, -k` | — | select KubeVirt backend (default: qemu) |
| `--bootc <image>` | kubevirt | build disk from a bootable container image (implies KubeVirt) |
| `--mem 4G` | both | memory |
| `--cpu 2` | both | cores |
| `--disk 20G` | both | disk size (bootc default: 50Gi) |
| `--iso <path/url>` | both | qemu: local ISO; kubevirt: CDI imports from URL |
| `--qcow <path>` | qemu | template disk |
| `--container-disk <image>` | kubevirt | ephemeral container disk |
| `--pvc <name>` | kubevirt | boot an existing PVC |
| `--namespace, -n` | kubevirt | namespace (default `tailvm`) |
| `--node <host>` | kubevirt | pin to node via `kubernetes.io/hostname` |
| `--cloud-init-password` | kubevirt | password (default: random 12-char, stored in registry) |
| `--cloud-init` | kubevirt | extra cloud-init user-data appended verbatim |
| `--ts-authkey` | kubevirt | Tailscale auth key (default: config file / `TS_AUTHKEY`) |
| `--force` | both | overwrite existing VM |

Name uniqueness is enforced across **both** backends before creation.

### 2.2 SSH resolution order

1. User: `-u` flag → `$USER` → `root`.
2. Password: `--password` flag → registry `Password` (saved at create time) →
   key-based only.
3. KubeVirt: `virtctl ssh vm/<name>` (API-server tunnel; works without proxy).
   QEMU: `ssh -p <forwarded-port> user@<host tailscale IP>`.
4. Password auth shells through `sshpass` when a password is in play.

## 3. Architecture

```
main.go                      thin entry → cmd.Execute()
cmd/
  root.go        cobra root; PersistentPreRunE inits the registry;
                 bare invocation runs the TUI; postQuitAction hook lets the
                 TUI hand off SSH/viewer sessions to the real terminal
  create.go      create command; dispatches qemu/kubevirt/bootc paths;
                 applies manifests via `kubectl apply -f -`
  commands.go    start/stop/delete/info/viewer/ssh/logs; backend dispatch
  list.go        unified Lip Gloss table; resolveBackend/requireBackend
  config.go      `corral config` introspection
  tui.go         Bubble Tea TUI (see §7)
pkg/
  types/         shared VM/CreateOpts/RegistryEntry structs, port maps
  registry/      ~/.local/share/tailvm/registry.json persistence (0600)
  config/        ~/.config/tailvm/config.yaml + TS_AUTHKEY
  qemu/          local VMs: qcow2 disks, systemd user units, metadata.json
  kubevirt/
    client.go    kubectl/virtctl wrapper; manifest generators (VM, PVC,
                 DataVolume, proxy Service/Deployment/RBAC)
    bootc.go     on-cluster bootc disk build (PVC + privileged Job)
```

**Backend resolution** (`resolveBackend`): registry hit → live KubeVirt list
(all namespaces) → QEMU directory probe → "" (unknown). The registry also
stores the namespace, so cross-namespace VMs keep working
(`resolveNamespace`).

## 4. State files

### 4.1 Registry — `~/.local/share/tailvm/registry.json` (0600)

```json
{
  "myvm": {
    "backend": "kubevirt",
    "namespace": "tailvm",
    "password": "abc123def456",
    "extra": { "bootc_image": "ghcr.io/..." }
  }
}
```

Written on create, removed on delete (CLI and TUI). Source of truth for
backend/namespace/password; live probing is the fallback so out-of-band VMs
are still usable.

### 4.2 QEMU metadata — `~/.local/share/tailvm/vms/<name>/metadata.json`

```json
{
  "name": "myvm", "cpu": 2, "memory": "4G", "disk_size": "20G",
  "vnc_port": 5917, "vnc_display": 17, "ssh_port": 2217,
  "tailscale_ip": "100.x.y.z", "iso": "...", "has_iso": true
}
```

`vnc_display` is `hash(name) % 100` (stable across recreates); `vnc_port` =
5900 + display, `ssh_port` = 2200 + display. Disk lives next to it as
`disk.qcow2`.

### 4.3 Config — `~/.config/tailvm/config.yaml`

```yaml
tailscale:
  auth_key: tskey-...
```

`TS_AUTHKEY` env var takes precedence. Seeded from Bitwarden item
`tailscale-apikey` by the dotfiles playbook.

## 5. QEMU backend

- `create` makes a qcow2 disk, then writes a **systemd user unit**
  `~/.config/systemd/user/tailvm-<name>.service` running
  `qemu-system-x86_64 -machine q35,accel=kvm` with virtio disk/net/rng.
- Networking is **user-mode** (`-netdev user`) with:
  - VNC bound to the host's Tailscale IP at `:<display>` — reachable
    tailnet-wide, invisible on the LAN.
  - `hostfwd=tcp:<tailscale-ip>:<ssh_port>-:22` so `corral ssh` reaches the
    guest's sshd through the host's Tailscale IP.
- `start/stop` = `systemctl --user start/stop`; `logs` = `journalctl --user -f`;
  `delete` stops, disables, removes unit + VM dir.
- Guests do not join the tailnet themselves (no cloud-init wiring on this
  backend); they are reachable *through* the host.

## 6. KubeVirt backend

### 6.1 VM creation

Namespace `tailvm` is created on demand and labeled
`pod-security.kubernetes.io/enforce=privileged` (the bootc builder needs it).
Boot source priority: `--iso` (CDI DataVolume CD-ROM, bootOrder 1 + blank PVC,
bootOrder 2) → `--container-disk` (+ optional data PVC) → `--pvc` → blank PVC.

Cloud-init always includes: a password (random unless given, persisted in the
registry), `ssh_pwauth: true`, the operator's public key from `~/.ssh/`
(`id_ed25519.pub` → `id_rsa.pub` → `id_ecdsa.pub`), and — when an auth key is
configured — a `runcmd` that installs Tailscale and runs
`tailscale up --auth-key=... --hostname=<name> --ssh`, so the VM appears on
the tailnet as a first-class node. The injected `runcmd` is skipped if
`--cloud-init` already supplies one (two `runcmd` keys would be invalid YAML).

### 6.2 Port proxy (tailnet exposure)

For VMs that should be reachable without `virtctl`, the TUI's "Edit ports"
action manages a per-VM proxy:

- **Service** `<name>-proxy`, annotated `tailscale.com/expose: true`,
  `tailscale.com/hostname: <name>-vm` → the Tailscale operator publishes
  `<name>-vm.<tailnet>.ts.net`.
- **Deployment** `<name>-proxy` (alpine):
  - port 5900 → `virtctl vnc --proxy-only` (virtctl fetched by an init
    container, only when VNC is enabled),
  - all other ports → `socat` to the VMI's pod IP, resolved by polling the
    KubeVirt API with the pod's ServiceAccount token and parsed with `jq`.
- **RBAC**: per-VM ServiceAccount + Role (VMI get, vnc get, portforward
  create) + RoleBinding.

Deleting the last port (or the VM) removes all proxy resources.

### 6.3 Lifecycle

`start`/`stop` → `virtctl`; `list` → `kubectl get vms -A -o json`, including
ISO-import progress from the DataVolume phase; `delete` → stop, delete VM,
PVCs/DataVolumes (`-disk`, `-data`, `-iso`, `-bootc-disk`), and the proxy
stack; `logs` → virt-launcher `compute` container logs by
`vm.kubevirt.io/name` label.

## 7. bootc pipeline (`create --bootc <image>`)

Builds a bootable-container OS disk **on the cluster** (no local
disk-image tooling needed):

1. Create PVC `<name>-bootc-disk` (default 50Gi).
2. Run a privileged **Job** whose builder container *is the bootc image*:
   - detects `KERNEL_VERSION` from `/usr/lib/modules`,
   - `truncate` a raw file on the PVC, loop-mount, `mkfs.xfs`,
   - `bootc install to-filesystem --source-imgref=docker://<image>
     --root-ssh-authorized-keys=... --generic-image --bootloader none
     --karg=root=UUID=<uuid>`,
   - enables `sshd.service` in the resulting ostree deployment
     (`systemctl --root=<deploy>`),
   - echoes `ROOT_UUID=` / `KERNEL_VERSION=` markers.
3. The CLI streams the Job logs live, waits for completion (15 min cap),
   parses the markers, and deletes the Job.
4. Creates a VM that **kernel-boots** (`spec.domain.firmware.kernelBoot`)
   pulling `vmlinuz`/`initramfs.img` for the detected kernel version straight
   from the container image, with `root=UUID=<uuid> ro console=ttyS0`.

Kernel boot exists because the Talos node kernel (6.18.x) cannot re-read
loopback partition tables, so a GRUB/ESP layout can't be produced in-cluster;
`--bootloader none` + KubeVirt kernelBoot sidesteps the bootloader entirely.

Constraints: requires a local `~/.ssh/*.pub` (only login path — no
cloud-init in bootc images); no tailnet auto-join (use the proxy, or bake
Tailscale into the image); the image must keep kernel+initramfs under
`/usr/lib/modules/<ver>/`.

## 8. TUI

State machine: `list` → (enter) → `actions` → `edit` (ports) /
`confirmDelete` / immediate actions.

- **list**: all VMs, both backends; `enter` select, `q` quit.
- **actions**: ▶ Start, ■ Stop, 🔑 SSH, 🖵 Viewer, ✎ Edit ports, ✕ Delete.
  SSH/Viewer set `postQuitAction` and quit Bubble Tea first so the session
  owns the terminal.
- **edit**: toggle the default ports (22/80/443/3389/5900) or add custom
  ones; each toggle re-applies the proxy manifests immediately.
- **confirmDelete**: `y` deletes (VM + disks + proxy + registry entry); any
  other key cancels.

## 9. Web UI (`corral web`)

A Proxmox-style dashboard for the KubeVirt backend, served from the binary
(`pkg/web`, static SPA via `//go:embed` — no JS toolchain). REST API plus two
websocket bridges: `/api/vnc/{ns}/{name}` ↔ `virtctl vnc --proxy-only`
(noVNC in the browser) and `/api/tty/{ns}/{name}` ↔ `virtctl console`
(xterm.js serial TTY). Actions: create (container-disk/ISO/PVC), start, stop,
restart, pause/unpause, live-migrate, delete. Responsive — usable on mobile,
consoles included.

Runs locally (`corral web`, default `127.0.0.1:8006`) or **on the cluster**:
`Containerfile` builds `ghcr.io/hanthor/corral` (alpine + corral +
kubectl + virtctl, published by the repo CI), deployed by
`talos-k8s/corral-web.yaml` with a scoped ClusterRole and a Service exposed
to the tailnet by the Tailscale operator (`corral.<tailnet>.ts.net`). The web
UI shares the registry and cluster state with the CLI/TUI — all three work in
tandem. Roadmap: `WEBUI-PLAN.md`.

## 10. Security model

- Registry contains cloud-init passwords → file mode 0600.
- Cloud-init user-data (incl. password and TS auth key) is embedded in the VM
  manifest: anyone with read access to the namespace can see it. Acceptable
  for a single-operator homelab; use ephemeral/pre-authorized keys.
- The bootc builder Job runs **privileged** (loop devices, mkfs) — hence the
  namespace PodSecurity label. Builder containers are deleted after the build.
- SSH defaults to `StrictHostKeyChecking=no` (VMs are disposable); identity
  flag available when stricter behavior is wanted.
- QEMU services bind only to the Tailscale IP, never 0.0.0.0.

## 11. External dependencies

| Tool | Needed for | Failure mode |
|---|---|---|
| `kubectl` | all KubeVirt ops | command errors surface |
| `virtctl` | start/stop/ssh/vnc | friendly install hint |
| `qemu-system-x86_64`, `qemu-img` | qemu backend | install hint |
| `systemctl --user`, `journalctl` | qemu lifecycle/logs | — |
| `tailscale` | host IP discovery | falls back to 127.0.0.1 (degraded) |
| `sshpass` | password SSH | install hint |
| `xdg-open` / flatpak virt-viewer | viewer | prints URL instead |

## 12. Known limitations & roadmap

- **QEMU**: no cloud-init/ignition injection; no `--qcow` template copy
  implemented in the unit path beyond disk creation; ISO must be a local file.
- **bootc**: no progress bar (logs stream raw); image must be public or
  pullable by the cluster; 15-minute build cap.
- **Proxy**: socat targets the VMI IP at proxy start; a VM restart that
  changes the pod IP needs a proxy pod restart.
- **Roadmap**: Bubble Tea spinner/progress for bootc builds; `corral create`
  from the TUI; QEMU ISO download cache (`vms/cache/`); ignition support for
  bootc VMs; `virtctl`-less mode using the K8s API directly.
