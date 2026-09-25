# Corral Roadmap

**Last updated**: 2026-09-25 | **Maintainer**: tuna-os (hanthor) / architect agent

---

## Mission

Corral herds VMs — and containers — into your tailnet. A single Go binary with
a CLI, TUI, and web dashboard, it manages VMs across five backends (qemu,
kubevirt, incus, libvirt, proxmox) and exposes every guest over the Tailscale
network your devices already share. It is also the org's CI boot-gate tool:
`reusable-build-image.yml` on `tunaos` main installs Corral and gates ISO
builds on `corral create gate --bootc ... --wait-ssh`.

---

## Current Status (2026-09-25)

- Go rewrite of the legacy Python `tailvm`; on-disk/`tailvm-` prefixes retained
  during the transition ([SPEC.md](SPEC.md)).
- Five backends supported; `bootc` creation mode builds a bootable-container
  disk on-cluster and runs it as a KubeVirt VM.
- ✅ **Backend move parity closed 2026-08-11**: the Proxmox export adapter
  (#163) and Incus image publishing (#164) both landed, and the Incus backend
  gained end-to-end coverage (#123). `move` is no longer the weakest surface —
  see [docs/backend-parity.md](docs/backend-parity.md).
- ✅ **Distribution and release infrastructure repaired (September 2026)**:
  - macOS release binary builds published and installer checksum verification added (#315).
  - Release workflow updated to stamp release tags properly without crowding product releases under plugin artifacts (#316).
- ✅ **Org CI adoption shipped**: tunaos#1273 was closed 2026-09-02 as superseded —
  hanthor landed the adoption directly on `tunaos` main instead of rebasing that
  PR. `reusable-build-image.yml` installs Corral (with a release-binary
  fallback) and gates on `sudo -E corral create gate --bootc "$IMAGE"
  --wait-ssh --timeout 900`; `tests/corral/verify.yaml` is the canonical
  scenario. This roadmap tracked the PR for three weeks after it closed —
  see the currency rule below.
- ✅ **`--json` output shipped**: #205 closed 2026-09-02, completed.
  `rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", ...)` in
  `cmd/root.go` makes it a global flag, not per-command.
- **No milestones on the repo**; work is tracked through issues and labels
  (`needs-triage`, `ready-for-agent`, …) plus this file.

### Priorities

| Priority | Item | Tracking | Status |
|----------|------|----------|--------|
| P0 | Fix advertised install path & macOS release publishing | #210, #315, #316 | ✅ Done |
| P0 | tunaOS boot-gate adoption | tunaos#1273 (closed, superseded) | ✅ Done — shipped on tunaos main 09-02 |
| P1 | VDI plugin epic — Windows/Linux desktop pools | #69, [docs/vdi-epic-status.md](docs/vdi-epic-status.md) | 🟡 In progress |
| P2 | Programmatic output — `--json` across commands | #205 (closed) | ✅ Done — global flag in `cmd/root.go` |
| P2 | Stable plugin API contract + marketplace schema v2 | [docs/plugin-marketplace.md](docs/plugin-marketplace.md) | 🟡 In progress |
| P3 | Dependency dashboard / renovate hygiene | #97 | 🔴 Open |

---

## Quarterly Goals

### Current Quarter (2026 Q3 — July–September)

**Theme**: Backend parity and release front-door repair.

| Goal | Owner | Tracking | Status |
|------|-------|----------|--------|
| Proxmox export adapter (move source parity) | architect | #163 | ✅ Done — closed 08-11 |
| Incus image publishing (move destination parity) | architect | #164 | ✅ Done — closed 08-11 |
| Incus E2E suite | quality | #123 | ✅ Done — closed 08-11 |
| Install path works on every OS the installer claims to support | — | #210, #315 | ✅ Done |
| One visible product release channel, separate from plugin artifacts | — | #210, #316 | ✅ Done |
| tunaOS boot-gate adoption | hanthor | tunaos#1273 (closed, superseded) | ✅ Done — shipped on tunaos main 09-02 |
| `--json` output across commands | — | #205 (closed) | ✅ Done |
| Document stable plugin API + marketplace schema v2 | guide | docs/plugin-marketplace.md | 🟡 In progress |

### Next Quarter (2026 Q4 — October–December)

**Theme**: Enterprise readiness

- VDI plugin GA (desktop pools for Windows/Linux) — #69
- Plugin marketplace growth: schema v2, signed releases, SBOM
- Supply-chain hardening aligned with org Q4 (package signing/SBOM, tunaos#1187)
- Backend support matrix + upgrade/migration documentation (5 backends × tailnet)

---

## Technical Debt Backlog

| Item | Issue | Priority | Effort |
|------|-------|----------|--------|
| `pkg/proxmox` (compat server) vs `pkg/proxmoxbe` (client) naming confusion | CONTEXT.md | P2 | S |
| Legacy `tailvm` prefix migration completion | SPEC.md | P2 | M |

---

## How to Contribute

Issues are triaged with `needs-triage` / `ready-for-agent` / `ready-for-human`
labels (see `docs/agents/issue-tracker.md`). The open surface is small right
now — #97 (dependency dashboard / renovate hygiene) is the most self-contained
pick, and #69 is the large VDI epic.

## Roadmap Governance

Maintained by the strategist agent; updates after major milestones or
quarterly. Propose changes via PR to this file with an issue reference.

**Currency rule**: a tracker cited in this file that closes must move its row in
the same PR, or the row must name a successor.

---
*Generated by strategist agent at ACMM L6 — full mode (ISSUES_AND_PRS).*
