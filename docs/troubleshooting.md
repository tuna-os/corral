# Problems and fixes

> Related: [user guide](user-guide.md), [backend support](backend-support.md),
> [Incus setup](incus-backend-setup.md), [libvirt setup](libvirt-backend-setup.md),
> [KubeVirt and Proxmox setup](kubevirt-proxmox-setup.md)

This guide starts from what you see: an error message or a symptom. Each
entry gives the cause and the fix. The error texts are the exact texts that
Corral prints. Search this page for the text of your error.

## Start with `corral doctor`

`corral doctor` checks every context that you configured, and every peer.
It shows one section for each context:

```text
local [qemu]
  ✓ QEMU (local backend)              qemu-system-x86_64 found (probe 3ms)
  ! KVM acceleration                  no /dev/kvm access — local VMs fall back to slow emulation (...)
  ✗ qemu-img                          not installed — local VM disk creation needs it (...)
```

| Mark | Meaning |
|---|---|
| `✓` | The check passed |
| `!` | A warning. The feature that the check names does not work, but Corral can still operate |
| `✗` | A required check failed. `corral doctor` exits with status 1 |
| `(fixable)` | `corral doctor --fix` can repair this item |

Useful forms of the command:

```bash
corral doctor                     # every context and every peer
corral doctor --context NAME      # one context only
corral doctor --fix               # repair the fixable KubeVirt items, then check again
corral doctor --suggest           # scan this host for backends; changes nothing, exits 0
corral setup                      # scan, then save a context for each backend that works
```

`--fix` changes only the default Kubernetes context. It installs KubeVirt,
CDI and metrics-server when they are missing, and sets the KubeVirt feature
gates and the update strategy. It does not change the local host, Incus,
libvirt or Proxmox.

When a context fails, the other contexts still show. `corral list` behaves
the same way, and prints `warning: context NAME unavailable: ...` for each
context that it cannot reach.

### Local host checks (`local [qemu]`)

| Check | If it fails |
|---|---|
| `QEMU (local backend)` | Install the `qemu-system-x86` package. Needed only for local VMs |
| `KVM acceleration` | Add yourself to the `kvm` group and log in again, or enable virtualization in the firmware. Without KVM, local VMs use slow software emulation |
| `Nested virtualization` | Needed only when a VM runs its own VMs. The check text gives the `modprobe` command |
| `Tailscale CLI` | Install Tailscale. Local VMs bind VNC and SSH to the Tailscale address of the host |
| `virtctl CLI` | Needed only for KubeVirt consoles and SSH |
| `VSOCK host support` | Needed only for `--vsock`. Load `vhost_vsock`, and install a `socat` with VSOCK support and `ssh-keygen` |
| `swtpm (TPM emulation)` | Needed only for `--tpm`. Install `swtpm` |
| `OVMF firmware` | Install `edk2-ovmf` or `ovmf`. UEFI guests do not boot without it |
| `socat` | Needed for the VNC bridge, `corral screenshot` and `--vsock`. Install `socat` |
| `qemu-img` | Install `qemu-img` or `qemu-utils`. Local VM creation needs it |

`VSOCK host support`, `swtpm`, `OVMF firmware`, `socat` and `qemu-img` are
required checks. If one of them fails, `corral doctor` exits with status 1,
also when you do not use that feature.

### KubeVirt checks

| Check | If it fails |
|---|---|
| `Cluster reachable` | The Kubernetes API did not answer. Look at your kubeconfig, the context, your VPN and your credentials |
| `KubeVirt installed`, `CDI installed`, `metrics-server` | Run `corral doctor --fix`, or see the [setup guide](kubevirt-proxmox-setup.md) |
| `LiveUpdate rollout strategy`, `LiveMigrate workload updates`, `Feature gate: ...` | Run `corral doctor --fix`. If an operator such as HCO manages the KubeVirt CR, set the values in the HyperConverged CR |
| `Default StorageClass`, `Expandable StorageClass`, `VolumeSnapshotClass` | Install a CSI driver with those features. See the [setup guide](kubevirt-proxmox-setup.md#4-storage-snapshots--expansion) |
| `Migratable storage (RWX)` | VMs on a storage class without ReadWriteMany cannot live-migrate |
| `GPU/PCI passthrough` | Enable IOMMU and bind the device to `vfio-pci` on its node |

### Incus, libvirt and Proxmox checks

- Incus: see [Incus setup](incus-backend-setup.md#4-verify).
- libvirt: see [libvirt setup](libvirt-backend-setup.md#4-verify).
- Proxmox: `API token` fails when the context has no token. Set
  `CORRAL_PROXMOX_TOKEN`, or add `proxmox.token` to the context.
- Proxmox: `API token accepted` fails when the cluster refuses the token.
  The role of the token needs `VM.Audit` and `VM.PowerMgmt`.
- Proxmox: `TLS trust` fails when your system does not trust the server
  certificate. Pin it with `proxmox.fingerprint`.

## Collect evidence

Corral writes structured logs to stderr. `--log-level debug` logs each
command that Corral runs (`kubectl`, `virtctl`, `incus`, `virsh` and the
others) with its duration and exit code. Corral replaces tokens,
passwords, Tailscale auth keys and Proxmox API tokens with `[REDACTED]`
before it writes the record. It logs the size of the input of a command,
not the input. The input can hold manifests and cloud-init passwords.

```bash
corral list --log-level debug
corral list --log-level debug --log-format json 2>corral.log
```

| Flag | Variable | Values |
|---|---|---|
| `--log-level` | `CORRAL_LOG_LEVEL` | `debug`, `info` (default), `warn`, `error` |
| `--log-format` | `CORRAL_LOG_FORMAT` | `text` (default), `json` |
| `--log-file` | `CORRAL_LOG_FILE` | A file that gets the log instead of stderr. Corral creates it with mode `0600` |

The TUI draws on the terminal, so it always writes its log to a file. The
default file is `~/.cache/corral/tui.log` (`$XDG_CACHE_HOME/corral` when
that variable is set).

`corral web` gives each HTTP request an ID and sends it back in the
`X-Request-ID` response header (see [Request IDs](api.md#request-ids)). Each
log record that the request causes has the ID in its `request_id` field.
These records include the backend commands, the Proxmox API calls, the
requests to peers, and the background tasks. A metrics collection run has an
ID that starts with `collect-`. To find all the records of a failed request,
search the log for its ID:

```bash
corral web --log-level debug --log-format json 2>corral-web.log
grep '"request_id":"5f0c9e2a7b1d4c3e"' corral-web.log
```

At the `info` level, the log of `corral web` shows the tasks that start and
end. At the `warn` level, it shows server errors, failed tasks and contexts
that Corral cannot list. Local QEMU commands, console streams and some
package-level KubeVirt helpers do not have the request ID yet.

Use these commands and files too.

| Tool | What it shows |
|---|---|
| `corral list` | Every instance, its backend, status and context. Also a warning for each context that failed |
| `corral info NAME` | The raw state from the backend (`kubectl get vm -o json`, `incus info`, `virsh dominfo`, or the local `metadata.json`) |
| `corral logs NAME` | KubeVirt: the `compute` container log of the `virt-launcher` pod. Local QEMU: the systemd journal of the VM unit |
| `corral logs NAME --serial --tail 100` | Local QEMU: the serial console of the guest, also when the guest never reaches SSH |
| `corral diagnose NAME --bundle-dir ./diag` | Local QEMU: serial log, screenshot, journal, QMP status and host checks in one directory |
| `corral screenshot NAME` | Local QEMU and KubeVirt: a PNG of the screen. `--require-paint` fails when the screen is blank |
| `corral qmp NAME query-status` | Local QEMU: the raw state from the QEMU monitor |
| `--json` | Machine-readable output for `corral list` and other commands |

On Incus, `corral logs` prints the console log of the instance
(`incus console --show-log REMOTE:NAME`). libvirt keeps no guest log that
Corral can read, so `corral logs` refuses libvirt domains. Use
`virsh -c URI console NAME`.

Files on the host where Corral runs:

| Path | Contents |
|---|---|
| `~/.config/corral/config.yaml` | Contexts, the default backend, and other settings (`$XDG_CONFIG_HOME/corral` when that variable is set) |
| `~/.local/share/corral/registry.json` | The backend, namespace, user and password that Corral saved for each VM |
| `~/.local/share/corral/vms/NAME/` | Local QEMU VM: `disk.qcow2`, `serial.log`, `metadata.json` (address and SSH port), `qmp.sock` |
| `~/.local/share/corral/vms/NAME/qemu.log` | Local QEMU output when the host has no systemd user session |
| `~/.config/systemd/user/corral-NAME.service` | The systemd user unit of a local QEMU VM |

## Names and contexts

| Error | Cause and fix |
|---|---|
| `instance "NAME" does not exist` | No context has an instance with that name or ID. Run `corral list`. If a context shows a warning, fix that context first |
| `instance name "NAME" is ambiguous; use one of the fully-scoped IDs shown by corral list` | Two contexts have an instance with that name. Use the ID from the `id:` line of `corral list` |
| `no VMs found. Create one: corral create <name>` | You gave no name, and the inventory is empty |
| `unknown context "NAME"` | `corral context use` takes a context name, not a URI or a remote. Add the context first with `corral context add` |
| `qemu is local and does not take a context` | Do not give `--context` to `corral context add --backend qemu` |
| `cluster unreachable (a recent kubectl call timed out; retrying in ...)` | A `kubectl` call timed out, so Corral skips the cluster for 15 seconds. Look at the cluster, or raise `CORRAL_KUBECTL_TIMEOUT` (default `5s`) |

A `--context` value that is not a context name is not an error. Corral gives
the value to the backend as a kube context, an Incus remote or a libvirt URI.
A misspelled name can therefore point Corral at a target that does not
exist. Run `corral context list` to see the names.

## Create

| Error | Cause and fix |
|---|---|
| `VM "NAME" already exists (backend: ...). Use --force to overwrite` | Corral already knows that name. Pick a new name, or add `--force` |
| `QEMU not found. Install: brew install qemu` | Corral cannot find both `qemu-system-x86_64` and `qemu-img`. Install QEMU from your distribution or Homebrew |
| `UEFI requested: UEFI firmware not found on host (install edk2-ovmf or ovmf)` | Install OVMF, or set `CORRAL_OVMF_CODE` and `CORRAL_OVMF_VARS` to its files. Use `--firmware bios` for a BIOS guest |
| `vsock requested but not available: ...` | See the `VSOCK host support` check of `corral doctor` |
| `swtpm not found: install the 'swtpm' package` | Install `swtpm`, or create the VM without `--tpm` |
| `unknown image "NAME" — see corral images` | `--image` takes a name from `corral images` on KubeVirt. Incus takes an Incus image instead |
| `bootc build needs the bootc plugin: corral plugin install bootc` | Install the plugin, then try again |
| `no SSH public key found (needed for bootc VM)` | Make a key with `ssh-keygen`. A bootc VM gets your public key for `root` |
| `the Incus backend creates from an Incus image; use --image ...` | See [Incus setup](incus-backend-setup.md#5-create-and-use-instances) |
| `libvirt create needs --iso or --qcow` | See [libvirt setup](libvirt-backend-setup.md#5-create-and-use-domains) |

`corral create` without a backend flag goes to the default backend. This is
`CORRAL_DEFAULT_BACKEND`, then `default.backend` in the configuration file,
then `qemu`. The KubeVirt source flags `--image`, `--container-disk`,
`--import` and `--pvc` select KubeVirt when the default is `qemu`. Run
`corral config get-default-backend` to see the value.

### The local VM is slow

Corral does not stop when `/dev/kvm` is not available. It starts QEMU with
software emulation (TCG), which is much slower. Run `corral doctor`. If
`KVM acceleration` shows `!`, add yourself to the `kvm` group and log in
again.

### KubeVirt import does not finish

`corral list` shows the import state of a KubeVirt disk:

| State | Meaning |
|---|---|
| `↓ queued` | CDI has not started the import. The PVC can wait for a node, or the storage class cannot provision |
| `↓ importing` or `↓ NN%` | CDI copies the image |
| `↓ Failed` | The import stopped with an error |
| `✓ ready` | The disk is ready |

Corral has no timeout for an import. If the state does not change, look at
the CDI importer pod and the PVC in the namespace of the VM (default
`corral-vms`):

```bash
kubectl get datavolume,pvc,pods -n corral-vms
kubectl describe datavolume NAME -n corral-vms
```

## Boot

If the VM starts but you cannot log in, read the console of the guest first.

- Local QEMU: `corral logs NAME --serial` shows the console from the
  firmware on. `corral diagnose NAME` looks in it for a kernel panic or an
  emergency shell. `corral screenshot NAME` shows the screen.
- KubeVirt: open the console in the web UI, or run `corral viewer NAME`.
  `corral screenshot NAME` saves the screen as a PNG through `virtctl vnc
  screenshot`.
- Incus and libvirt: open the console in the web UI.

| Error | Cause and fix |
|---|---|
| `VM "NAME" does not exist` (local QEMU) | The systemd unit or the VM directory is missing. Run `corral list`, or create the VM again |
| `starting VM: ...` (local QEMU) | systemd could not start the unit. Run `corral logs NAME` |
| `QEMU exited immediately (...)` | QEMU stopped at start. The message ends with the last lines of `qemu.log` |
| `VM "NAME" has no recorded QEMU command — recreate it ...` | The host has no systemd user session, and an older Corral made this VM. Run `corral create NAME --force ...` |
| `no serial log — recreate the VM (corral create --force ...) to pick up console capture` | An older Corral made this VM. Make it again to get a serial log |
| `virtctl not found — install: brew install virtctl` | Install `virtctl`. KubeVirt start, stop, SSH and consoles need it |

## SSH

`corral ssh` works differently on each backend:

| Backend | How `corral ssh` connects |
|---|---|
| Local QEMU | `ssh` to the Tailscale address of the host, on the port that QEMU forwards to port 22 of the guest |
| KubeVirt | `virtctl ssh`, through the Kubernetes API. The guest needs no open port |
| Incus | `incus exec REMOTE:NAME -- bash`. This is not SSH, and Corral ignores the SSH flags |
| libvirt | Not supported. Use `ssh` to the guest address |
| Corral peer | `peer/NAME` goes through the remote Corral |

The user is `-u`, then the user that Corral saved for the VM, then `$USER`,
then `root`. When you give `-u`, Corral saves it for the next time. The
password is `--password`, then the password that Corral saved. For a
KubeVirt VM, Corral saves the cloud-init password at create time.

| Error | Cause and fix |
|---|---|
| `VM "NAME" has no Tailscale IP — is Tailscale running?` | Tailscale did not answer when you created this local VM. Corral then bound VNC and SSH to `127.0.0.1`. Use `ssh -p PORT USER@127.0.0.1`, with `ssh_port` from `metadata.json`. Or start Tailscale and create the VM again |
| `VM "NAME" has no forwarded SSH port — recreate it with this corral version` | An older Corral made this VM. Make it again |
| `ssh not found in PATH` | Install the OpenSSH client |
| `sshpass not found (needed for password auth) — install: brew install sshpass` | Password login needs `sshpass`. Install it, or use a key with `-i` |
| `VM "NAME" has no vsock — create with --vsock (or --vsock-cid)` | `corral ssh --vsock` needs a VM that you made with `--vsock` |
| `libvirt domain "NAME" has no discovered SSH address; ...` | Expected. Get the address with `virsh domifaddr NAME` and use `ssh` |

### SSH refuses the login

- **KubeVirt.** Cloud-init sets the password for the default user of the
  image, for example `fedora` or `ubuntu`. Corral uses `$USER` when you do
  not give `-u`. Try `corral ssh NAME -u fedora`.
- **bootc.** Corral puts your public key on the `root` account. Use
  `corral ssh NAME -u root`.
- **The guest has no SSH server.** Use the console in the web UI, or
  `corral type` and `corral key` on a local QEMU VM.

### Tailscale

- Local QEMU VMs need the `tailscale` command on the host. At create time,
  Corral reads `tailscale ip -4`. VNC and SSH of the VM listen on that
  address only.
- KubeVirt VMs get a tailnet Service only when the cluster has the
  `tailscale` IngressClass from the Tailscale operator. Without it, Corral
  skips that step. `corral ssh` still works through `virtctl`.
- `corral config` shows whether you set `TS_AUTHKEY` or
  `tailscale.auth_key`. No create path uses the key today.

## Delete

| Error | Cause and fix |
|---|---|
| `still present after 5m0s: persistentvolumeclaim/NAME` | `--wait` stopped before Kubernetes removed the object. A pod that uses the disk keeps the PVC. Run `kubectl get pod -n NAMESPACE` to find the pod, then run the delete again |
| `delete pvc: ... is forbidden` | Your Kubernetes account cannot delete that kind of object. Corral continues with the other objects. Ask your cluster administrator for the `delete` permission |
| `2 of 3 deletes failed` | Corral deleted the other names. The lines above the error give the cause for each name. With `--json`, the `error` field gives it |
| `instance "NAME" does not exist` | No context has a VM with that name, and the default context has no objects for it. Run `corral list`, or add `--context` |

An interrupted `corral bootc create` leaves a builder VM, `NAME-bootc-builder`,
and a disk, `NAME-bootc-disk`. Do not delete these objects with `kubectl`.
Run `corral delete NAME`. It removes the builder VM, its Secret and the disk
in one step. To remove all old interrupted builds, run
`corral gc --builders`.

## Move

`corral move` runs a check before it changes anything. If the check fails,
Corral prints the reasons and `this move was refused; nothing was changed`.

| Reason | Fix |
|---|---|
| `no destination backend was given` | Add `--to BACKEND` |
| `the source and destination are both ... in the same context` | To move between nodes of one backend, use `corral migrate` (KubeVirt) |
| `containers cannot be moved between backends` | Move VMs only |
| `the ... backend cannot export a disk` | Corral cannot read the disk on this source backend |
| `the ... backend cannot receive a moved instance` | Pick another `--to` backend |
| `the guest boots via UEFI and the ... backend has no firmware path` | Install OVMF, or pick a destination with UEFI |
| `... has ... free and the disk needs up to ...` | Free space, or set `--scratch` to a larger directory |
| `... is a remote hypervisor and its disks are not on this filesystem` | Run Corral on the libvirt host, or use shared storage |

Run `corral move NAME --to BACKEND --dry-run` first. It prints the plan and
changes nothing. If the move fails after it starts, the error tells you
whether the source is intact. Corral stops the source, and it deletes the
source only when you give `--delete-source`.

## VDI desktops

These are the errors of the VDI broker (`/api/vdi/`, the Desktops screen,
and `corral vdi connect`). [docs/vdi.md](vdi.md#self-service-broker) explains
the broker.

| Error | Cause | Fix |
|---|---|---|
| `the VDI broker is off (set CORRAL_VDI_BROKER=1)` | `corral web` runs without the broker | Set `CORRAL_VDI_BROKER=1` and restart `corral web` |
| `VDI self-service needs an authenticated identity` (401) | The request came without the identity headers | Connect through the Tailscale ingress or the `corral-auth` gateway |
| `a service identity cannot use VDI self-service` (403) | A Corral peer token sent the request | Use a user identity; peers cannot hold desktops |
| `the desktop belongs to another user` (403) | Another user holds the desktop | Use your own desktop, or ask an admin to release it |
| `pool has no free members` (409) | Users hold every member of the pool | Wait for a release, or ask an admin to add members |
| `no claimed desktop by that ID` (404) | The claim ended: a release or a reclaim | Claim the pool again |
| `the claim on this desktop expired` (409) | No broker renewed the Lease for an hour | Claim the pool again; the claim then renews |
| `the desktop is not running; start it, then connect` (409) | The member stopped | Select **Start**, or send `POST .../start` |
| `the desktop offers no console Corral can open` (409) | The VM has no RDP, graphics device, or serial console | Enable a console in the golden VM |
| `startError` in a claim | The member did not start; you keep the desktop | Fix the cause (often cluster capacity), then start it again |
| A release task ends with `the claim is kept; retry the release` | The stop or the recreate failed | Release again after you fix the cause |
| A desktop was released while you were away | The disconnect grace period or the maximum duration ran out | Claim again; see [Sessions and reclaim](vdi.md#sessions-and-reclaim) |

## Unsupported operations

Some commands work on KubeVirt only. On another backend they stop with
`COMMAND is only supported for KubeVirt VMs`. These commands are
`migrate`, `scale`, `adddisk`, `rmdisk`, `addnic`, `lanservice`,
`snapshot`, `pause`, `unpause`, `export` and `template`. `corral clone`
stops with `cloning is only supported on KubeVirt VMs`. See
[backend support](backend-support.md#incus-and-libvirt-snapshots-migration-and-hotplug)
for the gaps and for the native commands that you can use until then.

Some commands work on local QEMU only. On another backend they stop with
`diagnose is only supported for QEMU VMs` or a similar message. These are
`diagnose`, `qmp`, `type` and `key`. `screenshot` also works on KubeVirt. On
any other backend it stops with
`the NAME backend does not support screenshot yet`.

## Environment variables

| Variable | Effect |
|---|---|
| `CORRAL_DEFAULT_BACKEND` | Backend for `corral create` without a backend flag |
| `CORRAL_KUBE_CONTEXT` | Kube context for `kubectl` and `virtctl`. `--context` sets it |
| `CORRAL_INCUS_REMOTE` | Incus remote. `--context` sets it for Incus |
| `CORRAL_LIBVIRT_URI` | libvirt URI. `--context` sets it for libvirt |
| `CORRAL_NAMESPACE` | KubeVirt namespace (default `corral-vms`) |
| `CORRAL_KUBECTL_TIMEOUT` | Timeout for each `kubectl` call (default `5s`) |
| `CORRAL_LOG_LEVEL`, `CORRAL_LOG_FORMAT`, `CORRAL_LOG_FILE` | Log level, format and file. See [Collect evidence](#collect-evidence) |
| `CORRAL_OVMF_CODE`, `CORRAL_OVMF_VARS` | UEFI firmware files for local VMs |
| `CORRAL_PROXMOX_TOKEN`, `CORRAL_PROXMOX_TOKEN_<CONTEXT>` | Proxmox API token |
| `CORRAL_VDI_BROKER`, `CORRAL_VDI_NAMESPACES` | Turn on the VDI broker of `corral web`, and the namespaces it serves |
| `CORRAL_VDI_DISCONNECT_GRACE`, `CORRAL_VDI_MAX_SESSION` | The VDI reclaim policy (default `15m` and no maximum) |
| `CORRAL_VDI_BROKER_URL` | Default `--broker` of `corral vdi connect` |
| `KUBECONFIG` | Kubeconfig file. When it exists, Corral adds a `kubevirt` context |
| `TS_AUTHKEY` | Tailscale auth key that `corral config` shows |
| `XDG_CONFIG_HOME` | Parent directory of the Corral configuration |

## Questions

**Why can't I SSH to my VM?** Run `corral list` and look at the backend.
Then find the error in the [SSH](#ssh) table. On libvirt, `corral ssh` is
not supported. On Incus, `corral ssh` uses `incus exec`.

**How do I move a VM to another backend?** Use `corral move NAME --to
BACKEND --dry-run`, then run it again without `--dry-run`. The guest stops
during the move and gets a new MAC address. See [Move](#move).

**How do I move a VM to another node?** On KubeVirt, use
`corral migrate NAME --node NODE`. Other backends do not support this from
Corral yet.

**Why does `corral list` not show my cluster?** A context failed. The
warning line names it. Run `corral doctor --context NAME`.

**Why does my Incus container not show in `corral list`?** Corral shows
Incus containers in `corral ct list`. `corral list` shows VMs.

**Can I try Corral without a cluster?** Yes. Add `--demo` to any command,
for example `corral web --demo`.
