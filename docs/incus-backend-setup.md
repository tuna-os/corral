# Incus backend setup

> Related: [libvirt setup](libvirt-backend-setup.md),
> [backend support matrix](backend-support.md),
> [problems and fixes](troubleshooting.md), [user guide](user-guide.md)

Corral uses the `incus` command-line client to manage Incus instances. Corral
has no Incus client library and no certificates of its own. It uses the
remotes, trust and socket access that your `incus` client already has. If
`incus list REMOTE:` works for your user, Corral can use that remote.

Corral sends each call to an explicit `REMOTE:name`. It never runs
`incus remote switch`, so Corral and your Incus client can use different
default remotes.

## What you get

| Capability | Incus support in Corral |
|---|---|
| Inventory in `corral list`, the TUI and the web UI | yes. VMs show in `corral list`; containers show in `corral ct list` |
| Start, stop, delete | yes (CLI, TUI, web) |
| Restart | TUI and web. The CLI `corral restart` does not support Incus today ([#489](https://github.com/tuna-os/corral/issues/489)) |
| Create | yes, from an Incus image (`images:ubuntu/24.04` and similar) |
| Shell | `corral ssh` and the web terminal run `incus exec`, not network SSH |
| Snapshots | TUI only today, see [backend support](backend-support.md#incus-and-libvirt-snapshots-migration-and-hotplug) |
| Cold move to or from another backend | yes, `corral move` |
| Live migration, CPU/RAM change, disk or NIC hotplug | no, not yet |
| Browser VNC | no, the web UI gives a terminal |

## 1. Prerequisites

- A host with Incus installed and initialized. For a new local host:

  ```bash
  sudo incus admin init --minimal
  ```

- Hardware virtualization (`/dev/kvm`) on the Incus server, if you want
  virtual machines. Containers work without it.
- Access to the Incus socket for your user. Distributions use one of two
  groups. The `incus-admin` group gives full access. The `incus` group gives
  restricted access with a project for each user. Add yourself to the group
  that your distribution documents, then log out and log in again:

  ```bash
  sudo usermod -aG incus-admin "$USER"
  ```

  Do not run Corral with `sudo` to get around a permission error.
- The `corral` binary. See the [README](../README.md#install).

## 2. Make sure that Incus works without Corral

```bash
incus remote list
incus query local:/1.0
incus info local:
```

For a remote server, add the remote with the Incus client first. Corral does
not add remotes or trust for you:

```bash
incus remote add lab https://lab.example.net:8443
incus query lab:/1.0
```

## 3. Add a Corral context for the remote

A context is a named connection that Corral aggregates. You can let Corral
find the local daemon, or add a context by hand.

Scan this host and save what works:

```bash
corral setup
```

`corral setup` always probes the `local` remote. If the daemon answers with
virtual machine support, setup offers to save a context with the name `incus`.

Add a context for a named remote by hand:

```bash
corral context add lab --backend incus --context lab
corral context list
```

The `--context` value is the Incus remote name. If you leave it empty, Corral
uses its configured remote (see below).

To make Incus the destination of `corral create` commands that do not name a
backend:

```bash
corral config set-default-backend incus
```

### How Corral selects the remote

For commands that do not resolve an existing instance, Corral selects the
remote in this order:

1. The global `--context` flag. A context name selects the remote of that
   context. With `--backend incus`, any other value is the remote name.
2. The `CORRAL_INCUS_REMOTE` environment variable.
3. The remote of the default context, when `corral context use NAME` selected
   an Incus context.
4. The `incus.remote` key in `~/.config/corral/config.yaml`. The
   `corral incus set-remote NAME` command writes this key.
5. The `local` remote.

Commands that act on an existing instance find the remote from the inventory.
For example, `corral start web` goes to the remote where `web` is.

## 4. Verify

```bash
corral doctor --context lab
corral list
```

`corral doctor` runs these checks for each Incus context:

| Check | What it does | If it fails |
|---|---|---|
| `Incus CLI` | Looks for `incus` on `PATH` | Install the Incus client |
| `Incus remote reachable` | Runs `incus query REMOTE:/1.0` | Read the error text. Look at `incus remote list` and the trust for that remote |
| `Incus VM support` | Looks for VM support in `incus info REMOTE:` | Enable KVM on the server, or use Incus only for containers |
| `Incus storage and network` | Reads the server configuration | Look at the storage pools and managed networks of the remote |

`corral doctor --fix` does not change Incus. All Incus fixes are manual.

## 5. Create and use instances

`corral create` with `--incus`, or with an Incus context, makes a
**virtual machine**:

```bash
corral create web --incus --image images:ubuntu/24.04 --cpu 2 --mem 4GiB
corral create web2 --context lab --image images:debian/12 --mem 2GiB
```

- `--image` takes Incus image syntax (`REMOTE:alias`). It does not take the
  names from `corral images`. The default is `images:ubuntu/24.04`.
- Corral refuses `--iso`, `--qcow`, `--import`, `--pvc` and
  `--container-disk` for Incus.
- Corral sends `--cpu` as `limits.cpu` and `--mem` as `limits.memory`. It
  changes the `Gi` and `Mi` suffixes to `GiB` and `MiB`, and sends other
  values without a change. The default `--mem` value is `4G`. If Incus
  refuses a value, give a unit such as `4GiB`.

The `corral incus` commands talk to Incus directly. `corral incus create`
makes a **container** unless you add `--vm`:

```bash
corral incus create tools --image images:ubuntu/24.04
corral incus create builder --image images:ubuntu/24.04 --vm --cpu 2 --mem 2GiB
corral incus --remote lab list
```

Lifecycle commands are the same as on the other backends:

```bash
corral start web
corral ssh web
corral ssh web -c 'uname -a'
corral stop web
corral delete web
```

`corral ssh` runs `incus exec REMOTE:NAME -- bash`. It ignores `--user`,
`--identity`, `--port`, `--password` and `-L`. The guest must have `bash`.
An Incus VM also needs the Incus agent in the guest.

In the web UI, the console of an Incus instance is a terminal that also runs
`incus exec`. The web UI has no VNC display for Incus.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `corral setup` shows `incus is not installed` | Install the Incus client, then run `corral setup` again |
| `corral setup` shows `incus is installed but the local daemon did not answer` | The daemon is down or your user cannot open the socket. Run `incus query local:/1.0` and read its error. For `permission denied`, add yourself to the Incus group and log in again |
| `incus daemon answers but did not advertise VM/QEMU support` | The server cannot run VMs, usually because KVM is not available. Containers still work |
| `corral list` prints `warning: context lab unavailable: ...` | Corral could not list that remote. The rest of the fleet still shows. Run `corral doctor --context lab` |
| `the Incus backend creates from an Incus image; use --image ...` | You gave an ISO, qcow2 or PVC source. Incus creates only from an Incus image |
| `incus launch failed: ...` | Incus refused the create. The text after the colon comes from Incus, for example an unknown image or a bad memory value |
| `snapshot is only supported for KubeVirt VMs` | The `corral snapshot` CLI is KubeVirt-only today. Use the TUI Snapshots view ([#491](https://github.com/tuna-os/corral/issues/491)) |
| `corral ct create --backend incus` puts the container on the wrong remote | This command uses the default remote of your Incus client. Run `incus remote switch` first, or use `corral incus --remote NAME create` |

For more symptoms, see [problems and fixes](troubleshooting.md).
