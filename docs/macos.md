# macOS on KubeVirt

`corral macos` owns Sequoia recovery preparation, persistent VM provisioning,
the first local administrator and key-only SSH bootstrap, private Beeper
authentication, focused credential input, and guest profile/bridge installation.
The plugin embeds its guest scripts; no dotfiles checkout is required. It does
not supply a preinstalled OS, bypass Apple 2FA, or establish
that Apple will activate iMessage. Apple's standard macOS licence restricts use
to Apple-branded hardware.

## Build and prerequisites

Build the plugin without replacing the Corral core:

```sh
go build -o "$HOME/.local/share/corral/plugins/corral-macos" ./cmd/corral-macos
corral plugin list
```

Provisioning needs `kubectl`, `virtctl`, Python 3 for the pinned upstream Apple
recovery downloader, `qemu-img`, `dmg2img`, `mcopy` (mtools), and upstream OpenCore
`macserial`. Guest setup and credential input are native Go commands on Linux or
macOS; they need OpenSSH and the Bitwarden CLI, not Python or external scripts.
Unlock Bitwarden through the existing noninteractive workflow. Credentials come
from `BW_SESSION` or the owned private session file, never command arguments.

KubeVirt, CDI, an existing namespace and default filesystem storage are required.
Select an explicit Corral context; the plugin never changes kubectl's current
context or silently rebinds an alias:

```sh
corral context add macos-home --backend kubevirt --context home-cluster
corral macos requirements --context macos-home --namespace corral-vms
```

The VM-local hook requires the `Sidecar` gate, deprecated but still available in
KubeVirt 1.9. `--enable-sidecar` explicitly appends it while preserving other
gates. It does not change host KVM parameters, reboot the node or restart other
VMs.

## Provision

```sh
corral macos provision messages --context macos-home --namespace corral-vms \
  --node home-node --cpu 1 --mem 6Gi --disk 64Gi --enable-sidecar
```

`provision` prepares verified media, opens a kernel-assigned loopback-only CDI
port-forward, uploads the media and closes the tunnel when it exits. It starts
an owned stopped VM by default; use `--start=false` to leave it halted. An
existing VM follows the preservation path without media downloads or a CDI
tunnel. It never restarts a running VMI or reapplies an installed VM's definition.

`prepare` downloads/converts media without changing the cluster. The lower-level
`create` command accepts already prepared media and an operator-managed
`--uploadproxy-url` for incomplete uploads. Fresh provisioning requires `--node`;
pending local-path PVCs select that node before binding. Conflicting selections
or detectable bound-volume hostname mismatches are refused, never moved.

The release-specific cache defaults to `corral/macos/sequoia` beneath the OS user
cache directory (`~/.cache` on Linux, `~/Library/Caches` on macOS). Apple media is
bound to its release/request provenance. Unknown legacy or Sonoma caches are not
silently adopted as Sequoia. The pinned downloader/OpenCore checksums, Apple
chunklist RSA signature and every recovery chunk must pass verification.

Each VM gets a private personalized EFI image with model-compatible serial/MLB,
UUID and ROM. Preserve its identity directory with the installed disks; generating
an identity does not prove Apple will accept it. The immutable source EFI is not
modified. The private clone installs upstream RELEASE builds of **Lilu 1.7.2**
and **VMHide 2.0.0**, verified against pinned SHA-256 hashes. VMHide loads only on
Darwin 24+; neither integration disables SIP or changes security boot arguments.
Dependencies are downloaded directly from upstream for local use, not bundled in
the plugin or published as prebuilt EFI images.

Three DataVolumes remain: `<name>-opencore` (1 GiB), `<name>-recovery` (8 GiB),
and `<name>-system` (default 64 GiB). CDI adds filesystem overhead; persistent EFI
state needs backend-state storage. Local-path volumes and the client cache are
not backups. Interrupted OpenCore uploads require the exact original private
identity/image and the digest bound to the DataVolume before its initial upload.
Missing or changed bindings are refused rather than regenerating the device.

## Install and boot

Use a Corral core with KubeVirt console support (validated with
v0.7.0+36.g76b6dd5), independently of the plugin build:

```sh
corral console kubevirt/home-cluster/corral-vms/messages
corral screenshot kubevirt/home-cluster/corral-vms/messages -o boot.png
```

Choose **macOS Base System** in OpenCore. In Disk Utility, erase only the system
disk as GUID/APFS, never OpenCore or recovery. Recovery downloads the remaining
OS from Apple, so guest networking must work. Keep OpenCore attached afterward.
Install current Sequoia security updates before connecting accounts.

The VM uses q35, persistent managed UEFI with Secure Boot off, SATA disks, VirtIO
networking, USB input and no memory balloon. Its VM-local hook presents an Intel
CPU on AMD and uses standard VGA. KubeVirt's RHEL QEMU omits VMware SVGA and
`isa-applesmc`; SMC emulation comes from the enabled VirtualSMC boot driver.
The hook preserves the absolute USB tablet and adds only a USB keyboard.
It removes the relative USB mouse from older hooks: QEMU otherwise selects
that mouse ahead of the tablet, causing Recovery console clicks to miss.

Start with one vCPU on AMD. Karnataka reached graphical recovery and the
installed Sequoia setup UI with one CPU; two- and four-CPU attempts panicked or
stalled. The cause is not established. Keep that deployment at one vCPU. The
hook preserves one-socket CPU topology, disables CPU hotplug and uses native
PCIe rather than ACPI bridge hotplug, without changing host TSC/MSR settings.

A Running VMI or painted OpenCore picker is not boot proof. Capture the actual
console on failure. Temporary `-v keepsyms=1 debug=0x100` boot arguments can
preserve panic output; remove diagnostic stop-on-panic settings before relying
on unattended recovery. Do not infer a need for global `kvm.ignore_msrs` changes.

## First local account and SSH

After installing macOS, return to **macOS Base System** in OpenCore and open
Utilities → Terminal. Leave that **root Recovery Terminal focused**, select a
plain **US keyboard layout**, and close other VNC viewers. Do not run bootstrap
against Setup Assistant, an installed macOS terminal, or a disk with an existing
local user. The installed System/Data volume names default to `macos` and
`macos - Data`; select their actual names explicitly if different.

```sh
corral macos bootstrap messages --context macos-home -n corral-vms \
  --user beeper --admin-item 'Messages VM local account' \
  --confirm-recovery-shell --confirm-us-layout
```

The selected Bitwarden login must have the same username as `--user` and a
separate VM-only password. Bootstrap creates a dedicated private ed25519 identity
at `CACHE/NAME-ssh/id_ed25519` only if absent; `--identity` selects a private
existing/generated key. It never replaces an identity.

The console receives **only public shell bootstrap and the public SSH key**,
using paced US-layout key events. Its compressed public payload and arguments
must pass SHA256 integrity verification before the consequential script executes;
console character loss fails closed. A minimal guarded System-volume mount
precedes that verification to obtain the installed native checksum/decompression
tools. Ownership and both confirmations are required before any VNC action.
Native Recovery guards must pass before the temporary root SSH daemon starts on
port 2222. Startup can take time; key-only SSH readiness is bounded and checked
before vault access, while changed host keys are immediately fatal.
Root Recovery is verified again over the Kubernetes API-tunneled SSH session.
The installer verifies the mounted
System/Data APFS volume-group identity, installed macOS, encryption state and
blank-account/resumable receipt **before reading Bitwarden**. Ambiguous disks,
FileVault, existing users/UID501/homes, unsafe paths and mismatched receipts are
errors, never permission to reset credentials. SIP and authenticated-root remain
enabled; FileVault is never disabled.

The client generates a native salted password hash, stores retry-safe account
state privately in context/namespace/VM-scoped `guest-bootstrap` cache state, and
streams the account record/key archive through encrypted SSH stdin. Passwords,
hashes and private preflight metadata are not placed in argv, Kubernetes
resources, console input or public logs. No plaintext password file is created.
Client retry state retains the salted account record, not the reversible
automatic-login blob; that blob is regenerated only in memory after vault
verification when explicitly requested. Keep the private cache for retries:
the same UUID/hash is reused, and changed VM identity, credentials, account, key,
APFS group/System/Data UUIDs or automatic-login choices are rejected. Volatile
disk numbers and a VMI restart alone do not invalidate that state.

Recovery retains its host key in root-private `/.corral-recovery/<scope>` on the
HFS Base System volume, separate from the installed guest's private host-key
trust; changed keys are refused. Where required, bootstrap temporarily adds
only a disabled UID75/GID75 `sshd` privilege-separation alias to **Recovery's
`/etc/master.passwd`**, preserving the exact original privately. It does not
change `/etc/passwd`, stock DirectoryService records or installed accounts to
work around Recovery NSS. Recovery also needs its stock `libsandbox.1.dylib`
and `libMatch.1.dylib` inside the SSH privilege-separation jail. Only absent
copies/directories are created, with root-owned safe permissions; existing
paths are verified and preserved. Owned hash receipts guard removal and exact
master-passwd restoration after the owned temporary daemon stops. Sandbox,
privilege separation, SIP and authenticated-root stay enabled.

The native root first-boot job finishes installed key-only SSH configuration and
removes itself after success. Recovery helpers are cleaned up after staging
(also attempted on later failures). Reboot is requested only after verified
completion and guarded cleanup (`--reboot=false` leaves Recovery running).
**Staged is not GUI-login proof**: inspect the installed macOS GUI and verify
SSH after reboot, then apply the guest profile below.

Automatic login is off unless `--auto-login` is explicitly selected. It requires
FileVault already off and writes the standard reversible `/etc/kcpassword`;
anyone with unencrypted disk access can recover that VM-only credential.
Apple sign-in, trusted-device approval, 2FA and native privacy permissions
remain interactive.

## Private account input

For an already configured guest, Setup Assistant can still be completed
manually with a dedicated local GUI account. Decline optional Siri, analytics,
Screen Time and iCloud Photos/Drive sync; defer Apple sign-in until Messages.
`setup` never changes FileVault and only enables automatic login on explicit
opt-in.

Inspect the guest control, verify the US input layout, focus the intended field,
and close other VNC viewers before invoking:

```sh
corral macos apple-field messages --context macos-home -n corral-vms \
  --item 'Apple login' --field username --confirm-focused-field --confirm-us-layout
```

Use `--field password` for the other field and `--enter` only when intended.
The command verifies the plugin-owned running VMI and establishes the API-tunneled
RFB connection before reading the vault. It rejects an entire unsupported field
before sending keys, explicitly handles Shift and paces USB reports. Ordinary
browser text typing can lose uppercase/symbols. Credentials never enter argv,
Kubernetes resources, logs or temporary files. GUI focus, Apple trusted-device
approval and 2FA remain interactive; authentication is not retried or assumed.

## Guest profile and Beeper

Bootstrap provisions key-only Remote Login and the client's dedicated SSH key.
For a manually configured guest, enable and authorize them first. The default
private key is `CACHE/NAME-ssh/id_ed25519`; `--identity` selects another key.
The local account's Bitwarden login must have the same username as `--user`.

```sh
corral macos setup messages --context macos-home -n corral-vms --user beeper \
  --admin-item 'Messages VM local account' --beeper-item 'Native Beeper login'
corral macos setup messages --context macos-home -n corral-vms --user beeper --check
```

`setup` verifies VMI ownership, pins the SSH host key in private context/namespace/
name-scoped state, and checks the macOS virtual GUI peer before reading the local
admin credential. Changed SSH host keys are refused. The profile prevents system
sleep and turns off root-volume Spotlight indexing, leaving push, identity,
Contacts, keychain, trust and security-update services intact. It refuses Linux
and physical Macs. With VMHide masking the hypervisor flag, only an actual active
`AppleVirtIOPCITransport` IORegistry object is accepted as alternate VM evidence.

`--auto-login` explicitly configures the selected local account for unattended
GUI login. FileVault must already be off; the plugin never disables encryption
or replaces another account's existing automatic-login policy. It writes the
standard root-owned, mode-0600 `/etc/kcpassword` and loginwindow preference using
the matching local credential over encrypted stdin, never command arguments.
The credential is **reversibly obfuscated, not encrypted**: anyone who can read
the unencrypted guest disk can recover it. Use a separate VM-only password,
never an Apple password, and prove unattended GUI login after a guest reboot.
The opt-in policy also prevents idle display sleep and sets the selected user's
screen-saver idle time to zero, so the UI-driven connector does not become stuck
behind an idle lock screen. It does not remove password protection from a
manually locked session or unlock an already locked session.
`--check` only reports these policies, including when combined with `--auto-login`.
Format reference: [dockur first-boot account provisioning](https://github.com/dockur/macos/blob/master/src/install/admin/create.py).

First setup prepares pinned official `bbctl` v0.15.0 authentication privately,
then streams its native config over SSH. `corral macos auth --item 'Native Beeper
login'` can prepare it separately without installing/connecting a bridge. Existing
local or guest authentication remains authoritative: selecting another item does
not silently switch accounts. Stale auth is an error, not permission to replace
it. Raw `whoami` JSON identity is validated because `bbctl` can exit zero on API
errors. Passwords/config travel only through encrypted stdin or a private child
environment; authentication output is never published. `--check` does not read
the vault, consume auth input, download binaries or apply configuration.

The dedicated guest state is `~/Library/Application Support/corral-macos`; its
user LaunchAgent is `io.github.tuna-os.corral.imessage`. It uses
`bbctl run --param imessage_platform=mac sh-imessage`, not the SIP-disabled
Barcelona connector, and leaves unrelated bridge state and Apple services alone.
It starts at GUI login; FileVault may require manual unlock after a restart.
The pinned `mautrix-imessage` binary links `libolm.3.dylib` by absolute
Homebrew path, which clean guests lack and Homebrew no longer ships; the agent
sets `DYLD_LIBRARY_PATH` to `bbctl`'s own downloaded copy under the prod data
binaries directory, so no sudo install or system path is needed.

Sign into **Messages only**, grant Full Disk Access to the exact launcher and
native connector paths printed by setup, and approve Automation/Contacts prompts.
Do not disable SIP or ADP. Verify inbound **and** outbound messages in Beeper;
a running LaunchAgent is not a delivery test. Private logs remain in the
restricted guest state directory and must not be published.

The VM's NIC `macAddress` is pinned to the OpenCore `ROM` from `identity.json`.
If en0 differs from `ROM`, iMessage still activates and self-messages work, but
Apple's device attestation fails (`Failed to baa sign message`) and every
lookup of another person returns `IDSPeerIDManagerErrorDomain Code=3`, so
nothing reaches other people. VMs created before this pin need the same
`macAddress` patched into their spec and a VM restart. Failed lookups are cached
for up to 24 hours per address.

References: [Beeper self-hosting](https://developers.beeper.com/bridges/self-hosting/),
[native Mac connector](https://docs.mau.fi/bridges/go/imessage/mac/setup.html),
[VMHide](https://github.com/Carnations-Botanica/VMHide),
[Apple security releases](https://support.apple.com/en-us/100100).

## Console automation

`scripts/macos-console/driver.py` drives guest consoles without an agent
watching every step: screenshot + tesseract OCR + VNC input, every action
verified (clicks land where OCR says the target is, typing is read back,
waits poll until text appears/vanishes). It encodes the hard-won limits:
VNC release is unreliable (press-hold + verify), META/Command never
arrives via VNC keysyms, F-keys arrive as media keys (never script
Ctrl+F7), Tab/Space need Full Keyboard Access in the guest. Product flows
(Beeper sign-in, bridge verification) live outside this repo and call the
driver; see the beeper-imessage automation for the first consumer.
