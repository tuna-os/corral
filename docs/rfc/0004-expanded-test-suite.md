# RFC-0004: Expanded test suite — spend coverage where the risk is

**Status:** Proposal. Draft; this RFC changes no code.
**Date:** 2026-10-11
**Author:** omp + coding assistant

## Summary

Corral's suite is large (149 files, ~1674 test/fuzz functions). Honest gates
protect it (race, shuffle, both tag sets, coverage ratchet, lint two-gate,
fuzz seeds + nightly, kind e2e, ui-smoke, vmtest layers). The problem is
shape, not size. Total coverage is 60.7% against a 59.5% floor. The thinnest
areas are the riskiest ones. At least one test passes in CI but fails on a
workstation with many tools.

The weak areas: the off-cluster bootc path has no tests, and the libvirt
client has 32%. The shell seam under all fakes has 53%. This RFC measures
the gaps. Then it proposes backfills in cheapest-first order, with explicit
non-goals. Without non-goals, a test proposal becomes a 100% mandate that
nobody follows.

## Where we are (measured 2026-10-11, main @ `eece036`)

`go test -count=1 -coverprofile=cover.out ./...` (plain tag set):

| Area | Coverage | Functions | Note |
|---|---|---|---|
| cmd/corral-bootc | 0.0% | 4 | `buildAndImport` at 0% — #368 |
| cmd/corral-incus, cmd/corral-wasm | 0.0% | 1+ | no test files at all |
| pkg/libvirt | 31.8% | 25 | SSH/matrix-claim paths thin |
| pkg/demo | 36.6% | 27 | the fake cluster ui-smoke stands on |
| cmd/corral-auth | 42.0% | 31 | passkeys.go entirely 0% |
| cmd/corral-aws-power | 44.2% | 9 | |
| cmd/corral-backup | 46.5% | 24 | runBackup/runRestore near 0% |
| pkg/snapshot | 49.0% | 47 | the consistency contract |
| cmd/corral-proxmox | 50.0% | 2 | |
| pkg/shell | 52.6% | 35 | the seam every fake goes through |
| pkg/types | 56.2% | 6 | |
| pkg/qemu | 58.3% | 107 | largest fn count; journal code unreached |
| pkg/incus | 60.4% | 43 | |
| pkg/kubevirt | 68.4% | 150 | |
| pkg/backend | 69.8% | 100 | conformance tests hold |
| pkg/web | 72.4% | 203 | |
| pkg/doctor | 81.3% | 49 | post-#447 split |
| pkg/move | 82.1% | 19 | |
| pkg/folder | 92.8% | 34 | |
| pkg/promtext | 95.8% | 8 | |
| **total** | **60.7%** | | floor 59.5% — 1.2pp of slack |

Tiers that run now: unit (race + shuffle + both tag sets) on every push;
coverage ratchet (`just cover`); lint two-gate. Fuzz seed corpora run in the
suite. Fuzz targets also run for 2 minutes each night (`FuzzParseQuantity`, `FuzzMemoryMiB`).
Other tiers: kind + emulated-KubeVirt browser e2e; real-Incus/libvirt/QEMU
e2e; ui-smoke (Chromium against `--demo`); vmtest layer (per-PR) and boot
(weekly); `@live-only` Playwright against the production cluster (manual).

`docs/testing.md` is stale in the other direction: its Layer-1 "plan" to
introduce `pkg/shell` predates `shell.Fake`, and its success criteria
("100+ tests, up from ~35") predates ~1674. The doc describes a smaller,
younger suite than the one that exists.

## Gaps, with evidence

1. **Untested production paths, not only low numbers.** `buildAndImport`
   (cmd/corral-bootc/main.go:77) is the full bootc flow off the cluster.
   It has 0% coverage. #368 prescribes the exact seam: a `Builder`
   interface next to the existing mockable `Target` interface.
   `corral-incus` and `corral-wasm` have no test files. passkeys.go is 0%.
2. **Thinness correlates with risk, inversely with attention.** libvirt
   carries SSH/matrix claims at 31.8%. `pkg/demo` at 36.6% underpins both
   ui-smoke and the TUI demo tests — a bug there green-lights everything
   above it. `pkg/shell` at 52.6% is the fake seam: all unit tests for
   backends trust it.
3. **One environment-dependent test.** `TestList_EmptyLocalQemu` sets
   `KUBECONFIG` to a nonexistent path. But it still shells out to a real
   `kubectl`, and that behavior changes with version and environment. The
   test fails on a workstation with many tools (this audit:
   `kubevirt: exit status 1`), but passes in CI. Thus the result now
   depends on the tools that the machine has. `CORRAL_REQUIRE_TOOLS`
   loud-skips exist to prevent this failure mode.
4. **The ratchet watches the average, not the organs.** `pkg/web` (203
   fns, 72%) can hide a small package that regresses.
   Total-only floors also punish honest work. A large new backend
   package at 0% in its first PR drops the total. This occurs even when
   fixture tests cover each new line.
5. **Fuzz covers 2 parsers; the repo has a dozen.** Other code gets
   adversarial input (peer-supplied IDs, dotfiles YAML). It has only
   example-based tests. This code includes the InstanceRef parser, catalog
   resolution, config load, and all matrix/adapter constructors.
6. **Frontend depth stops at smoke.** ui-smoke proves that the dashboard
   renders against `--demo`. The SPA flows (multi-select bulk bar, export
   picker, tag chips, read-only gate) rely on `@live-only` manual runs.
   They also rely on the non-live specs that the 2026-06-21 change widened.
   No tier owns the middle: scripted interaction against the demo cluster
   on every push.
7. **Nobody pays the new-backend tax.** RFC-0003 adds four backends. A
   fixture standard is necessary (govmomi/simulator, recorded XenAPI
   payloads, prism stubs, VBoxManage fake). Without it, each matrix column
   ships with no tests. Then the conformance tests can only check
   consistency, not behavior.

## Proposal, cheapest first

| Slice | Work | Closes |
|---|---|---|
| 0 | Raise `.coverage-budget` to the measured 60.7% (ratchet slack is ~1pp; the gate script itself demands tightening) | — |
| 1 | #368 seam + table tests for `buildAndImport`; arg-parse/error-path tests for `corral-incus`, `corral-wasm`, passkeys routes | #368 |
| 2 | Hermetic `TestList_EmptyLocalQemu` (inject the runner or skip explicitly when a real kubectl is present without a cluster) — the exhibit-A fix for environment dependence | — |
| 3 | Backfill with numbers: libvirt →60% (SSH/address paths), shell →80% (the seam), snapshot →70% (consistency labels per backend), demo →60% (fake-cluster fidelity), qemu journal code via existing `shell.Fake` | — |
| 4 | Per-package coverage *report* in CI (informational, not a gate): a table like the one above on every PR, so a regressing small package is visible even when the total holds | — |
| 5 | Fuzz inventory + 2–4 new targets (InstanceRef parse, catalog resolve first — both face peer/file input) | — |
| 6 | Mid-tier frontend: drive the demo cluster with scripted interaction (the ui-smoke harness extended to click through bulk/export/tags flows) on every push; triage `@live-only` for specs that could drop the tag under kind | — |
| 7 | Backend-fixture standard folded into RFC-0003's per-backend checklist: no matrix column without a fixture test; simulator/recorded-payload pattern per backend family | RFC-0003 |
| 8 | Refresh `docs/testing.md` (Layer-1 past tense, current counts, both tag sets, the mid-tier) so the next audit starts from truth | — |

Slices 0–2 are each under an hour and independently mergeable. Slice 4
deliberately reports before it gates. Per-package hard floors on day one
would freeze new backends (gap 4), or people would disable them within a
month. The header of the coverage-gate script says that people disable a
gate that nobody trusts.

## Explicitly not in this RFC

- **No 100% target, no per-package hard floors yet.** Slice 4 reports for
  a quarter first. Floors follow only where the numbers prove stable.
- **No TUI layout assertions.** The existing rule stands: assert dispatch
  and content, never padding, borders, or columns.
- **No tests of upstream tools.** Right flags, not their behavior — unchanged.
- **No weekly-boot on every push.** `vmtest-e2e` stays weekly/on-demand.
  The layer tier remains the per-PR boot-evidence gate (cost decision from
  2026-07, not revisited here).
- **No CI-pass-local-fail fixes beyond slice 2.** We file other latent
  environment dependencies when we find them. We do not hunt them in one
  sweep.
