# RFC-0004: Expanded test suite — spend coverage where the risk is

**Status:** Design proposal, not committed. No code in this RFC.
**Date:** 2026-10-11
**Author:** omp + coding assistant

## Summary

Corral's suite is large (149 files, ~1674 test/fuzz functions) and honestly
gated (race, shuffle, both tag sets, coverage ratchet, lint two-gate, fuzz
seeds + nightly, kind e2e, ui-smoke, vmtest layers). The problem is not size,
it is shape: total coverage sits at 60.7% against a 59.5% floor, the thinnest
areas are the riskiest ones (an untested off-cluster bootc path, a 32%
libvirt client, the 53% shell seam everything fakes through), and at least
one test passes in CI while failing on a tool-rich workstation. This RFC
measures the gaps, then proposes backfills in cheapest-first order — with
explicit non-goals, because a testing proposal without them becomes a 100%
mandate nobody follows.

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

Tiers already running: unit (race + shuffle + both tag sets) on every push;
coverage ratchet (`just cover`); lint two-gate; fuzz seed corpora in-suite +
2-minute nightly targets (`FuzzParseQuantity`, `FuzzMemoryMiB`); kind +
emulated-KubeVirt browser e2e; real-Incus/libvirt/QEMU e2e; ui-smoke
(Chromium against `--demo`); vmtest layer (per-PR) and boot (weekly);
`@live-only` Playwright against the production cluster (manual).

`docs/testing.md` is stale in the other direction: its Layer-1 "plan" to
introduce `pkg/shell` predates `shell.Fake`, and its success criteria
("100+ tests, up from ~35") predates ~1674. The doc describes a smaller,
younger suite than the one that exists.

## Gaps, with evidence

1. **Untested production paths, not just low numbers.** `buildAndImport`
   (cmd/corral-bootc/main.go:77) is the entire off-cluster bootc flow at
   0% — #368 prescribes the exact seam (a `Builder` interface next to the
   existing mockable `Target` interface). `corral-incus` and `corral-wasm`
   have no test files. passkeys.go is 0%.
2. **Thinness correlates with risk, inversely with attention.** libvirt
   carries SSH/matrix claims at 31.8%. `pkg/demo` at 36.6% underpins both
   ui-smoke and the TUI demo tests — a bug there green-lights everything
   above it. `pkg/shell` at 52.6% is the fake seam: every backend unit
   test trusts it.
3. **One environment-dependent test.** `TestList_EmptyLocalQemu` sets
   `KUBECONFIG` to a nonexistent path yet still shells out to a real
   `kubectl`, whose behavior varies by version and environment: it fails
   on a tool-rich workstation (this audit: `kubevirt: exit status 1`)
   while green in CI. The suite's answer to "does this pass?" currently
   depends on which tools the machine happens to have — the exact failure
   mode `CORRAL_REQUIRE_TOOLS` loud-skips were built to prevent.
4. **The ratchet watches the average, not the organs.** `pkg/web` (203
   fns, 72%) can mask a regressing small package indefinitely. Total-only
   floors also punish honest work: adding a large new backend package at
   0%-first-PR drops the total even when every new line is fixture-tested.
5. **Fuzz covers 2 parsers; the repo has a dozen.** InstanceRef parsing,
   catalog resolution, config load, and every matrix/adapter constructor
   take adversarial input (peer-supplied IDs, dotfiles YAML) with only
   example-based tests.
6. **Frontend depth stops at smoke.** ui-smoke proves the dashboard renders
   against `--demo`; the SPA flows (multi-select bulk bar, export picker,
   tag chips, read-only gate) rely on `@live-only` manual runs plus the
   2026-06-21 widened non-live specs. No tier owns the middle: scripted
   interaction against the demo cluster on every push.
7. **New-backend tax is unbilled.** RFC-0003 adds four backends; without a
   fixture standard (govmomi/simulator, recorded XenAPI payloads, prism
   stubs, VBoxManage fake) each matrix column ships test-free and the
   conformance tests can only check consistency, not behavior.

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

Slices 0–2 are each under an hour and independently mergeable. Slice 4 is
deliberately report-before-gate: per-package hard floors on day one would
either freeze new backends (gap 4) or get disabled within a month — the
coverage-gate script's own header says a gate nobody trusts gets disabled.

## Explicitly not in this RFC

- **No 100% target, no per-package hard floors yet.** Slice 4 reports for
  a quarter first; floors follow only where the numbers prove stable.
- **No TUI layout assertions.** The existing rule stands: assert dispatch
  and content, never padding, borders, or columns.
- **No testing upstream tools.** Right flags, not their behavior — unchanged.
- **No weekly-boot on every push.** `vmtest-e2e` stays weekly/on-demand;
  the layer tier remains the per-PR boot-evidence gate (cost decision from
  2026-07, not revisited here).
- **No fixing CI-pass-local-fail beyond slice 2.** Other latent
  environment dependencies get filed as found, not hunted in one sweep.
