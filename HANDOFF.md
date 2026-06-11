# Corral handoff — 2026-06-11

> Project: **Corral** (`corral` binary, `github.com/hanthor/corral`, public repo)
> Live: `https://corral.manatee-basking.ts.net` (cluster deployment on bihar)
> Docs: [README.md](README.md), [SPEC.md](SPEC.md), [WEBUI-PLAN.md](WEBUI-PLAN.md),
> [docs/api.md](docs/api.md), [docs/architecture.md](docs/architecture.md),
> [docs/kubevirt-proxmox-setup.md](docs/kubevirt-proxmox-setup.md)

## Status: shipped

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

**Catalog + import** — curated OS images (8 containerdisks from
quay.io/containerdisks), CDI import for arbitrary qcow2/raw/ISO URLs.

**Advanced KubeVirt ops** — live/offline CPU/RAM scaling, disk hotplug,
online PVC expansion, snapshots (create/list/restore/delete), clone,
templates, guest-agent info, events, metrics, VM export (disk download).

**CLI** — `corral create/list/start/stop/ssh/viewer/logs/info/delete` + all
KubeVirt ops (`restart`, `pause`, `migrate`, `scale`, `adddisk`, `rmdisk`,
`snapshot`) + `config` + `doctor` + `images` + `plugin` + `web` + `completion`.

**TUI** — Bubble Tea app with scrollable actions menu (Start, Stop, SSH,
VNC, Delete).

**Doctor** — cluster diagnostics (`/api/doctor` + `corral doctor`), checks
KubeVirt, CDI, namespaces, storage, feature gates. Auto-fix for fixable
issues.

**Plugin system** — krew-style extensions (marketplace at
`marketplace/index.json`), web UI Extensions tab. Five plugins ship from CI:
**bootc** (build a container image into a VM disk on-cluster), **snapsched**
(CronJob snapshots with retention), **schedule** (autostart/shutdown windows),
**gpu** (PCI/vGPU passthrough discovery + attach), **windows** (UEFI/TPM/
virtio-tuned Windows VMs with the virtio-win driver ISO).

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
| `pkg/catalog/catalog.go` | Curated OS image catalog |
| `pkg/kubevirt/client.go` | VM CRUD, SSH, cloud-init, registry |
| `pkg/kubevirt/features.go` | Scale, volumes, snapshots, clone, export, etc. |
| `pkg/kubevirt/bootc_core.go` | Bootc interface seam |
| `pkg/kubevirt/bootc.go` | Bootc implementation (//go:build bootc) |
| `pkg/web/server.go` | HTTP server, VM list/create/action/delete, nodes, tasks, WS bridges |
| `pkg/web/features.go` | All advanced-op handlers |
| `pkg/web/static/index.html` | SPA shell, create dialog, build dialog |
| `pkg/web/static/app.js` | API client, tree, detail panels, create wizard, import, bootc streaming |
| `deploy/corral-web.yaml` | On-cluster deployment manifest |
| `marketplace/index.json` | Plugin registry |

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
- **Bootc is separate binary.** Users must install it via marketplace. The
  on-cluster deployment image doesn't include it (would need `-tags bootc`
  build).
- **The `/api/images` endpoint on the deployed pod returns 404.** The
  running image may be stale — needs a rebuild and rollout. `/api/vms` and
  `/api/capabilities` work correctly.
- **No tests for app.js** (the vanilla-JS SPA has no JS test harness).

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
