# Corral Roadmap

**Last updated**: 2026-10-19 | **Maintainer**: tuna-os (hanthor) / strategist agent

---

## Mission

Corral herds VMs — and containers — into your tailnet. Corral is a single Go binary
with a CLI, TUI, and web dashboard. It manages VMs across nine backends. Five
are Tier 1 or 2 (qemu, kubevirt, incus, libvirt, proxmox). Four are Tier 3
(vsphere, nutanix, xen, virtualbox). It exposes every guest over the Tailscale
network that your devices already share.

Developers also use it to boot-gate tunaOS
images on a KubeVirt cluster (`scripts/boot-gate.sh` in tunaOS).

---

## Current Status (2026-10-19)

- Corral is a Go rewrite of the legacy Python `tailvm`. On-disk names keep
  the `tailvm-` prefix during the transition ([SPEC.md](SPEC.md)).
- Corral supports nine backends. The `bootc` mode builds a disk from a
  bootable container on the cluster. Then it runs that disk as a KubeVirt VM.
- ✅ **tunaOS CI uses Corral as its boot gate.** tunaOS#3249 replaced
  `scripts/iso-e2e.sh` with `corral vmtest` v0.7.0 (#431).
- ✅ **v0.7.0 is out** (2026-10-15): signed binaries with SBOMs,
  and Homebrew is on v0.7.0 (#339, #210).
- The repo has no milestones. This file and the issue labels track the work.

### Priorities

| Priority | Item | Tracking | Status |
|----------|------|----------|--------|
| P0 | Launch: Show HN and announcements (drafts in `docs/blog/`) | #423, #424, #425, #426 | 🟡 Drafts merged (#611); awaiting maintainer decision + timing |
| P1 | Architecture tech-debt refactoring | #602, #603, #604, #605, #607, #614 | 🟡 Findings filed; split & decoupling underway |
| P3 | Dependency hygiene | #97 | 🟢 Ongoing (Renovate) |

---

## Roadmap (dependency order)

Phases 1 to 3 are done (see the Done list). The launch remains. Each
launch item has a draft in `docs/blog/` and a `ready-for-human` label.

### Phase 4: Launch and outreach

The git history is clean (#264), v0.7.0 is public (#339), and tunaOS uses
the gate (#431).

| Item | Issue | Depends on | Status / Checkpoint |
|------|-------|------------|---------------------|
| Show HN | #423 | — | 🟡 Ready; awaiting maintainer approval + timing |
| bootc case study | #426 | — | 🟡 Ready; awaiting maintainer approval |
| VDI launch announcement | #425 | — | 🟡 Ready; awaiting maintainer approval |
| KubeVirt ecosystem positioning | #424 | — | 🟡 Ready; awaiting maintainer approval |

### Open after Phase 3

- Run the manual pass for the Tier 3 backends against real systems
  ([runbook](docs/runbooks/enterprise-backends-manual-pass.md)). Until then
  VMware, Nutanix, XenServer/XCP-ng and VirtualBox stay Tier 3.
- VDI surfaces without an end-to-end test stay Preview: the `corral vdi`
  CLI, ephemeral and container pools, USB redirection and device checks.

---

## Done in 2026 Q3–Q4

- Backend parity for `move`: #163, #164, #123.
- Install path and macOS binaries: #315, #316.
- Web workspace epic #350: all child issues are done.
- Vendored asset provenance and SRI checks: #215, #396, #504.
- Path traversal in local VM names: #497, #502.
- Incus and libvirt commands, contexts and snapshots, and Proxmox create: #489–#493.
- Config cache and cross-process lock: #359.
- Enterprise readiness criteria
  ([docs/enterprise-readiness.md](docs/enterprise-readiness.md)): #404.
- Test backfill (RFC-0004), coverage floor 59.5% to 69.0%: #506. It
  fixed two bugs. Gc stopped a VM with the same name in another
  namespace (#538). VM stop could kill an unrelated process (#540).
- Split `pkg/web/server.go` into routes, request middleware, task tracker,
  and create-target files, with characterization tests first: #503
  (#529, #559, #563).
- VDI pools of ephemeral CT desktops
  ([docs/vdi-ct-pools.md](docs/vdi-ct-pools.md)): #230.
- Signed plugins with SBOMs, verified at install time: #505.
- Signed core binaries and container image, with SBOMs: #584.
- Self-service API for VDI: opt-in `/api/vdi/` routes in `corral web`
  ([ADR-0013](docs/adr/0013-vdi-broker-placement.md)): #227.
- TUI state machine: #305 (#511, #549, #567, #571, #575, #580). Tests
  for each screen transition came first. Each screen has a typed mode, a
  key handler, and a view. Backend work runs in commands, not in `Update`.
- Structured logs with `log/slog` and request IDs, from the web request to
  the backend commands: #419 (#578, #579).
- Backend-specific code goes through `pkg/backend` adapters: #214
  (#550, #521, #524, #553, #562, #564, #552, #576).
  27 branches on a backend name remain. Each has a reason in
  [docs/backend-parity.md](docs/backend-parity.md), and the ratchet in
  `pkg/backend/matrix_test.go` holds the count.
- VDI session records and conservative reclaim (disconnect grace, maximum
  duration, ephemeral VM pools): #228.

- VDI one-click connect: the web Desktops screen and `corral vdi connect`:
  #229.
- VDI GA: the broker on persistent VM pools is Stable on KubeVirt. CI tests
  it end to end, and an upgrade test reads v0.6.0 state: #69 (#592, #593).
- Enterprise backends (RFC-0003): VirtualBox, vSphere, XenServer/XCP-ng and
  Nutanix, and CLI commands through the adapters: #507 (#586–#591, #594–#596).
- TUI models in their own files (`cmd/tui.go` 1,496 to 590 lines): #554.
- v0.7.0 released with signed binaries and SBOMs: #339, #210.
- tunaOS CI boot gate on `corral vmtest` (tunaOS#3249): #431.
- `e2e-kind` is a required check. Its path filter is a job, so PRs that
  skip the end-to-end test do not block.
- Old build files removed from the git history (33 MB to 12 MB): #264.

---

## Technical Debt Backlog
 
| Item | Issue | Priority | Effort |
|------|-------|----------|--------|
| VM test harness package split (image build, layer composition, boot orchestration) | #604, #607 | P1 | L |
| HTTP handler refactor away from package-global state | #614 | P1 | M |
| `cmd/create.go` decoupling (Lima parsing, bootc, backend provisioning) | #602 | P2 | M |
| TUI Update loop state machine & dispatch decoupling | #603 | P2 | M |
| Web client `app.js` poll loop decoupling (state refresh, view routing, lifecycle) | #605 | P2 | S |
| `pkg/proxmox` (compat server) vs `pkg/proxmoxbe` (client) naming confusion | CONTEXT.md | P2 | S |
| Legacy `tailvm` prefix migration completion | SPEC.md | P2 | M |

## How to Contribute

Pick an item from the earliest phase that has open work. Earlier phases
hold all of its dependencies. The labels `needs-triage`, `ready-for-agent` and
`ready-for-human` show the state of each issue (see
`docs/agents/issue-tracker.md`).

## Roadmap Governance

The strategist agent keeps this file current after each milestone and each
quarter. To change it, open a PR that refers to an issue.

**Currency rule**: when an issue in this file closes, the same PR must move
its row, or the row must name a successor issue.
