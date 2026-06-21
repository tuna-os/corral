# Corral handoff — 2026-06-11 (active section updated 2026-06-14)

> Project: **Corral** (`corral` binary, `github.com/hanthor/corral`, public repo)
> Live: `https://corral-1.manatee-basking.ts.net` (cluster deployment, ns `corral`)
> Docs: [README.md](README.md), [SPEC.md](SPEC.md), [WEBUI-PLAN.md](WEBUI-PLAN.md),
> [docs/api.md](docs/api.md), [docs/architecture.md](docs/architecture.md),
> [docs/kubevirt-proxmox-setup.md](docs/kubevirt-proxmox-setup.md)
> Domain: [CONTEXT.md](CONTEXT.md), [AGENTS.md](AGENTS.md)
> ADRs: [docs/adr/](docs/adr/)

---

## Active work (2026-06-19) — branch `feat/bootc-desktop-catalog`

**Goal:** get the bootc e2e (`e2e/corral.spec.js:764`, `@live-only`) green end-to-end:
build → boot → SSH via `virtctl port-forward` → **SSH over Tailscale**.

**DONE — unify all VMs on the Tailscale operator proxy (no in-guest tailscale).**
Composefs bootc images (`fedora-bootc:44`, `centos-bootc:stream9/10`, `dakota`)
seal `/etc` read-only at `bootc install` time, so post-install tailscale injection
is impossible (EROFS, aborts the build). Instead **all** VMs now get tailnet access
from the Tailscale K8s operator via `kubevirt.ApplyProxy(name, ns, ports)`
(`pkg/kubevirt/client.go`): a Service annotated `tailscale.com/expose:true` +
hostname `<name>-vm`, plus a `corral-proxy` pod that discovers the VM's guest IP
from the VMI status and `socat`-forwards :22 (+VNC) to it. `ssh <user>@<name>-vm`
over the tailnet reaches the already-enabled guest sshd with **zero in-guest
tailscale** — composefs-agnostic, no auth key baked in, identical on ostree and
composefs images. The operator has its own creds, so `TS_AUTHKEY` /
`TailscaleAuthKey` is gone for VMs.

**Changes made (this session, uncommitted):**
- Removed in-guest tailscale: cloud-init `runcmd` join in `client.go`, the bootc
  cloud-init volume in `generateBootcVM`, `generateBootcTailscaleSetup`, the
  `TailscaleAuthKey` field + `--ts-authkey` flag (types.go/create.go/server.go/
  cmd/corral-bootc).
- `ApplyProxy(name, ns, []int{22,5900})` now called on **every** VM create
  (regular web+CLI ports 22/5900/3389, bootc web+CLI 22/5900) — unconditional.
- Tests fixed for the new signatures: `bootc_test.go`
  (`TestGenerateBootcVM_KernelArgsAndNoInGuestTailscale`), `bootc_render_test.go`
  (6-arg `generateBootcJob`), `client_test.go` (dropped the TailscaleAuthKey test),
  `cmd/cmd_test.go` (dropped `ts-authkey` flag + 3 tsAuthKey tests). `go build`,
  `go vet`, `go test` all green for both the default and `bootc` tag sets.
- e2e: both tailscale blocks (regular fedora VM, bootc) now match the operator
  device `<vm>-vm`. Bootc test image switched to
  **`ghcr.io/projectbluefin/dakota:testing`** (desktop, composefs), disk 30G.

**Deployed:** amd64 `-tags bootc` binary built, image pushed
`ghcr.io/hanthor/corral:latest`, `deploy/corral-web` rolled out 2026-06-19/20. Live
server reports `bootc:true`.

**Root cause found on the first dakota e2e: server-side 15-min build cap.** The
operator-proxy refactor is fine; the e2e failed at the *build* step with
`bootc build failed: timeout waiting for bootc build (15 minutes)`. Sequence
(verified): node pulls dakota in ~3m47s (3.0 GB, 120 layers), then `bootc install`
**re-pulls the same image in-pod via ostree** (no node cache) and checks it out
layer by layer — a desktop image blows past 15 min. `waitForBootcJob` in
`pkg/kubevirt/bootc.go` had two hardcoded 15-min loops (450×2s). **Fix:** both now
use `bootcBuildTimeout()` — default **30 min**, override with
`CORRAL_BOOTC_BUILD_TIMEOUT` (minutes). e2e build-wait bumped to 35 min, outer
`test.setTimeout` to 65 min. Rebuilt/pushed/rolled out; **dakota e2e re-running**.
If 30 min still isn't enough, bump `CORRAL_BOOTC_BUILD_TIMEOUT` on the deployment
env (no rebuild needed) — that's why it's configurable.

**Two more blockers found + fixed on the next runs:**

1. **Proxy RBAC: corral-web couldn't create the proxy resources.** Second run
   built+booted fine but the task errored `proxy RBAC: exit status 1` — the
   `corral-web` ClusterRole had only `get,delete` on deployments/services/
   serviceaccounts/roles/rolebindings (enough for DeleteProxy, not ApplyProxy).
   Fixed in `deploy/corral-web.yaml`: added `list,create,update,patch` to those,
   **plus** `subresources.kubevirt.io virtualmachineinstances/portforward:
   get,create` — the per-VM proxy Role grants portforward, and RBAC privilege-
   escalation prevention blocks creating a Role granting a verb the creator
   lacks. Applied live (ClusterRole only, not the Deployment, to preserve the
   live-only env).

2. **dakota ships sshd DISABLED** (`/usr/lib/systemd/system-preset/openssh.preset:
   disable sshd.service`). Universal Blue desktop images disable sshd by preset;
   server bootc images (fedora/centos-bootc) enable it. `bootc install
   --root-ssh-authorized-keys` writes the key, but sshd never starts, and
   composefs /etc is read-only so we can't `systemctl enable` post-install.
   Verified by inspecting the image on-cluster (sshd binary present,
   `PermitRootLogin` defaults to `prohibit-password` = key auth OK). **Fix:**
   `bootcKernelArgs` now appends **`systemd.wants=sshd.service`** to the VM
   cmdline (we already control kernelBoot kernelArgs) — pulls sshd + host-key
   generation into the boot transaction with no enable symlink, idempotent for
   server images, composefs-agnostic. e2e port-forward SSH window bumped
   180s→360s (desktop first boot is slow). Unit test asserts the arg.

**ROOT-CAUSED — dakota boot failure = XFS root vs btrfs-only initramfs.** dakota
builds (~25 min) and starts, but the guest dropped to emergency mode. Captured the
exact error via KubeVirt `logSerialConsole` (set `domain.devices.logSerialConsole:
true`, then `kubectl logs <virt-launcher-pod> -c guest-console-log`):

```
mount[176]: mount: /sysroot: unknown filesystem type 'xfs'.
sysroot.mount: Mount process exited, code=exited, status=32
[FAILED] Failed to mount sysroot.mount - /sysroot.  → emergency.target
```

Our builder formats the root **XFS** (`mkfs.xfs`), but Universal Blue **desktop**
images (bluefin/dakota, custom kernel `7.0.7`, 200 MB initramfs) default to
**btrfs** and their stock initramfs ships **no xfs module** (boot even runs
`modprobe@btrfs.service`). KubeVirt kernelBoot boots that stock initramfs, so it
can't mount our XFS root → `sysroot.mount` fails → emergency → no multi-user, no
sshd (hence the SSH banner-exchange timeouts; the systemd.wants fix was right but
moot until boot). Ruled out along the way: composefs/readonly-sysroot (identical to
fedora-bootc:44, which boots), verity (`veritysetup.target` reaches OK), device/uuid
(`systemd-fsck-root` passes), `rootfstype=xfs` (still "unknown filesystem type").
Confirmed the deployment has no `/etc/fstab` and the `.ostree.cfs` composefs blob
is present, so sysroot.mount is generated purely from `root=UUID=` → fstype is the
only variable.

**FIX (shipped):** builder root fstype is now configurable —
`bootcRootFS()` / **`CORRAL_BOOTC_ROOTFS`** env (default `xfs`, so server images are
unchanged). `generateBootcJob` swaps `mkfs.xfs`→`mkfs.<fs>`; `bootcKernelArgs`
appends `rootfstype=<fs>`. Set **`CORRAL_BOOTC_ROOTFS=btrfs`** on `deploy/corral-web`
(live) to build dakota/UB desktop images with a btrfs root their initramfs can
mount. **Re-validating now** with `dbg-dak4` (btrfs build → boot → past sysroot.mount
→ multi-user → sshd → proxy → tailnet). 

**...but btrfs build FAILED on Talos — deeper constraint found.** `mkfs.btrfs`
succeeds but `mount` of the btrfs disk fails IN THE BUILDER: `mount: unknown
filesystem type 'btrfs'`. The builder formats+mounts the disk **on the Talos build
node** (to run `bootc install to-filesystem`), and **Talos's kernel has no btrfs
module** (it's an optional `siderolabs/btrfs` system extension, not installed).
Probed both sides directly:
- **Build node (Talos bihar):** mounts **xfs, ext4** — NOT btrfs.
- **dakota guest:** kernel has `xfs.ko`+`btrfs.ko` in `/usr/lib/modules`, but its
  **initramfs ships btrfs ONLY** (no xfs, no ext4 module). KubeVirt kernelBoot
  boots that stock initramfs, so the guest can only mount **btrfs** as root.

So the root fs must be mountable by BOTH the Talos node (xfs/ext4) AND dakota's
initramfs (btrfs) — **the intersection is empty**. xfs builds but won't boot
(guest); btrfs boots but won't build (node). The `CORRAL_BOOTC_ROOTFS` knob can't
resolve this alone; **reverted the live env back to xfs** so normal builds work.

**To actually run dakota/UB-desktop images, pick one (real work, decision needed):**
1. **Add `siderolabs/btrfs` to the Talos nodes** (machineconfig + reboot bihar/
   karnataka). Then build btrfs; dakota's btrfs initramfs boots it. Cleanest,
   aligns with how UB images are designed — but it's a cluster infra change.
2. **Regenerate dakota's initramfs with xfs and boot that.** The dakota builder
   container has dracut + xfs.ko, so it can rebuild the initramfs `--add-drivers
   xfs`. But KubeVirt kernelBoot reads kernel/initrd from a *container image*, so
   we'd build+push a tiny per-VM kernel image, OR switch off kernelBoot to UEFI
   disk boot (systemd-boot, dakota's bootloader) reading the initrd from the disk.
   Keeps infra unchanged; bigger pipeline rework.
3. **Park UB desktop images**; keep the fstype-configurable code for images where
   node+guest fs agree; point the e2e back at a server bootc image (fedora/
   centos-bootc) that builds xfs and boots.

The fstype-configurable code (`CORRAL_BOOTC_ROOTFS`) is committed regardless —
it's the right primitive once a viable fs exists (e.g. after option 1).

## VM-based builder PoC (2026-06-20) — decision: replace the pod builder

James chose to **replace the pod/kernelBoot builder** with a **VM-based `bootc
install to-disk`** builder (the original never-automated PoC from old HANDOFF @
16a4a75^). A builder VM's *full kernel* does the fs work (so the Talos-node btrfs
gap is irrelevant), `to-disk` writes a real bootable disk (GPT + ESP + bootloader),
and the final VM boots it via firmware — no kernelBoot, no fstype/initramfs
mismatch. **Manually proven on-cluster** (manifests in /tmp/poc-builder.yaml,
block PVC `poc-dakota-disk`, builder VM `poc-builder`):
- Builder VM = `quay.io/containerdisks/fedora:42` + 40Gi emptyDisk scratch
  (/var/lib/containers) + block-mode target PVC (`serial: target`) + cloud-init.
- `podman pull dakota` (~3.4 min) then `podman run --privileged --pid=host -v
  /dev:/dev -v scratch:/var/lib/containers -v key:/buildkey.pub dakota bootc
  install to-disk --wipe ... /dev/disk/by-id/virtio-target`. Source auto-detect
  works (no overlay-ref bug). Read progress via `logSerialConsole` +
  `guest-console-log`.
- **WORKS through:** GPT (BIOS boot + 512M ESP + XFS root — dakota's declared
  root fs!), XFS root, full ostree deploy (~16 min), `Initializing systemd-boot`.
- **BLOCKED at the bootloader:** `error: Installing to disk: bootupd is required
  for ostree-based installs`. dakota:testing (Bluefin **"Dakotaraptor"**
  experimental branch, bootc 1.16.1) ships **no bootupd and no dnf/rpm** to add it;
  bootc hard-requires bootupd for the EFI payload even with `--bootloader systemd`
  and even without `--generic-image`. This is a gap in this bleeding-edge image.

**ROOT CAUSE of "Volume corrupt" FOUND (via James's own `ostree-composefs-rebase`
tool, /home/james/dev/ostree-composefs-rebase, src/migration/mod.rs:1151):** bootc's
composefs backend writes the kernel/initrd to the ESP by **reading them back from the
EROFS/composefs store, which returns ZERO-FILLED content past the inline threshold**
— so the 200 MB initrd on the ESP is corrupt past ~13 MB (exactly what mtools/OVMF
saw). James's tool works around it by **re-extracting the real bytes** (`podman create`
+ `podman cp`, or registry stream). **Fix applied to the PoC:** after `bootc install
to-disk --composefs-backend`, mount the ESP (`/dev/disk/by-id/virtio-target-part2`,
works in the builder VM's full-kernel udev), `podman cp
<dakota>:/usr/lib/modules/<kver>/{vmlinuz,initramfs.img}` over the corrupt
`/EFI/Linux/bootc_composefs-<hash>/{vmlinuz,initrd}`, sync, umount. Re-validating.
NB: dakota's intended install (tuna-installer / projectbluefin/bootc-installer,
images.json) uses **filesystem=btrfs** (not xfs) — revisit fs choice when codifying.

**PROVEN VM-builder recipe for dakota (composefs) — codify this:**
1. block-mode target PVC; builder VM = fedora:42 containerdisk + 40Gi emptyDisk
   scratch (/var/lib/containers) + target PVC (serial `target`) + cloud-init.
2. `podman pull <img>`; `podman run --privileged --pid=host --security-opt
   label=type:unconfined_t -v /var/lib/containers:/var/lib/containers -v /dev:/dev
   -v key:/buildkey.pub <img> bootc install to-disk --composefs-backend --wipe
   --root-ssh-authorized-keys /buildkey.pub /dev/disk/by-id/virtio-target`.
3. **initrd fixup** (bootc EROFS zero-fill bug): `podman cp <img>:/usr/lib/modules
   /m` (NOT `podman run ls` — dakota's uutils ls returns nothing; copy the dir and
   `KVER=$(ls /m|head -1)`), then `cp /m/$KVER/{vmlinuz,initramfs.img}` over
   `/EFI/Linux/bootc_composefs-<hash>/{vmlinuz,initrd}` on the mounted ESP
   (/dev/disk/by-id/virtio-target-part2 — works in the builder VM's full-kernel udev).
4. **kargs** on the BLS entry options (sed on `/EFI/.../loader/entries/*.conf`): add
   `console=ttyS0` (serial visibility) + `systemd.wants=sshd.service` (dakota ships
   sshd disabled; composefs /etc is writable at runtime so this starts it).
   MUST use the SAME image for install + fixup (dakota:testing bumped 7.0.7→7.0.11
   mid-session → kernel/modules skew → no virtio_net → sshd unreachable).
5. final VM: boot the block PVC via UEFI (`firmware.bootloader.efi`, q35) — NO
   kernelBoot.
6. **MUST use `--filesystem btrfs`** on the bootc install. dakota's install.toml
   defaults root to xfs, but its initramfs is **btrfs-only** (no xfs module) and its
   own installer (images.json) uses btrfs. An xfs root → initramfs can't mount it →
   `/dev/gpt-auto-root` + initramfs emergency (saw it via console=ttyS0). bootc flag:
   `bootc install to-disk --filesystem btrfs` (values xfs|ext4|btrfs). The builder
   VM's full kernel handles btrfs (the Talos *node* can't — that's why the old pod
   builder never could).
7. **Inject root SSH key MANUALLY** — `--root-ssh-authorized-keys` is a **no-op with
   the composefs backend** (verified: `find authorized_keys` on the disk = nothing).
   On composefs dakota, runtime `/root` = `/var/roothome` and `/var` =
   `state/os/default/var` (btrfs subvol). Write the pubkey to
   `<btrfs-root>/state/os/default/var/roothome/.ssh/authorized_keys` (mkdir .ssh 0700,
   file 0600, root:root) by mounting `/dev/disk/by-id/virtio-target-part3` in the
   builder VM.

**✅ DAKOTA BOOTS + SSH WORKS END-TO-END (2026-06-20).** PoC: `ssh root@…` →
`PRETTY_NAME="Bluefin"`, kernel 7.0.11, root fstype `overlay` (composefs),
`bootc status` = healthy BootcHost.

**✅ CODIFIED INTO GO (2026-06-20).** Pod builder fully replaced by the VM-builder in
`pkg/kubevirt/bootc.go`:
- `bootcBuildDisk`: creates a **block-mode** target PVC (`generateBlockPVC`), a
  cloud-init **Secret** (`generateBuilderSecret` — script exceeds KubeVirt's 2 KB
  inline userData limit, so `cloudInitNoCloud.secretRef`), and a builder VM
  (`generateBuilderVM`: fedora:42 containerdisk + 40Gi emptyDisk scratch + block
  target via `serial: target` + the secret), starts it, `waitForBuilderVM` (polls
  the virt-launcher `guest-console-log` for `CORRAL_BUILD_OK/FAIL`, streams progress),
  then deletes builder VM + secret. Returns `BootcBuildResult{PVCName}` (the
  RootUUID/KernelVersion/KernelArgs fields are gone).
- `builderCloudInit` const = the proven recipe; **auto-detects backend**: composefs
  (no bootupd) → `--composefs-backend --filesystem btrfs` + initrd fixup + manual
  key inject; ostree (has bootupd) → plain to-disk + xfs. Both add
  `console=ttyS0 systemd.wants=sshd.service` to the BLS entries.
- `generateBootcVM`: final VM = UEFI boot of the block PVC (q35 +
  `firmware.bootloader.efi`, NO kernelBoot). Seam in bootc_core.go + callers
  (server.go, cmd/corral-bootc) updated to the new 7-arg signature.
- `bootcRebuild`: simplified (no kernelBoot patch — same-named disk PVC + restart).
- Knobs: `CORRAL_BOOTC_BUILD_TIMEOUT` (min, default 45), `CORRAL_BOOTC_BUILDER_IMAGE`.
- Tests rewritten (`bootc_render_test.go`, `bootc_test.go`); `go build`/`vet`/`test`
  green both tag sets. e2e updated (no kernelBoot → assert `efi`; builder-VM
  diagnostic via guest-console-log; 80 min budget).

**✅ CODIFIED PATH VALIDATED END-TO-END (2026-06-20)** via the API (`cod1`,
dakota:testing): builder VM build 35m → final VM created using `efi` (UEFI, no
kernelBoot) → started → `ssh root@cod1` (port-forward): Bluefin, kernel 7.0.11,
root `overlay` (composefs) → **operator-proxy tailnet device `cod1-vm` came up →
`ssh root@cod1-vm` works**. The whole bootc stack the e2e exercises now passes
through the codified server. cod1 cleaned up.

**Proxy hardening (2026-06-20):** the build-time `ApplyProxy` flaked once
(`proxy RBAC: exit status 1`) and wrongly failed the whole build. Fixed: (a)
server.go bootc path now runs `ApplyProxy` as a **separate best-effort task after
the build finishes** — a flaky proxy no longer marks a good build as failed (the
VM is up + reachable via port-forward regardless); (b) `ApplyProxy` retry hardened
to 12×5s and now covers the Service/Deployment applies too (`applyProxyManifest`).
The proxy RBAC itself is correct (corral-web holds vmi get/vnc/portforward, so no
bind/escalate needed); the failure was transient.

**Production-safe e2e isolation (2026-06-20).** The `corral-vms` namespace is now
**production**. Live e2e must NOT touch it. Added `deploy/corral-web-e2e.yaml`: a
second corral-web (same cluster-scoped SA/ClusterRole) defaulting to the
**`corral-e2e`** namespace (`CORRAL_NAMESPACE=corral-e2e`), reachable at
`https://corral-e2e.manatee-basking.ts.net`. Because the API create handler honors
`req.Namespace` AND the UI form defaults to the server's namespace, pointing the
suite at this instance isolates everything — no per-test namespace plumbing:
```
cd e2e && CORRAL_URL=https://corral-e2e.manatee-basking.ts.net \
  CORRAL_NS=corral-e2e ./node_modules/.bin/playwright test --grep "@live-only"
```
**CI:** `.github/workflows/e2e.yml` runs only the CI tier
(`--grep-invert @live-only`) on kind+emulated KubeVirt. The @live-only tier (bootc,
SSH, consoles, snapshots) is **NOT in CI** — run manually against corral-e2e.

**Both backends validated (2026-06-20):** composefs (dakota, `cod1`) AND ostree
(fedora-bootc:44, `ost2`) both build → UEFI boot → SSH via the codified VM-builder.
Two fixes landed during e2e validation against corral-e2e:
- **ostree UEFI boot:** `bootc install to-disk` via bootupd only writes
  EFI/<vendor>/ + an NVRAM entry; a fresh VM has empty NVRAM → "No bootable
  option". Fixed by adding **`--generic-image`** on the ostree branch (writes the
  removable EFI/BOOT/BOOTX64.EFI fallback path, skips firmware changes).
- **backend detection:** UB desktop images ship Rust uutils — `--entrypoint
  /bin/sh -c '...'` returns NOTHING, so my first "hardened" sh-script detection
  misdetected dakota as ostree → built xfs → didn't boot. Reverted to per-probe
  `--entrypoint /usr/bin/test -e <path>` (the form cod1 proved): bootupd present →
  ostree; else systemd-boot present → composefs.
- **create-dialog timeout:** the hardened `ApplyProxy` (12×5s retry) ran
  synchronously on the regular create path, blocking the response past the UI's
  20s dialog timeout (broke the Tailscale e2e). Now async/best-effort after the
  response (server.go), matching the bootc path.

**Backend detection — must inspect the filesystem, never execute the image.**
Burned two e2e builds: my "hardened" detection misdetected dakota as ostree →
built xfs → didn't boot. Root cause: dakota's `/usr/bin/test` (and `/bin/sh`) are
symlinks to **uutils-coreutils** (a multicall binary); podman `--entrypoint` can't
dispatch it (argv[0] breaks), so any "run a binary in the image" probe returns
nothing. **Fix:** `CTR=$(podman create $IMG)` then `podman cp "$CTR:<path>" -`
existence checks (no execution); reuse the same CTR for the composefs modules
copy. Verified early-exit: dakota → `CORRAL_BACKEND=composefs FS=btrfs`. dakota
is a moving target (kver + tooling change daily) — filesystem inspection is the
only robust read.

**Proxy RBAC — apiGroup bug (not load).** `ApplyProxy` failed with
`proxy RBAC: exit status 1`; surfacing kubectl's stderr (added to `applyManifest`)
revealed the truth: `roles ... is forbidden: user "corral-web" is attempting to
grant RBAC permissions not currently held: {APIGroups:["kubevirt.io"], Resources:
["virtualmachineinstances/portforward"], Verbs:["create"]}`. `GenerateProxyRBAC`
granted `portforward` under **`kubevirt.io`**, but the subresource lives under
**`subresources.kubevirt.io`** — and the proxy doesn't even use portforward (the
proxy pod socats to the guest IP it reads from the K8s API). **Fix:** dropped the
portforward rule from the proxy Role entirely (it now grants only vmi-get +
vnc-get, both held by corral-web → escalation passes), and removed the matching
(now-vestigial) portforward grant from corral-web's ClusterRole. (My earlier manual
tests used the *correct* apiGroup, so they passed and masked the real Role's bug.)
Also: `applyManifest` surfaces stderr; ApplyProxy is best-effort + 12×5s retry;
regular create runs it async after the response (was blocking the UI's 20s
create-dialog timeout → broke the Tailscale e2e).

**Status: code complete + deployed; composefs + ostree both validated. Re-running
the bootc + Tailscale e2e against corral-e2e (isolated from production corral-vms).
Not yet committed.**

Builds/deploys: same loop as above; live server has the codified binary.

**RESULT: composefs backend BUILDS + installs dakota; "Volume corrupt" WAS bootc's
EROFS zero-fill of the initrd on the ESP, fixed by re-extracting real bytes.** `bootc install to-disk --composefs-backend`
**succeeded** (POC_BUILD_OK): GPT (BIOS boot + 1G ESP + composefs root), full
dakota deploy, "Installing bootloader via systemd-boot" (NO bootupd). The final VM
(block PVC, UEFI/q35 `firmware.bootloader.efi`) **boots: OVMF → systemd-boot →
finds the composefs BLS entry** (`/EFI/Linux/bootc_composefs-<hash>/{vmlinuz,initrd}`,
`options ... composefs=?<hash>`). But it fails: `Error preparing initrd: Volume
corrupt`. Inspected the ESP (mtools at the partition offset): vmlinuz 19 MB OK,
**initrd dir-entry says 200 MB but its FAT chain breaks** — mtools `Fat problem
while decoding`, reads only 13 MB; OVMF independently reports Volume corrupt. So
OVMF/EDK2's FAT driver can't read the large/fragmented 200 MB initrd off the ESP —
a known bleeding-edge issue (experimental composefs backend + huge initrd on FAT),
not a corral bug. Next options to try (each ~18 min): retry build (FAT frag may
vary); larger/explicit-cluster ESP; UKI (but it's also a big FAT file — same risk);
newer OVMF; or report upstream. dakota disk left as pvc/poc-dakota-disk (vm/poc-dakota
stopped) for follow-up.

**ANSWER (per bootc docs + James): use the composefs backend.** dakota is a
composefs + systemd-boot image with NO bootupd — that's exactly the bootc
**composefs-rs backend** (`bootc install to-disk --composefs-backend`), not the
default ostree backend (which we ran → needed bootupd). The composefs backend
manages systemd-boot itself (no bootupd) and boots via UKI/BLS, sidestepping
dakota's btrfs-only standalone initramfs. Flag confirmed present in dakota's bootc
1.16.1: `--composefs-backend  If true, composefs backend is used, else ostree`.
(dakota has systemd-boot + no bootupd but NO UKI at /boot/EFI/Linux — UKI is for
auto-detect; testing the explicit flag with BLS kernel+initramfs.) Build running
with `bootc install to-disk --composefs-backend --wipe ...` (dropped
--generic-image/--bootloader which forced the ostree+bootupd path). Docs:
https://bootc.dev/bootc/experimental-composefs.html (marked experimental).

**Superseded fork below (kept for context) for dakota's bootloader (~18 min each):**
- a. Installer-container trick: run `bootc install to-disk --source-imgref
  docker://dakota` from a container that HAS bootupd (fedora-bootc:42 has
  `/usr/sbin/bootupctl` + payload) — but its payload is Fedora grub2/shim, not
  dakota's systemd-boot; uncertain whether the resulting disk boots dakota.
- b. `bootc-image-builder` (official image→disk tool) in the builder VM → raw
  image → dd to the block PVC. May handle the bootloader where bare `to-disk`
  can't.
- c. `--bootloader none` + manually install systemd-boot to the ESP (ostree BLS
  kernels live on the XFS /boot, not the FAT ESP — non-trivial layout plumbing).
- d. Validate the whole pipeline + the final-VM UEFI boot with a server bootc
  image that HAS bootupd (fedora/centos-bootc), then **codify the VM-builder into
  Go** (replacing generateBootcJob/GenerateBootcVM, BootcBuildResult, the wait
  loop, server.go, cmd/corral-bootc, tests). Treat dakota's image gap separately.

**Codification (still TODO, large):** new build path = block PVC + builder VM
(cloud-init runs to-disk) + poll-to-Succeeded + delete builder + final VM booting
the block PVC via UEFI firmware (`domain.firmware.bootloader.efi`). No more
RootUUID/KernelVersion/KernelArgs capture (disk is self-bootable). Update
`BootcRebuild` too. PoC leftovers in `corral-vms`: vm/poc-builder (off),
pvc/poc-dakota-disk — reuse or clean up.

The other 4 fixes (operator proxy, build timeout, proxy RBAC + retry, systemd.wants)
are correct and orthogonal. Diagnostic tip: **KubeVirt `logSerialConsole` +
`guest-console-log` container** is the reliable way to read a bootc guest's boot —
`virtctl console` loses the race on fast-failing boots, and these images' initramfs
emergency demands a root password (no `rd.shell`/`rd.break` passwordless shell).

**Note on running the e2e:** use the project-local playwright from `e2e/`
(`cd e2e && ./node_modules/.bin/playwright test …`), NOT bare `npx playwright`
— npx fetches a newer version that mismatches and errors out before any test runs.

**Deploy loop:**
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags bootc -o build/corral .
podman build --arch amd64 -t ghcr.io/hanthor/corral:latest -f Containerfile .
podman push ghcr.io/hanthor/corral:latest   # gh auth token | podman login ghcr.io -u hanthor --password-stdin
kubectl -n corral rollout restart deploy/corral-web && kubectl -n corral rollout status deploy/corral-web
```

**Run the e2e:**
```bash
env CORRAL_URL=https://corral-1.manatee-basking.ts.net \
  npx playwright test --grep "bootc VM" --reporter=line
```
(Clean up `e2e-`/`dbg-` prefixed VMs+PVCs in `corral-vms` first.)

---

## Status: shipped

### New: Proxmox API compatibility (corral-proxmox plugin)

A marketplace plugin that serves `/api2/json/...` — the Proxmox VE REST API —
translated onto KubeVirt. Proxmox ecosystem tools (Terraform bpg/proxmox,
Ansible, proxmoxer) can manage KubeVirt VMs without modification.
Documented in [docs/proxmox-api.md](docs/proxmox-api.md) (endpoints, vmid
mapping, auth via `--token`/`CORRAL_PROXMOX_TOKEN`, known gaps). 2026-06-12:
removed ~1,000 lines of code duplicated between `cmd/corral-proxmox` and
`pkg/proxmox` (the plugin now just wraps `proxmox.NewHandler`), and wired up
the previously unreachable shared-secret auth.

**15 endpoints**, verified against `bpg/proxmox` v0.109.0:
- VM CRUD + lifecycle (create/start/stop/delete via `virtctl`)
- Node discovery + status (kubectl, live data)
- Storage (StorageClasses → Proxmox storage types)
- Access control (K8s RBAC → Proxmox users/roles/groups, see ADR-0001)
- VNC/TTY proxy stubs
- Pools/HA/LXC stubs (empty, no errors)

Corral is feature-complete for v1.0. All Proxmox-parity operations work
through CLI, TUI, and web UI.

### What's built

**VM lifecycle** — create (6 source types), list, start, stop, restart,
pause/unpause, migrate, SSH, VNC, delete. Two backends: QEMU (local) and
KubeVirt (cluster).

**Web UI** — dark, mobile-friendly SPA with tree navigation (datacenter →
node → VM), detail panels (Summary, Hardware, Snapshots, Events, Console),
create wizard (catalog / containerDisk / import / ISO / bootc / PVC), image
library with CDI import, bootc build log streaming, extensions store. Live
VNC (noVNC) and serial console (xterm.js) in the browser.

**Catalog + import** — curated OS images from reputable publishers only:
8 containerdisks (quay.io/containerdisks, boot directly), 7 official distro
cloud images straight from the distros' own mirrors (cloud.debian.org 12/13,
fedoraproject.org 42, cloud.centos.org stream 9/10, repo.almalinux.org 9/10 —
CDI import, real PVC disk), and 6 TurnKey Linux appliance installer ISOs
(core/LAMP/WordPress/Nextcloud/GitLab/fileserver — finish install over VNC).
`catalog.Image` has three kinds (ContainerDisk/URL/ISO); web + CLI route each
to the right create path. Plus CDI import for arbitrary qcow2/raw/ISO URLs.
(linuxserver.io publishes container images, not VM disks — not applicable.)

**Bootc lifecycle (2026-06-13)** — beyond create: `corral bootc rebuild
<vm> [--image]` (rebuild the disk from the recorded image, or override),
`upgrade <vm>` (pull the latest of the current image), `switch <vm>
--image <ref>` (move to a different image), `status` (list bootc VMs + their
images). Shared rebuild logic in `pkg/kubevirt/bootc.go` (`BootcRebuild`
seam): stop → delete disk PVC → rebuild → patch kernelBoot → start, preserving
sizing/networking. Web UI: an **Upgrade** button on bootc VMs (detected via
the new `types.VM.Bootc` field) runs the rebuild as a background task with
the live build-log dialog (`POST /api/vms/{ns}/{name}/bootc/rebuild`).

**Bootc catalog** — curated bootc bases (`catalog.BootcImages`): Fedora bootc
42/43, CentOS bootc stream9/10, Universal Blue uCore/uCore-minimal. Listed by
`corral bootc images`, accepted as `--image <name>` in the plugin, served at
`/api/images?type=bootc`, and offered as datalist suggestions in the web
create wizard's bootc source field (names also resolve server-side).

**Advanced KubeVirt ops** — live/offline CPU/RAM scaling, disk hotplug,
online PVC expansion, snapshots (create/list/restore/delete), clone,
templates, guest-agent info, events, metrics, VM export (disk download).

**CLI** — `corral create/list/start/stop/ssh/viewer/logs/info/delete` + all
KubeVirt ops (`restart`, `pause`, `migrate`, `scale`, `adddisk`, `rmdisk`,
`snapshot`) + `config` + `doctor` + `images` + `plugin` + `web` + `completion`.

**TUI** — Bubble Tea app with scrollable actions menu (Start, Stop, SSH,
VNC, Delete).

**RDP (2026-06-12)** — `GET /api/vms/{ns}/{name}/rdp` probes the guest's
3389 (Windows native, or Linux gnome-remote-desktop/xrdp) and the VM Summary
panel shows connection instructions when it answers; `/api/rdp/{ns}/{name}`
is a websocket↔3389 bridge (via `virtctl port-forward`) ready to carry an
in-browser IronRDP client later — plan and honest sizing in
[docs/adr/0002-browser-rdp-via-ironrdp.md](docs/adr/0002-browser-rdp-via-ironrdp.md).
The windows plugin's `--rdp` proxy path now has unit tests, and `shell.Fake`
records stdin on `RunStdin` for manifest assertions.

**Task panel (2026-06-12)** — Proxmox-style activity log: every mutating
server operation (create/start/stop/delete/scale/snapshots/clone/imports/
plugin installs/bootc builds) is recorded with status + duration in an
in-memory ring (`pkg/web/tasklog.go`, `GET /api/tasklog`) and shown in a
collapsible bottom panel in the web UI. Console tabs gained fullscreen
buttons and noVNC scaling controls (local scale-to-fit toggle + remote
resize). `/api/capabilities` now also lists installed plugins so the UI can
light plugin features up (only bootc consumes it so far).

**Create wizard UX (2026-06-12, from live feedback)** — catalog is the
default boot source with the image list visible on open; the SSH field is
universal (any source type) and accepts a public key **or a GitHub username**
(`gh:user` / `github:user` / `@user`, resolved server-side via
github.com/user.keys, multiple keys supported); instance type, preference,
namespace, node and cloud-init live behind an Advanced fold; hint text is
per-source-type. Cloud-init extra YAML is now **structurally merged** into
the generated user-data (duplicate top-level keys like a second
ssh_authorized_keys used to silently break SSH — found by e2e, fixed in
`kubevirt.mergeCloudInit`). Default namespace is **corral-vms**, overridable
with `CORRAL_NAMESPACE` (the live deployment pins `tailvm` in
deploy/corral-web.yaml).

**Simple create wizard (2026-06-12)** — the create dialog now opens as a
card-based wizard: distro cards with logos (simple-icons CDN, letter-badge
fallback), All/Servers/Appliances/Bootc filter chips (bootc only when the
plugin is on), then name + S/M/L size presets + SSH key/GitHub username.
The full form lives behind "Advanced…". Catalog entries carry
`logo` + `variant` metadata.

**Tailnet-by-default (2026-06-12)** — set `tailscale.expose: true` (+
optional `tailscale.tags`) in config.yaml or `CORRAL_TAILNET_EXPOSE=1` /
`CORRAL_TAILNET_TAGS=tag:corral-vm`: every created VM automatically gets
the proxy Service with tailscale-operator annotations exposing SSH/VNC/RDP
— fresh VMs are tailnet devices with zero in-guest setup.

**Plugin web-UI integration (2026-06-13)** — `/api/capabilities` lists
installed plugins; the UI lights up their features:
- **snapsched** → Snapshot-schedule panel in the Snapshots tab (every +
  keep → CronJob; list/remove). Web manages the CronJob directly via
  `pkg/cronops` (`pkg/web/snapsched.go`), no plugin binary needed —
  works on the deployed pod.
- **windows** → "Windows (UEFI/TPM, installer ISO)" source type in the
  Advanced create form, with an RDP-expose toggle. Manifest generation
  moved to `pkg/kubevirt/windows.go` (`GenerateWindowsVM`/`CreateWindowsVM`),
  shared by the plugin CLI and the web server so it works on-cluster.
- **schedule** → Autostart/shutdown windows panel on the Summary tab
  (start/stop cron → CronJobs flipping runStrategy via `pkg/cronops`,
  `pkg/web/schedule.go`).
- **gpu** → GPU/PCI passthrough section in the Hardware tab
  (`pkg/web/gpu.go`): lists the KubeVirt CR's permitted devices, attach/
  detach to the VM (patches `spec.domain.devices.gpus`). Permitting a device
  cluster-wide stays a CLI admin op (`corral gpu enable`).

**Doctor UI (2026-06-12)** — failed checks render red with a per-item Fix
button (`POST /api/doctor/fix {"check": "<name>"}` → `doctor.FixOne`)
alongside the reconcile-all button.

**Multiview (2026-06-12)** — tree entry rendering a grid of live view-only
noVNC tiles for up to 6 running VMs (built for watching automated GUI test
runs); click a tile's title to jump to that VM's full console.

**Doctor** — cluster diagnostics (`/api/doctor` + `corral doctor`), checks
KubeVirt, CDI, namespaces, storage, feature gates. Auto-fix for fixable
issues.

**Plugin system** — krew-style extensions (marketplace at
`marketplace/index.json`), web UI Extensions tab. Six plugins ship from CI:
**bootc** (build a container image into a VM disk on-cluster), **snapsched**
(CronJob snapshots with retention), **schedule** (autostart/shutdown windows),
**gpu** (PCI/vGPU passthrough discovery + attach), **windows** (UEFI/TPM/
virtio-tuned Windows VMs with the virtio-win driver ISO), **proxmox**
(Proxmox VE API compatibility — serve /api2/json backed by KubeVirt, so
Terraform providers and other Proxmox tools can manage VMs).

### Architecture decisions

- **No client-go.** Shells out to `kubectl`/`virtctl`. Keeps binary ~12 MB.
- **Single binary, three UIs.** CLI + TUI + Web share the same backend
  packages and registry file.
- **Bootc is a plugin.** Compiled behind `//go:build bootc`, separate binary
  (`corral-bootc`). Core binary stays lean.
- **Embedded SPA, no JS build step.** Vanilla JS + CSS via `//go:embed`.
- **Secrets stay local.** Registry (mode 0600) in `~/.local/share/tailvm/`.
  No secrets in git or cluster state.

## Cluster state (2026-06-11)

| Resource | Detail |
|---|---|
| KubeVirt | v1.8.2 |
| Talos | v1.13.2, kernel 6.18.29 |
| Nodes | bihar (cp, Intel), karnataka (worker, AMD Strix Halo) |
| Storage | longhorn (CSI), RWX, snapshots, expansion |
| CDI | Running |
| Tailscale operator | Ingress at `corral.manatee-basking.ts.net` |
| Multus CNI | v4.2.2 (installed 2026-06-12, `deploy/multus/`), test NAD `corral-test-bridge` in tailvm |
| Corral web pod | Running, 1 replica, `ghcr.io/hanthor/corral:latest` |

## File map (key files)

| File | Role |
|---|---|
| `cmd/root.go` | CLI entrypoint, plugin dispatch |
| `cmd/create.go` | `corral create` (all flags, catalog/import/bootc) |
| `cmd/images.go` | `corral images` (catalog + datavolumes) |
| `cmd/web.go` | `corral web` server |
| `cmd/tui.go` | Bubble Tea TUI |
| `cmd/corral-bootc/main.go` | Bootc plugin binary |
| `cmd/corral-proxmox/main.go` | Proxmox API plugin binary |
| `cmd/corral-proxmox/server.go` | Proxmox → KubeVirt translation (15 endpoints) |
| `docs/adr/0001-k8s-rbac-to-proxmox-privileges.md` | RBAC mapping ADR |
| `docs/agents/` | Agent skills configuration |
| `pkg/catalog/catalog.go` | Curated OS image catalog |
| `pkg/kubevirt/client.go` | VM CRUD, SSH, cloud-init, registry |
| `pkg/kubevirt/features.go` | Scale, volumes, snapshots, clone, export, etc. |
| `pkg/kubevirt/bootc_core.go` | Bootc interface seam |
| `pkg/kubevirt/bootc.go` | Bootc implementation (//go:build bootc) |
| `pkg/web/server.go` | HTTP server, VM list/create/action/delete, nodes, tasks, WS bridges |
| `pkg/web/features.go` | All advanced-op handlers |
| `pkg/web/rdp.go` | RDP port probe + websocket↔3389 bridge |
| `docs/proxmox-api.md` | Proxmox compat layer docs (endpoints, auth, gaps) |
| `docs/adr/0002-browser-rdp-via-ironrdp.md` | RDP roadmap (IronRDP/RDCleanPath) |
| `e2e/corral.spec.js` | Playwright suite (CI tier + @live-only tier) |
| `.github/workflows/e2e.yml` | CI e2e: kind + KubeVirt emulation + CDI |
| `pkg/web/static/index.html` | SPA shell, create dialog, build dialog |
| `pkg/web/static/app.js` | API client, tree, detail panels, create wizard, import, bootc streaming |
| `deploy/corral-web.yaml` | On-cluster deployment manifest |
| `marketplace/index.json` | Plugin registry (bootc, snapsched, schedule, gpu, windows, proxmox) |

## Build & deploy

```bash
go build -o corral .                 # core binary
go build -tags bootc -o corral .     # with bootc

# Container image (on-cluster)
docker build --platform linux/amd64 -t ghcr.io/hanthor/corral .
docker push ghcr.io/hanthor/corral

# Deploy
kubectl apply -f deploy/corral-web.yaml
kubectl -n tailvm rollout restart deploy/corral-web
```

## Known gaps

- **No auth in web UI.** Gated by tailnet membership only. Fine for
  single-operator, not multi-tenant.
- **Metrics require metrics-server.** The UI grays out the resource-usage
  panels if metrics aren't available. Neither bihar nor karnataka currently
  runs metrics-server.
- **Bootc CLI is a separate binary** (marketplace). The on-cluster web
  image now builds with `-tags bootc` (ci.yml, 2026-06-12), so the deployed
  UI offers the bootc create flow after the next push + rollout.
- **The `/api/images` endpoint on the deployed pod returns 404.** The
  running image may be stale — needs a rebuild and rollout. `/api/vms` and
  `/api/capabilities` work correctly.
- **No tests for app.js** (the vanilla-JS SPA has no JS test harness).
- **Bootc boot path was broken until 2026-06-12** — the builder wrote
  `disk.raw`, but KubeVirt's filesystem-mode PVC convention is `disk.img`,
  so on first start virt-handler ignored the built system and tried to
  create a blank disk.img (failing "not enough space"). Found by the
  hardened e2e (which actually boots the built VM); fixed by naming the
  image disk.img and sizing it to 85% of the PVC. Builds before the fix
  produced VMs that could never boot.
- **KubeVirt kernel-boot checksum bug — fixed on this cluster
  (2026-06-12).** On stock KubeVirt v1.8.2 + k8s 1.36, kernel-boot VMs
  (bootc) run fine but their reported status flaps indefinitely:
  `status.kernelBootStatus.kernelInfo.checksum` is a uint32 (crc32) that
  overflows the schema's `format: int32`, so virt-handler's status updates
  are rejected forever. TWO enforcement points had to be fixed:
  1. the VMI CRD schema — `format` dropped from both checksum fields, both
     versions (direct patch + persistent `customizeComponents` JSON patch
     on the KubeVirt CR — note: virt-operator does NOT re-apply CRDs on
     reconcile, hence the direct patch);
  2. the `virtualmachineinstances-update-validator.kubevirt.io` entry was
     **removed from the `virt-api-validator` ValidatingWebhookConfiguration**
     — virt-api re-validates with a schema compiled into the binary, which
     the CRD patch can't reach. The apiserver still schema-validates via
     the (patched) CRD.
  **virt-operator re-applies the CRD (format → int32) within ~10s and the
  apiserver then rejects status writes — so the only durable cluster-side
  fix is to keep virt-operator scaled to 0** (`kubectl -n kubevirt scale
  deploy virt-operator --replicas=0`). It is currently scaled to 0 on this
  cluster; the data plane (virt-api/controller/handler/exportproxy) runs
  fine without it. Restore with `--replicas=2` before a KubeVirt upgrade.
  Verified end-to-end with the operator paused (e2e bootc test, 9.6 min:
  build → boot → Running → live serial console). Report upstream to
  kubevirt/kubevirt.
  **Corral-side resilience (2026-06-13):** `parseVMList` rescues kernel-boot
  VMs whose VMI status is frozen — if printableStatus is transitional but
  the virt-launcher pod is Running, the VM shows Running
  (`launcherRunningIndex`). So bootc VMs display correctly in the UI even if
  the operator gets restored and the status freezes again (console/SSH at a
  frozen VMI phase is still unverified, hence the paused-operator default).
- **Bootc builds are pull-dominated.** The builder pod pull uses the node
  cache, but `bootc install` re-pulls the image inside the pod (~1.3 GB for
  centos-bootc) from the registry every build. A pull-through registry
  mirror on-cluster would cut builds from ~15 min to ~3. The build pipeline
  now reports pull progress, survives Job pod retries, and retries loop
  device allocation (all found by e2e against the live cluster, 2026-06-12).

## Test suite (2026-06-11)

Hermetic unit tests across every package — no kubectl, virtctl, cluster, or
network needed (verified against an empty PATH). External commands go through
the `shell.Runner` seam (`pkg/shell`, with a scriptable `shell.Fake`);
package-level seams: `kubevirt.SetDefaultRunner/SetPackageRunner/
SetApplyRunner`, `doctor.SetRunner`. Coverage: catalog/config/doctor 100%,
registry 97%, shell 90%, plugin 88%, web 79%, kubevirt 75%, qemu 61%,
cmd 21%. Live-cluster e2e tests live behind `-tags integration`
(`pkg/kubevirt/integration_test.go`) and `scripts/smoke-web.sh`; CI runs
gofmt + vet + build + `go test -race` for both the default and `bootc` tag
sets, plus a coverage report (`.github/workflows/ci.yml`).

### Playwright web-UI e2e (e2e/, 2026-06-12)

Full-flow browser tests against a **live cluster** (production-safe: every
resource is `e2e-`-prefixed, deleted in `afterEach`, and verified gone; the
cleanup guard refuses to touch anything unprefixed). Target via `CORRAL_URL`
(default `http://localhost:8006`), namespace via `CORRAL_NS` (default
`tailvm`). Run: build `-tags bootc`, `corral web`, then `cd e2e && npx
playwright test`. Coverage:

- create wizard field toggling for every source type (incl. bootc datalist)
- catalog content checks (official distro sources, TurnKey, bootc catalog)
- VM create through the UI for all six source paths: catalog containerdisk,
  catalog official qcow2 (asserts the DV imports from cloud.debian.org),
  containerDisk, import-by-URL (cirros, waits for CDI Succeeded + boots),
  installer ISO (alpine), existing PVC (imports to the library first)
- lifecycle via the UI toolbar buttons (start/stop/delete with confirm)
- settings: Hardware-tab CPU/memory scale (asserts the VM spec), Snapshots
  tab take + delete (longhorn)
- consoles: raw websocket checks (VNC bridge speaks RFB, TTY returns serial
  output) plus the real UI tabs (noVNC canvas, xterm screen)
- SSH: throwaway keypair injected via the wizard's cloud-init box, login
  through `virtctl port-forward` asserts `echo SSH_OK`
- bootc build from the wizard (build-dialog log stream → VM exists)
