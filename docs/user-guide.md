# Corral User Guide & Feature Manual

Welcome to the comprehensive user guide for **Corral** — the unified management platform for virtual machines, pet-pod containers, and local hypervisors.

## Backends, contexts, and peers

A *context* is a named connection to one backend. Examples are the local
host, a kube context, an Incus remote, a libvirt URI and a Proxmox VE
cluster. Corral shows the instances of all contexts at the same time.

```bash
corral setup                                  # find the backends on this host and save them
corral context list                           # the * marks the default destination
corral context add hv --backend libvirt --context qemu+ssh://hypervisor/system
corral context use hv                         # aliases: set, switch
corral config set-default-backend incus       # backend for creates with no backend flag
```

`corral context use` takes a context name, not a URI. It does not change
kubectl or the Incus client. The global `--context NAME` flag selects a
context for one command.

A local dashboard can aggregate an in-cluster Corral without a local
kubeconfig:

```bash
corral peer add homelab https://corral.example.ts.net
```

For a console, Corral tries the direct address that the VM advertises first.
It relays through the peer only when direct access is not available.

## Start here

Run `corral --demo`, `corral web --demo` or `corral list --demo` to try
Corral with a built-in fake fleet. Then pick the backend that you have.

| Backend | Setup guide |
|---|---|
| Local QEMU/KVM | No setup. Run `corral doctor --context local` |
| KubeVirt | [kubevirt-proxmox-setup.md](kubevirt-proxmox-setup.md) |
| Incus | [incus-backend-setup.md](incus-backend-setup.md) |
| libvirt | [libvirt-backend-setup.md](libvirt-backend-setup.md) |
| Proxmox VE | The commands below, and [ADR-0009](adr/0009-proxmox-backend.md) |

**Local QEMU/KVM** — a VM on this machine, under a systemd user unit:

```bash
corral create scratch --iso ~/Downloads/ubuntu-24.04.iso --disk 40G --start
corral viewer scratch          # VNC, to run the installer
corral ssh scratch             # after the guest has an SSH server
```

**KubeVirt** — a VM on the cluster of your current kube context:

```bash
corral doctor
corral create web --kubevirt --image fedora
corral start web
corral ssh web -u fedora
```

**Incus** — a VM on an Incus remote:

```bash
corral context add lab --backend incus --context local
corral create web --context lab --image images:ubuntu/24.04 --mem 4GiB
corral ssh web                 # runs incus exec
```

**libvirt** — a domain on a local or remote hypervisor:

```bash
corral context add hv1 --backend libvirt --context qemu+ssh://admin@hv1.example.net/system
corral create db --context hv1 --iso /var/lib/libvirt/images/debian-12.iso
corral list
```

**Proxmox VE** — the guests of an existing cluster:

```bash
corral context add pve --backend proxmox --context pve.example.net
export CORRAL_PROXMOX_TOKEN='corral@pve!corral=TOKEN-SECRET'
corral doctor --context pve
corral list
```

Corral reads the Proxmox token from `CORRAL_PROXMOX_TOKEN`, or from
`proxmox.token` in the context entry of `~/.config/corral/config.yaml`.
`corral create NAME --context pve --iso local:iso/debian.iso` creates a VM on
Proxmox. The ISO must already be on a PVE storage, named by its volume id.
A backend that the CLI cannot create on (VirtualBox, vSphere, Xen, Nutanix)
stops with `create is not supported on backend NAME from the CLI`; Corral
never creates the VM on a different backend instead.

## Common workflows

### CLI, TUI or web UI

The CLI, the TUI and the web UI use the same inventory and the same
configuration. Use the interface that fits the task:

| Interface | Use it to |
|---|---|
| CLI (`corral COMMAND`) | Write scripts and CI jobs. Add `--json` for machine-readable output |
| TUI (`corral`) | Look at the whole fleet in a terminal, and do actions from a menu that shows only the operations that the backend supports |
| Web UI (`corral web`) | Use consoles in the browser, dashboards and graphs, and access from a phone |

Some operations have a gap on one interface. For example, Incus and libvirt
snapshots work in the TUI only. See
[backend support](backend-support.md#incus-and-libvirt-snapshots-migration-and-hotplug).

### Create, connect and remove

```bash
corral create web --kubevirt --image fedora
corral list                              # status, context and the ID of each instance
corral info web
corral ssh web -c 'uptime'               # one command, no shell
corral ssh web -L 8080:localhost:80      # open http://localhost:8080 for port 80 of the VM
corral stop web
corral delete web                        # asks first; -f does not ask
```

When two contexts have an instance with the same name, give the ID from the
`id:` line of `corral list` instead of the name.

### Find VMs and read their facts

```bash
corral list --prefix tuna-q-                 # names that start with tuna-q-
corral list --tag pair=run42 --json          # VMs with the tag pair=run42
corral list --tag pair=run42 --tag desktop=gnome
corral info web --json                       # one structured object for scripts
```

The `--prefix` and `--tag` filters apply to the table and to `--json`. When
you give `--tag` two or more times, a VM must have all of the tags. A tag
without a value, for example `--tag perf`, means `perf=true`. Only KubeVirt
VMs have tags. In `corral list --json`, the `tagValues` field shows the tags
of each VM.

`corral info NAME --json` shows the name, namespace, backend, status, node,
CPU topology, dedicated CPUs, memory, disks, input devices, firmware, IP
addresses, tags, labels and the bootc image. Without `--json`, `corral info`
shows the description from the backend. The structured output works on
KubeVirt only. On other backends, `corral info NAME --json` stops with an
error.

### Make a matched pair of VMs for performance tests (KubeVirt)

A performance comparison needs two VMs that are the same, except for the
guest. Use these flags on `corral create` or `corral bootc create`:

| Flag | Result |
|---|---|
| `--node NODE` | The two VMs run on the same node. |
| `--cpu`, `--mem`, `--disk` | The two VMs have the same size. Use the same values for the two VMs. |
| `--dedicated-cpus` | Each vCPU gets its own host CPU. The memory request is equal to the memory limit, thus the VM has the guaranteed QoS class. |
| `--isolate-emulator-thread` | The QEMU emulator thread gets one more host CPU. You must also use `--dedicated-cpus`. |
| `--tag KEY=VALUE` | Corral writes the label `corral.dev/tag.KEY=VALUE` on the VM. Use the same tag on the two VMs to make a group. |

```bash
corral bootc create gnome-vm --image ghcr.io/tuna-os/marlin:latest \
  --node karnataka --cpu 4 --mem 6G --disk 40Gi \
  --dedicated-cpus --tag pair=run42 --tag desktop=gnome
corral bootc create tuna-vm --image ghcr.io/tuna-os/marlin:latest \
  --node karnataka --cpu 4 --mem 6G --disk 40Gi \
  --dedicated-cpus --tag pair=run42 --tag desktop=tuna
corral list --tag pair=run42 --json
corral info gnome-vm --json | jq '{node, cpu, memory}'
```

Make sure that the two VMs show the same `node`, `cpu.vcpus` and `memory`
values, and `cpu.dedicatedCpuPlacement: true`.

`--dedicated-cpus` needs a node with the label `cpumanager=true`. KubeVirt
sets this label when two conditions are true:

1. The kubelet on the node uses the static policy of the CPU manager
   (`cpuManagerPolicy: static`) and keeps CPUs for the system
   (`reservedSystemCPUs`).
2. The KubeVirt CR enables the `CPUManager` feature gate.

If the node does not have the label, Corral stops before it creates the VM,
and the error names the node. If you do not give `--node`, Corral looks for
one schedulable node with the label. For `corral bootc create`, Corral does
this check before the disk build starts. The builder VM does not get
dedicated CPUs or tags.

The flags work on KubeVirt only. On other backends, Corral stops with an
error and does not create the VM. You cannot use `--dedicated-cpus` with
`--instancetype`, because the instancetype sets the CPU configuration.
### Delete VMs and clean up

```bash
corral delete web db -f                  # two VMs, no question
corral delete web -f --wait              # stop when all objects of the VM are gone
corral delete web db -f --wait --json    # one result for each VM, for scripts
corral gc --builders                     # remove bootc builds that stopped before the end
```

`corral delete` accepts one or more names. It asks one question for all the
names. Use `-f` to delete without the question.

On KubeVirt, `corral delete` removes the VM and all the objects that Corral
made for it:

- The VM and its VMI.
- The disks: the DataVolumes and the PVCs.
- The cloud-init Secret and the Windows answer-file ConfigMap.
- The tailnet proxy and the LAN Service.
- The snapshots, the restores and the clones of the VM.
- The CronJobs of `corral schedule`, `corral snapsched` and `corral backup`.
- The builder VM, the disk and the Secret of a `corral bootc create` that
  stopped before the end.

Corral finds an object by its name or by the label `corral.dev/vm=NAME`.
Corral does not delete a labeled disk that a different VM uses. A clone gets
the labels of the source disks, thus this rule keeps the disks of the clone.

You can also delete a name that has only the objects of an interrupted
build. For example, you stop `corral bootc create desk` before the VM exists.
Then `corral delete desk` removes the builder VM and the disk. Corral looks
for these objects in the default context. Use `--context` for a different
context.

Successful deletion also retires saved credentials for that exact backend,
context, namespace, and peer identity. Older name-only registry entries without
a recorded remote context are retained with a warning: Corral cannot prove which
cluster owns them. They are not reused as credentials for a different target,
and saved registry data alone never establishes that a VM still exists. Keep or
remove such legacy data manually in `~/.local/share/corral/registry.json` after
checking its ownership; changing the default context does not migrate it.

A delete does not wait for Kubernetes finalizers. Add `--wait` to wait until
the deleted objects are gone. `--timeout` sets the maximum time. The default
is 5 minutes. If an object stays after that time, the command fails and
names the object.

With `--json`, the output is a list with one item for each name:

```json
[
  {
    "name": "web",
    "deleted": true,
    "removed": ["virtualmachine.kubevirt.io/web", "persistentvolumeclaim/web-disk"]
  },
  {
    "name": "db",
    "deleted": false,
    "removed": [],
    "error": "instance \"db\" does not exist"
  }
]
```

`removed` gives each object as `kind/name`. Other backends delete a VM as one
unit, thus their list is empty. If one VM fails, Corral continues with the
other names. The exit status is not zero when Corral cannot delete a VM.

`corral gc --builders` removes a bootc builder VM that has no final VM and is
older than `--builder-age`. The default age is 2 hours. Corral also removes
the disk and the Secrets of that build. A build that completed but that you
did not finish with `corral bootc create --resume` counts as interrupted.

### Wait until a VM is ready

Use `corral wait` in scripts. Do not write a loop around `corral ssh`. The
command stops when all the conditions that you name are true:

```bash
corral wait vm --unit gdm.service --paint --timeout 15m
```

| Flag | The condition is true when |
|---|---|
| `--running` | The backend shows that the VM runs. Corral always does this check first |
| `--ssh` | The guest runs a command over SSH as `--ssh-user` (default `root`) |
| `--boot-complete` | `systemctl is-system-running` shows `running` or `degraded` |
| `--unit NAME` | `systemctl is-active NAME` shows `active`. You can use this flag more than one time |
| `--exec CMD` | The command `CMD` in the guest exits with 0. You can use this flag more than one time |
| `--paint` | A screenshot of the console is not blank |
| `--boot-id ID` | The guest has a boot ID that is not `ID` |

Corral does the checks in the order of the table, one time each
`--interval` (default `5s`). It writes each condition to stderr when the
condition becomes true. `--boot-complete`, `--unit`, `--exec` and `--boot-id`
use SSH, so they also add the SSH condition. The backend must support
`corral screenshot` for `--paint`.

Each SSH check also reads the boot ID of the guest. If the boot ID changes,
the guest restarted, and Corral does the guest checks again. After
`corral restart`, the old boot can still answer for some seconds. To prevent
this, give the boot ID from before the restart:

```bash
old=$(corral --json wait vm --ssh | jq -r .boot_id)
corral restart vm
corral wait vm --boot-id "$old" --unit gdm.service --paint --timeout 15m
```

If a condition is not true before `--timeout` (default `10m`), the command
exits with a value that is not 0. The message gives the condition and the
last result of its check. With `--json`, the command writes one object to
stdout:

```json
{"vm": "vm", "ready": true, "elapsed_seconds": 95.2, "boot_id": "b5daeddb-…",
 "conditions": [{"name": "unit:gdm.service", "ok": true, "elapsed_seconds": 80.1, "observed": "active"}]}
```

`elapsed_seconds` of a condition is the time at which it became true. A
condition that Corral did not check has no `observed` value.

### Clone a VM and use templates (KubeVirt)

```bash
corral clone web web-copy
corral images pull ubuntu                # a stopped VM with the template mark
corral template ls
corral template new tmpl-ubuntu dev1     # a new VM from the template
corral template mark web                 # use your own VM as a template
```

Clone and templates work on KubeVirt only. On other backends, use a cold
move or the native clone command of the backend.

### Move a VM to another backend

`corral move` copies the disk to another backend and makes a VM there. It is
a cold move: the guest stops, and it gets a new MAC address and usually a new
IP address. The source stays, stopped, unless you add `--delete-source`.

```bash
corral move web --to libvirt --dry-run   # the plan and the warnings, no change
corral move web --to libvirt
corral move web --to qemu --name web-local
```

To move a KubeVirt VM to another node of the same cluster with no stop, use
`corral migrate web --node NODE`.

### Snapshots

```bash
corral snapshot create web
corral snapshot ls web
corral stop web
corral snapshot restore web SNAPSHOT
corral snapshot rm web SNAPSHOT
```

The `corral snapshot` commands work on KubeVirt. For local QEMU, Incus and
libvirt instances, use the **Snapshots** action in the TUI.

### Install plugins

Plugins add workflows that not every user needs. They install into
`~/.local/share/corral/plugins` and run as `corral NAME`.

```bash
corral plugin search
corral plugin info backup
corral plugin install backup
corral plugin update                     # every plugin that you did not pin
```

If a plugin asks for permissions, `corral plugin install` stops and names
them. Read them with `corral plugin info NAME`, then add
`--accept-permissions`.

| Plugin | Install it when | First commands |
|---|---|---|
| `bootc` | You boot bootable container images as VMs | `corral bootc create dev --image bluefin` |
| `backup` | You keep VM disks in S3, R2 or another rclone remote | `corral backup create web --dest r2:backups/corral`, `corral backup list` |
| `windows` | You install Windows guests with UEFI, TPM and virtio drivers | `corral windows requirements`, `corral windows create win11 --iso URL` |
| `gpu` | You give a GPU or another PCI device to a VM | `corral gpu list`, `corral gpu attach web --device ID` |

[first-party-plugins.md](first-party-plugins.md) lists the backends that each
plugin supports.

### When something fails

Run `corral doctor`, then look for the error text in
[problems and fixes](troubleshooting.md).

---

## 1. Overview & Architecture

Corral puts VMs and containers from many backends into one interface, one CLI, and one Tailscale network.

```mermaid
flowchart TD
    User([User / CLI / Web UI]) --> Corral[Corral Core]

    subgraph Backends
        Corral --> QEMU[QEMU / KVM Local]
        Corral --> KubeVirt[KubeVirt / Kubernetes]
        Corral --> CT[Pet-Pod CT / K8s]
        Corral --> Incus[Incus / LXC and VMs]
        Corral --> Libvirt[libvirt local or SSH URI]
        Corral --> Proxmox[Proxmox VE cluster]
        Corral --> Peer[Remote Corral peer]
    end

    subgraph Access Layer
        QEMU --> Tailnet[Tailscale Tailnet]
        KubeVirt --> Tailnet
        CT --> Tailnet
        Incus --> Tailnet
        Libvirt --> Tailnet
        Proxmox --> Tailnet
        Peer --> Tailnet
    end
```

### Supported Compute Backends

| Backend | Scope | Tech Stack | Use Case |
|---|---|---|---|
| **QEMU** | Local Host | QEMU / KVM + systemd | Fast, local ephemeral or dev VMs on your workstation |
| **KubeVirt** | Cluster | KubeVirt + Kubernetes | Production VMs with live migration, PVC storage, and scaling |
| **Container (CT)** | Cluster | Kubernetes Pod + PVC | "Pet Pods" — persistent Linux containers (Distrobox on K8s) |
| **Incus** | Local or remote | Incus remote | Containers and VMs managed through existing Incus trust |
| **libvirt** | Local or remote | libvirt URI / OpenSSH | Existing domains and remote QEMU hypervisors |
| **Proxmox VE** | Cluster | Proxmox VE HTTPS REST API | VMs and LXC containers managed on existing PVE clusters |

---

## 2. Interactive Interfaces

### Web Dashboard

Run `corral web`, then open the Proxmox-style web UI on port 8006 of `localhost`. For public, local, homelab, and always-on setups see [deployment modes](deployment-modes.md).

![Automated unified fleet capture](screenshots/generated/web-fleet.png)

![Datacenter View](screenshots/dashboard.png)

#### Features:
- **Datacenter Tree**: Navigate through all VMs, CTs, and Incus instances across local and cluster nodes.
- **Live CPU & Memory Sparklines**: Real-time load graphs for each VM.
- **Tag Filters**: Filter instances by custom tags (`prod`, `dev`, `desktop`).
- **Mobile Responsive**: Manage your VM fleet from your mobile browser.
- **Theme & Brand**: Customize accent colours and the header brand, and inject custom CSS. Use CLI flags, a config file, or the built-in Settings page.

![Mobile Dashboard](screenshots/dashboard-mobile.png)

#### Topology

Topology, under Datacenter in the tree, draws the fleet as the machines it
runs on. Each node is a card, with its guests inside and the CPU and memory
they take summed at the top. A tile's left edge shows the guest's state.

To migrate a guest, drag its tile onto another node's card. corral asks you
to confirm first. If it cannot make the move, it says why and refuses it. The
keys `g` then `t` open the topology from anywhere.

#### Several dashboards

A Datacenter or node screen can hold more than one dashboard. Use the picker
above the widgets to switch, and New to add one. Each dashboard keeps its own
arrangement and its own density. So you can keep one screen for capacity and
another for a failure you need to chase.

Rename changes the name of the current dashboard. Delete removes it and its
arrangement; the widgets stay available under Add widget. A screen always
keeps at least one dashboard, so the last one does not delete.

Dashboards live in your own browser, like the rest of the layout. corral
shares them with nobody.

#### Dashboard density

The Density control above a dashboard sets how much room each widget takes:
Compact, Cosy or Roomy. Compact fits more on one screen. Each dashboard keeps
its own choice, and Reset layout puts it back with the arrangement.

#### Links to a screen

The address bar follows what you look at. Select a guest and open its console,
and the address holds both. Send that address to somebody else and they arrive
on the same screen. The back and forward buttons move through the screens you
visited.

The address carries the tree view, the selection and the open tab. On a screen
with the guest list, it also carries the list's filters and sort. So you can
send somebody the stopped guests on one node, sorted by memory. It does not
carry how wide you keep the sidebar, which columns you hid, or where you put
the dashboard widgets. Those stay in your own browser.

#### Web keyboard shortcuts

Press `Ctrl+K` (`⌘K` on macOS) to open the command palette. Type part of a
name to find a VM, a CT, a node or a pool. Type a verb and a name to find an
action, for example `stop web-prod`. Press `Enter` to run the first result.
The items that you used last show at the top of the list.

| Key | Action |
|---|---|
| `Ctrl+K` | Open the command palette |
| `Ctrl+B` | Hide or show the sidebar |
| `?` | Show the list of shortcuts |
| `/` | Filter the tree by guest name |
| `c` | Open the console of the selected VM |
| `s` | Start or stop the selected VM |
| `g` then `d` | Go to the datacenter |
| `g` then `t` | Go to the topology |
| `v` | Switch the tree to the next view |
| `t` | Show the Tasks panel, and hide it again |
| `e` | Show the Events panel for the selection, and hide it again |

You can also narrow the palette by a field. Type `node=corral-1` to see only
the guests on that node, or `status=stopped` to see the stopped ones. The
fields are `node`, `namespace`, `status`, `backend` and `tag`. A field and a
name work together: `node=corral-1 web` searches only that node.

The one-key shortcuts do not operate when you type in a field or in a console.
A read-only user does not see the actions that change a guest.

#### Theme & Branding

In Corral's web UI, you can fully customize the accent colour, the header
brand, and custom CSS. You need no source-code changes.

**CLI flags** (highest priority, override everything):
```bash
corral web --accent \"#3b82f6\" --brand-title \"My Lab\" --brand-emoji \"⚡\" --brand-subtitle \"Engineering\"
```

**Config file** (`~/.config/corral/config.yaml`, loaded on startup):
```yaml
web:
  accent: \"#22c55e\"        # CSS hex colour
  accent_2: \"#16a34a\"      # hover/active variant
  brand_title: \"My Lab\"
  brand_emoji: \"⚡\"
  brand_subtitle: \"Engineering\"
  custom_css: |
    .btn.primary { border-radius: 20px; }
    .card { background: var(--panel-2); }
```

**Settings page** (web UI → Settings in the sidebar):
- Colour picker with preset swatches (Orange, Blue, Green, Purple, Red, Amber)
- Brand title, emoji, and subtitle fields with live header preview
- Custom CSS textarea that injects styles immediately as you type
- Save button persists to `config.yaml`

Precedence: CLI flags > config.yaml > built-in defaults (🤠 Corral, orange
accent).

---

### Terminal UI (TUI)

To launch the interactive TUI (built on Bubble Tea), run `corral` with no arguments. To explore in demo mode, run `corral --demo`.

![Automated Corral TUI capture](screenshots/generated/tui-fleet.png)

The left side is a unified, searchable fleet. Wide terminals add a details
pane with canonical ID, backend/context, placement, address, resources, and
available operations. Failed remotes appear as partial-fleet warnings without
hiding healthy instances.

#### TUI keyboard shortcuts and features

- `/` fuzzy-searches names, canonical IDs, backends, contexts, nodes, and IPs.
- `Tab`, `[` and `]` cycle between the complete fleet and named contexts.
- `Enter` opens a capability-aware action menu. The menu leaves out
  unsupported operations, so they do not fail after you select them. These
  all live there: power (start/stop/restart/pause/resume), migrate, clone,
  snapshots, and events. So do the template mark, CPU/RAM, published ports,
  export, SSH, VNC, and delete.
- **Snapshots** opens the instance's captures: `n` takes one (named or
  auto-named), `Enter` restores, `x` deletes, `r` reloads. Corral supports every backend
  that can snapshot: KubeVirt, libvirt, Incus, and local QEMU. Each capture
  reports what it caught (offline, filesystem, or crash-consistent). This is
  the same as the web UI's Snapshots tab.
- **Events** shows the recent Kubernetes events for a KubeVirt VM and its
  `virt-launcher` pod, newest first.
- **Make/Unmark template** flips the golden-template label that
  `corral clone` copies from.
- CTs get their own smaller menu — start, stop, console, CPU/RAM, delete.
- `s` and `x` quickly start or stop the selected VM; `r` refreshes every
  backend.
- `d` runs scoped QEMU, KubeVirt, Incus, and libvirt diagnostics.
- The mouse works too. Click a row to select it, and scroll with the wheel.
  Double-click to open its actions (or to run the highlighted one). A click
  on a port row in the ports form toggles it. Destructive actions still go through
  their confirmation — a double click can't reach anything a keypress can't.
- `?` opens the in-app command deck.
- QEMU, KubeVirt, Incus, libvirt, and pet-pod CTs share the inventory without
  losing their backend-specific capabilities.

---

## 3. Core Feature Walkthrough

### 1. Instance Lifecycle Management

Create and manage instances across any backend with identical commands:

```bash
# Create local QEMU VM
corral create dev-vm --iso https://example.com/ubuntu.iso --disk 40G

# Create KubeVirt cluster VM
corral create prod-vm --kubevirt --image fedora

# Create on an existing Incus remote
corral create fast-vm --backend incus --context lab --image images:ubuntu/24.04

# Universal operations
corral start dev-vm
corral stop dev-vm
corral delete dev-vm
```

---

### 2. Remote Access: VNC, RDP & TTY

Corral provides single-command remote access to guest displays and shells:

#### In-Browser & Local VNC
![VM Summary & VNC](screenshots/vm-summary.png)

- **VNC Display**: `corral viewer <vm-name>` opens a VNC session. In the Web UI, `noVNC` shows the display in the browser, with nothing to install.
- **Screenshot**: `corral screenshot <vm-name> -o screen.png` saves the display of a local QEMU or KubeVirt VM as a PNG. It also tells you if the screen is blank, and `--require-paint` fails on a blank screen. KubeVirt captures go through `virtctl vnc screenshot`.
- **Interactive TTY / SSH**: `corral ssh <vm-name>` or `corral ct console <ct-name>` connects your terminal to a shell in the guest (through SSH, `virtctl ssh`, or `incus exec`).

![Local VM Framebuffer](screenshots/local-vm-console.png)

#### Scripted console access

Use `corral console <vm-name>` to get a console page for one VM. The page shows only the screen of the VM. Browser automation, for example Playwright, can use it without the dashboard.

```bash
corral console desk
# Console of desk: http://127.0.0.1:41234/console/corral-vms/desk
```

The command finds the VM and starts a web server. It prints the URL, and then serves the page until it gets SIGINT or SIGTERM. The `--addr` flag sets the listen address. The default is `127.0.0.1:0`, and the OS selects a free port. The `--json` flag gives one line of JSON:

```json
{"vm":"desk","namespace":"corral-vms","url":"http://127.0.0.1:41234/console/corral-vms/desk","websocket":"ws://127.0.0.1:41234/api/vnc/corral-vms/desk"}
```

Each `corral web` server also has the page at `/console/<namespace>/<name>`. The URL does not change while the VM exists. The page has these properties:

- It shows the screen at 1:1 scale. Add `?scale=fit` to scale the screen to the window.
- `<body data-connected="true">` shows that the console is connected.
- The canvas has the ID `vnc-canvas`. It gets keyboard focus, so key presses go to the guest.
- The page connects again after a guest reboot or a logout.
- The page uses the same websocket as the dashboard, with the same access checks.

This Python example opens the console of `desk` with Playwright:

```python
import json
import subprocess

from playwright.sync_api import sync_playwright

console = subprocess.Popen(["corral", "--json", "console", "desk"], stdout=subprocess.PIPE, text=True)
url = json.loads(console.stdout.readline())["url"]
try:
    with sync_playwright() as p:
        page = p.chromium.launch().new_page(viewport={"width": 1920, "height": 1080})
        page.goto(url)
        page.wait_for_selector("body[data-connected=true]", timeout=120_000)
        page.keyboard.type("hello")
        page.locator("#vnc-canvas").screenshot(path="desk.png")
finally:
    console.terminate()
```

`corral web --addr 127.0.0.1:0` also lets the OS select the port. The server writes the URL to stderr. With `--json`, it writes `{"url":"http://127.0.0.1:41234"}` to stdout.

#### Console keyboard and pointer

Use these commands when the guest has no SSH, for example at a greeter or at a
LUKS passphrase prompt. They work on local QEMU and KubeVirt VMs. You do not
need a VNC client.

```bash
corral key <vm-name> ret                       # press one key
corral key <vm-name> ctrl alt f2               # press a key combination
corral type <vm-name> 'my passphrase' --enter  # type text, then press Return
corral pointer <vm-name> 1236 12 --click left  # click at x=1236, y=12
```

- `corral key` uses QEMU key names: `ret`, `esc`, `tab`, `spc`, `backspace`, `delete`, `ctrl`, `alt`, `shift`, `meta_l` (the Super key), `f1` to `f24`, `up`, `down`, `left`, `right`, letters and digits. `corral key --help` shows more names. The keys go down in the given order and come up in the opposite order.
- `corral type` types only the characters of a US keyboard. If the text has a different character, the command stops and types nothing.
- `corral pointer` moves the pointer to a position on the screen. The position is in pixels from the top-left corner, and it must be in the screen that `corral screenshot` shows. Add `--click left`, `--click middle` or `--click right` to click there. Add `--double` to double-click. Only KubeVirt VMs support this command, and the VMI must have a tablet input device.
- A local QEMU VM gets the keys through its QMP monitor. A KubeVirt VM gets them on its VNC console through `virtctl vnc`, thus `virtctl` must be on your `PATH`.
- KubeVirt allows one VNC session for each VM. Each command closes the VNC session that you have open, for example the console in the web UI. Open the console again after the command.
- With `--json`, each command prints what it sent. For example, `corral pointer` prints `{"vm": "desk", "action": "pointer", "x": 1236, "y": 12, "button": "left", "double": false}`. `corral type` prints the number of characters, not the text.

---

### 3. Bootable Container Images (`bootc`)

Boot containers directly as VMs using the `bootc` plugin:

```bash
corral plugin install bootc
corral bootc create my-node --image quay.io/centos-bootc/centos-bootc:stream9
```

- Builds a bootable OS disk on the cluster with `bootc install to-disk`.
- The VM gets a USB tablet, so clicks in the noVNC console land where you aim them.
- Upgrade the OS image of a guest with `corral bootc upgrade <vm-name>`.

For desktop images without a regular account, create one in the installed
disk. Do not change the guest by hand:

```bash
corral bootc create my-node --image ghcr.io/tuna-os/marlin:tuna \
  --user tuna --password-prompt
```

The account gets a home directory in `/var/home`, a Bash login shell, and
the password. Corral writes it to the `/etc` and `/var` of the installed
deployment. The account is there at the first boot, and the login screen
shows it.

Scripts cannot answer a prompt. Give the password on stdin with
`--password-stdin`. Corral reads the first line and does not show it:

```bash
printf '%s\n' "$PASSWORD" | corral bootc create my-node \
  --image ghcr.io/tuna-os/marlin:tuna --user tuna --password-stdin
```

You can also give a SHA-512 crypt value with `--password-hash`. Corral rejects
`--password` in plaintext, because other users can read command lines. Use
only one of `--password-prompt`, `--password-stdin`, and `--password-hash`.
The same account flags work with `bootc rebuild`, `bootc upgrade`, and
`bootc switch`. You cannot use `--resume` to add an account to a disk that
is already installed.

#### Testing a bootc image (`corral vmtest`)

`corral vmtest` boots an image locally and tests it. It needs no plugin, and
it is what a CI pipeline should call:

```bash
corral vmtest gate --bootc ghcr.io/tuna-os/yellowfin:latest \
  --user tester --password hunter2 --sudo-user \
  --check 'systemctl --failed --no-legend'
ssh -i corral-vmtest-out/ssh/id_ed25519 -p 2242 tester@127.0.0.1
```

The run leaves the VM up, so the next step tests a real machine over SSH.
It also writes the guest's serial console, a screenshot of every stage of the
boot, an optional WebM timelapse, and `result.json`. Accounts, passwords,
packages and first-boot hooks go into a layer built on top of the image, which
is never modified. Full reference:
[docs/ci-boot-gate.md](ci-boot-gate.md).

---

### 4. Proxmox VE API Compatibility Layer

Corral includes a layer that emulates the REST API of Proxmox VE (`/api2/json/...`). With it, Terraform (`bpg/proxmox`), Ansible, and Proxmoxer tools can manage KubeVirt and Incus instances natively.

```bash
corral plugin install proxmox
corral proxmox serve --addr :8006
```

---

### 5. Plugin Marketplace

Expand Corral with lightweight marketplace plugins:

```bash
# Browse marketplace
corral plugin search

# Installed plugins
corral plugin list

# Plugin management
corral plugin install <name>
corral plugin remove <name>
```

#### Available Plugins:
- `bootc`: Builds VMs from bootable container images.
- `proxmox`: Compatibility server for the REST API of Proxmox VE.
- `backup`: S3/R2 VM disk backup & restore.
- `snapsched`: Automated schedules for VM snapshots, with retention rules.
- `schedule`: VM autostart and shutdown cron windows.
- `gpu`: GPU / PCI passthrough, via discovery of the device plugin.
- `windows`: First-class creation of Windows VMs (UEFI, TPM, virtio drivers).
- `vdi`: Desktop pools for Virtual Desktop Infrastructure (VDI).

#### Plugin screens and widgets

Some plugins add screens and dashboard widgets to the web UI. The Extensions
screen shows what each plugin adds, before and after you install it.

- A plugin widget does not appear on its own. Add it from "Add widget" on the
  Datacenter dashboard.
- A plugin screen opens from "Added by plugins" on the Extensions screen, or
  from the command palette.
- A plugin screen is read-only. It shows information, and it cannot change the
  fleet.
- If a plugin does not answer, the widget or screen tells you why. Click "Try
  again" to ask again.

---

## 4. Diagnostics & Troubleshooting

Run `corral doctor` to diagnose cluster capabilities, hypervisor support, and storage drivers:

```bash
corral doctor
```

In a multi-context setup it diagnoses every configured QEMU, KubeVirt, Incus,
libvirt, and Proxmox target. Use `corral doctor --context NAME` for one target. The
complete capability matrix and the direct-versus-relayed networking behavior
are in [backend-support.md](backend-support.md).

```text
local [qemu]
  ✓ QEMU (local backend)              qemu-system-x86_64 found (probe 2ms)
  ✓ KVM acceleration                  /dev/kvm accessible
  ✓ qemu-img                          found — disk create/resize for local VMs

kubevirt [kubevirt]
  ✓ KubeVirt installed                operator + CR present (probe 41ms)
  ✓ CDI installed                     containerized-data-importer present
  ✓ VolumeSnapshotClass               persistent-VM snapshots available

lab [incus]
  ✓ Incus remote reachable            remote API and trust are valid (probe 18ms)
  ✓ Incus VM support                  virtual-machine instances supported
```

[Problems and fixes](troubleshooting.md) explains each check, the common
error messages, and how to collect evidence when a VM does not boot.

## Reproducible screenshots

Documentation captures use the real demo fleet that is built in. They are automated
for both Chromium and the Bubble Tea TUI:

```bash
npm install --prefix e2e
node scripts/capture-docs.mjs docs/screenshots/generated
```

The same command can target the TunaOS site checkout:
`node scripts/capture-docs.mjs ../docs/static/img/screenshots/corral`.

The `Screenshots` workflow runs this script when the web UI, the TUI or the
demo fleet changes, and one time each week. It then opens a PR with the new
images. When the `TUNAOS_DOCS_TOKEN` secret is set, it also opens a PR in
tuna-os/docs. That PR updates the images on the TunaOS site.
