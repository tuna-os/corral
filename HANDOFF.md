# tailvm bootc handoff — 2026-06-10

> **Update (later 2026-06-10):** project renamed to **Corral** (`corral`
> binary; on-disk/cluster `tailvm` identifiers kept for compat). Remaining-work
> items addressed: sshd is now enabled in the bootc deployment by the builder
> Job; kernel paths are auto-detected (no more hardcoded el10 version); builder
> logs stream live; the pod is selected by `job-name` label. Also fixed:
> Tailscale auth key now actually injected into cloud-init, proxy script
> jq-based IP parsing (python3 was never installed), EnsureNamespace dry-run
> no-op, QEMU SSH hostfwd, logs command, delete confirmation + registry
> cleanup, registry 0600. See SPEC.md for the full design.

## What was built

### SSH (complete)
- `tailvm ssh <name>` CLI command with `-u`/`-i`/`-c`/`-p`/`--password` flags
- Auto password lookup from registry (cloud-init passwords stored at create time)
- KubeVirt backend: delegates to `virtctl ssh` (native K8s tunnel + key auth)
- QEMU backend: raw `ssh` to VM's Tailscale IP
- Both backends accept `sshpass` for password-based auth
- TUI: scrollable actions menu with SSH option (quits TUI, launches session)

### SSH key injection (complete)
- At `tailvm create` time, reads `~/.ssh/id_ed25519.pub` (falls back to `id_rsa.pub`, `id_ecdsa.pub`)
- Injects key into KubeVirt VM cloud-init (`ssh_authorized_keys`)
- Password stored in `~/.local/share/tailvm/registry.json` for later retrieval
- `tailvm ssh` auto-looks up the stored password if `--password` isn't specified

### TUI (complete)
- Scrollable selectable actions menu (replaced old key-binding panel `s`/`x`/`v`/`e`/`d`)
- Actions: ▶ Start, ■ Stop, 🔑 SSH, 🖵 Viewer (VNC), ✎ Edit ports, ✕ Delete
- Arrow key navigation, Enter to select, Esc to go back

### Config (complete)
- `tailvm config` reads `~/.config/tailvm/config.yaml` or `$TS_AUTHKEY` env var
- Tailscale auth key support for VM Tailnet setup
- Ansible dotfiles already fetch key from Bitwarden (`tailscale-apikey` item)

### bootc disk builder (code complete, blocked by infra)

**What works**: `tailvm create --bootc <uri>` creates a PVC and runs a KubeVirt Job
that uses `bootc install to-filesystem` on a raw XFS loopback device. The OS deploys
correctly (64 layers, ~14 seconds), SSH keys are injected, root UUID is captured.

**What's blocked**: The booted VM has no bootloader because Talos kernel 6.18.29
can't re-read loopback partition tables. SeaBIOS (the default KubeVirt firmware) can't
boot a raw disk without GRUB.

**Nested VM approach (proven, not automated)**:

```
VM 1 (builder): Fedora 40 cloud image
  ├─ 30Gi PVC: docker storage
  ├─ 50Gi PVC: target disk (/dev/vdc)
  ├─ docker pull ghcr.io/tuna-os/yellowfin:gnome          ✅
  ├─ docker save → podman load (6.7G tar)                 ✅
  ├─ podman run bootc install to-disk /dev/vdc             ✅
  │   ├─ GPT partitions: BIOS boot + ESP + root            ✅
  │   ├─ XFS root + FAT ESP                                ✅
  │   ├─ 64 layers deployed in 15 seconds                  ✅
  │   ├─ SSH keys injected                                 ✅
  │   ├─ GRUB bootloader installed                         ✅
  │   └─ Installation complete                             ✅
  └─ systemctl --root=/mnt enable sshd (to be automated)   pending
─────────────────────────────────────────────────────────────────
VM 2 (final): Boots from PVC
  ├─ SeaBIOS → GRUB → kernel 6.12.0-233                   ✅
  ├─ Guest agent connected                                 ✅
  └─ SSHD needs enablement                                 🔴
```

### Remaining work

1. **Enable sshd on bootc disk** — after `bootc install to-disk`, mount `/dev/vdc3`,
   run `systemctl --root=/mnt enable sshd`.  10 minutes of work.

2. **Fix image transport to bootc** — bootc running inside a podman container can't
   auto-detect the source image due to a containers/image library bug with overlay
   storage reference format. The fix: either:
   - Use `--source-imgref=docker://registry` with a working local registry
     (musl-based `registry:2` crashes with RELRO error on this kernel)
   - Use `--source-imgref=docker-archive:/image.tar` (docker save → tar file)
     (blocked by 30Gi PVC being too small for both docker layers + 6.7G tar)
   - Use `--source-imgref=oci:/oci:gnome` with skopeo export
     (blocked by `/var/tmp` on 5GB root disk filling up)
   **Solution**: mount-bind `/var/tmp` to the 30Gi PVC, then skopeo or docker-save works.
   The bind-mount was done but skopeo/docker-save still ran out because 30Gi was tight.
   Use a 50Gi export PVC or resize the target PVC.

3. **Automate the builder VM** — codify the Fedora builder VM creation, docker setup,
   bootc install, sshd enablement, and final VM creation into `tailvm create --bootc`.

4. **TUI progress indicator** — Charm.sh Bubble Tea spinner/progress bar during the
   bootc build (which takes 2-5 minutes).

### Files changed (7 files, ~400 lines net)

| File | Change |
|------|--------|
| `pkg/kubevirt/bootc.go` | **New** — `BootcBuildDisk()`, `GenerateBootcVM()`, Job manifest, waitForBootcJob |
| `pkg/kubevirt/client.go` | `SSH()` with `virtctl ssh`, `LoadSSHPublicKey()`, cloud-init key injection |
| `pkg/qemu/qemu.go` | `SSH()` with sshpass, `readMetadata()` helper |
| `pkg/types/types.go` | `Password` on `RegistryEntry`, `SSHPublicKey` on `CreateOpts` |
| `pkg/config/config.go` | **New** — `~/.config/tailvm/config.yaml` reader |
| `cmd/commands.go` | `ssh` command with flags, `resolveNamespace()` cross-ns fix |
| `cmd/create.go` | `--bootc` flag, ssh key injection at create time |
| `cmd/config.go` | **New** — `tailvm config` command |
| `cmd/tui.go` | Scrollable actions menu, SSH action, post-quit hook |
| `cmd/root.go` | Post-quit action runner |
| `cmd/list.go` | `resolveBackend()` cross-namespace fix |

### Cluster state

| Resource | Detail |
|----------|--------|
| KubeVirt | v1.8.2 |
| Talos | v1.13.2, kernel 6.18.29 |
| Nodes | bihar (cp), karnataka (worker, AMD GPU) |
| Storage | local-path (rancher.io), WaitForFirstConsumer |
| CDI | Running |
| Privileged pods | Allowed (PodSecurity warning only) |
| Loopback partition support | ❌ Kernel can't re-read partition tables on loopback |
| Registry:2 (musl) | ❌ Crashes with RELRO protection error |
