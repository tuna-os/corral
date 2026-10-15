# libvirt backend setup

> Related: [Incus setup](incus-backend-setup.md),
> [backend support matrix](backend-support.md),
> [problems and fixes](troubleshooting.md), [user guide](user-guide.md)

Corral uses the `virsh` and `virt-install` commands to manage the domains
of libvirt. It does not link to the libvirt library. Each libvirt context is a
libvirt connection URI, for example:

| URI | Meaning |
|---|---|
| `qemu:///system` | The system daemon on this host. This is the default |
| `qemu:///session` | The daemon of your user session on this host |
| `qemu+ssh://user@hypervisor/system` | The system daemon on another host, through SSH |

Corral gives the URI to `virsh -c URI` without a change. If
`virsh -c URI list --all` works for your user, Corral can use that URI.

## What you get

| Capability | libvirt support in Corral |
|---|---|
| Inventory in `corral list`, the TUI and the web UI | yes, including domains that Corral did not create |
| Start, stop, delete | yes (CLI, TUI, web). Stop sends an ACPI shutdown |
| Restart | yes (CLI, TUI, web), with `virsh reboot` |
| Create | yes, from an ISO or a qcow2 disk, through `virt-install` |
| Console | web VNC (also through SSH), `corral viewer` and the TUI run `virt-viewer` |
| SSH | no, `corral ssh` refuses libvirt domains. Use `ssh` to the guest address |
| Snapshots | TUI only today, see [backend support](backend-support.md#incus-and-libvirt-snapshots-migration-and-hotplug) |
| Cold move to or from another backend | yes, `corral move`. As a source, only from a local URI |
| Live migration, CPU/RAM change, disk or NIC hotplug | no, not yet |

## 1. Prerequisites

- libvirt and QEMU/KVM on the hypervisor host.
- These commands on the host where you run Corral:
  - `virsh` (package `libvirt-client` or `libvirt-clients`). Corral needs it
    for all operations.
  - `virt-install` (package `virt-install` or `virtinst`). Corral needs it
    for `corral create`.
  - `virt-viewer`. This is optional. The TUI console uses it.
  - `qemu-img` and OVMF (`edk2-ovmf` or `ovmf`). Corral needs these to move a
    VM onto libvirt or off libvirt.
- On Debian and Ubuntu, this installs the local parts:

  ```bash
  sudo apt-get install libvirt-daemon-system libvirt-clients virtinst ovmf
  ```

- For `qemu:///system`, membership of the `libvirt` group. Add yourself, then
  log out and log in again:

  ```bash
  sudo usermod -aG libvirt "$USER"
  ```

  Do not run Corral with `sudo` to get around a permission error.

### Remote hypervisors (`qemu+ssh://`)

Corral has no SSH settings of its own. `virsh` opens the SSH connection, so
your OpenSSH configuration, keys and agent apply. Before you add Corral, make
sure that this command works without a password prompt:

```bash
virsh -c qemu+ssh://user@hypervisor/system list --all
```

- Use key authentication. Load the key into `ssh-agent`, or set it in
  `~/.ssh/config`.
- Run `ssh user@hypervisor` one time to accept the key of the host.
- The remote user must be able to open `qemu:///system` on the hypervisor.
  Usually this means membership of the `libvirt` group on that host.
- For the web VNC console, Corral runs `ssh -T -W` to the hypervisor. This
  command does not read a port from the URI. If SSH on the hypervisor does
  not listen on port 22, set the port in `~/.ssh/config`.

## 2. Make sure that libvirt works without Corral

```bash
virsh -c qemu:///system connect
virsh -c qemu:///system nodeinfo
virsh -c qemu:///system pool-list --all
virsh -c qemu:///system net-list --all
```

Corral does not give a storage pool or a network to `virt-install`. Those
defaults come from `virt-install` and libvirt. Usually a host needs an active
`default` pool and an active `default` network. A domain that Corral makes
when it moves a VM onto libvirt always uses the `default` network.

## 3. Add a Corral context

Scan this host and save what works:

```bash
corral setup
```

`corral setup` tries `qemu:///system` and the URIs of your libvirt contexts.
If one answers, setup offers to save a context with the name `libvirt`.

Add a context by hand, for a local or a remote URI:

```bash
corral context add hv1 --backend libvirt --context qemu+ssh://admin@hv1.example.net/system
corral context list
```

To make libvirt the destination of `corral create` commands that do not name
a backend:

```bash
corral config set-default-backend libvirt
```

### How Corral selects the URI

For commands that do not resolve an existing domain, Corral selects the URI
in this order:

1. The global `--context` flag. A context name selects the URI of that
   context. With `--backend libvirt`, any other value is the URI.
2. The `CORRAL_LIBVIRT_URI` environment variable.
3. The URI of the default context, when `corral context use NAME` selected a
   libvirt context.
4. The `libvirt.uri` key in `~/.config/corral/config.yaml`.
5. `qemu:///system`.

Commands that act on an existing domain find the URI from the inventory.
For example, `corral stop db` goes to the URI where `db` is.

## 4. Verify

```bash
corral doctor --context hv1
corral list
```

In `corral list`, the CONTEXT and NODE columns of a libvirt domain show its
URI. `corral doctor` runs these checks for each libvirt context:

| Check | What it does | If it fails |
|---|---|---|
| `virsh CLI` | Looks for `virsh` on `PATH` | Install the libvirt client |
| `libvirt reachable` | Runs `virsh -c URI connect` | Read the error text. Look at the URI, the SSH transport, the daemon and your group membership |
| `libvirt hypervisor` | Runs `virsh nodeinfo` | Look at the permissions on the hypervisor |
| `libvirt storage` | Runs `virsh pool-list --all` | Define or start a storage pool |
| `libvirt networking` | Runs `virsh net-list --all` | Define or start a virtual network |
| `libvirt console viewer` | Looks for `virt-viewer` (warning only) | Install `virt-viewer` for the TUI console. The web VNC console works without it |

`corral doctor --fix` does not change libvirt. All libvirt fixes are manual.
To check a URI that you did not save as a context, use:

```bash
corral doctor --backend libvirt --context qemu+ssh://admin@hv2.example.net/system
```

## 5. Create and use domains

`corral create` on libvirt needs `--iso` or `--qcow`:

```bash
corral create db --context hv1 --iso /var/lib/libvirt/images/debian-12.iso --cpu 2 --mem 4G --disk 40G
corral create app --context hv1 --qcow /var/lib/libvirt/images/app.qcow2
```

- With `--iso`, Corral runs `virt-install --cdrom` and makes a new disk.
  `--disk` sets its size in GiB (default 20).
- With `--qcow`, Corral runs `virt-install --import` on that disk.
- The paths go to `virt-install` without a change. For a remote URI, the file
  must be at that path on the hypervisor.
- Give `--mem` in `G` or `M`. Corral changes `G` to MiB. It sends other
  suffixes without a change.
- Corral does not set the firmware, the network or the OS variant. Corral
  ignores `--firmware`, `--tpm`, `--start` and `--wait-ssh` on libvirt.

Lifecycle commands are the same as on the other backends:

```bash
corral start db
corral stop db
corral delete db
```

`corral delete` runs `virsh destroy` and then
`virsh undefine --remove-all-storage --nvram`. This removes the disks of the
domain.

To restart a domain, use the TUI or the web UI. The CLI `corral restart`
command does not support libvirt today.

### Consoles and SSH

- **Web UI.** The console tab reads `virsh domdisplay` and connects to the
  VNC display. For a `qemu+ssh://` URI with a display on the loopback address
  of the hypervisor, Corral opens the connection through SSH.
- **TUI.** The viewer action runs `virt-viewer --connect URI NAME`.
- **CLI.** `corral viewer NAME` runs `virt-viewer --connect URI NAME` with
  the URI of the domain. `corral logs` refuses libvirt domains: libvirt keeps
  no guest log that Corral can read. Use `virsh -c URI console NAME`.
- **SSH.** `corral ssh` stops with
  `libvirt domain "NAME" has no discovered SSH address`. Corral does not
  find guest addresses on libvirt. Get the address with
  `virsh -c URI domifaddr NAME` and use `ssh` directly.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `corral setup` shows `virsh is not installed` | Install the libvirt client package, then run `corral setup` again |
| `libvirt daemon at qemu:///system refused permission` | Add yourself to the `libvirt` group and log in again |
| `virsh is installed but no libvirt daemon answered` | Start `libvirtd` (or the `virtqemud` socket), and look at the URI |
| `libvirt reachable` fails for a `qemu+ssh://` URI | Run the same `virsh -c URI list --all` command yourself. Fix the SSH keys, host key or remote group first |
| `libvirt create needs --iso or --qcow` | Give one of the two sources. libvirt has no image catalog in Corral |
| `virt-install: ...` | `virt-install` refused the create. The text after the colon comes from `virt-install`, for example a missing pool or network |
| `libvirt domain "NAME" has no discovered SSH address` | This is expected. Use `ssh` to the guest address |
| `domain "NAME" does not advertise a VNC display` | The domain has no VNC graphics device. Add one with `virsh edit` |
| `snapshot is only supported for KubeVirt VMs` | The `corral snapshot` CLI is KubeVirt-only today. Use the TUI Snapshots view ([#491](https://github.com/tuna-os/corral/issues/491)) |
| `corral move` shows `... is a remote hypervisor and its disks are not on this filesystem` | Corral can only export disks from a local URI. Run Corral on the hypervisor, or use shared storage |

For more symptoms, see [problems and fixes](troubleshooting.md).
