# Corral Testing Plan

## Current state (refreshed 2026-10-12)

174 test files, ~1,840 test functions, ~46k lines of test code, across four layers:

- **Unit tests** cover the earlier gaps. They cover web handlers
  (HTTP via `httptest` + a scriptable `shell.Fake` runner) and WS console
  bridges (byte-level round-trips, including the local-VNC bridge). They also
  cover doctor checks and fixes, qemu ops, kubevirt manifest/parse logic, and
  the parse logic in cmd.
- **Demo-mode end-to-end** (`pkg/web/demo_test.go`): boots the real mux
  against the in-memory fake cluster in `pkg/demo`. It exercises list → action →
  state-flip → metrics through real handler code.
- **UI smoke in CI** (`.github/workflows/ui-smoke.yml`): headless Chromium
  drives the actual dashboard against `corral web --demo` on every
  `pkg/web`/`pkg/demo` change (`scripts/ui-smoke.mjs`).
- **VDI broker e2e** (`scripts/e2e-vdi.sh`, in the cluster e2e job): a
  second `corral web` runs the broker on real Leases and VMs. It claims desktops, checks ownership, resolves connect, and releases.
  Then it waits for a reclaim at the maximum session duration. Last, it
  builds `corral-vdi` from the v0.6.0 tag. It checks that an assignment of
  that release carries over (see the
  [upgrade path](enterprise-readiness.md#upgrade-path)).
- **Cluster e2e** (`.github/workflows/e2e.yml`): kind + emulated KubeVirt on
  GitHub runners — real `kubectl`/`virtctl` against a real API server.
- **The generated layer, in a real bootc image** (`just vmtest-layer`, the
  `layer` job in `.github/workflows/vmtest.yml`). It builds what `pkg/vmtest`
  generates on top of `quay.io/fedora/fedora-bootc:41`. Then it asserts inside
  the image: the accounts, the keys, the passwords, the modes, the units. A
  container engine and a minute — no KVM, no root. It runs on every pull request
  that touches the harness. Build tag `e2elayer`.
- **A real bootc boot** (`just vmtest-e2e`, the `boot` job): `corral vmtest`
  builds a real image into a disk and boots it with KVM. Then it asserts inside
  the guest over SSH. Weekly and on demand, never on a push — it pulls
  gigabytes and boots a VM. Build tag `e2evmtest`, as `pkg/bootc` uses
  `e2ebootc`.
- **Fuzz targets** (6): each seed corpus runs with the normal suite. The
  nightly workflow gives each target two minutes. See [Fuzz targets](#fuzz-targets).
- **Matrix conformance** (`pkg/backend/matrix_test.go`): the 9-backend parity
  matrix stays in sync with the code and the docs. See
  [Backend matrix conformance](#backend-matrix-conformance).

  The split between those two is not tidiness. Three runs of the boot job died
  in the same container build step, for three variations of one shell mistake.
  Nothing cheaper ever ran the generated shell against a real image. The layer
  tier costs a minute and catches that whole class.

Two files drive the TUI's update loop directly. `cmd/tui_flows_test.go`
covers the list, the context cycle, quick keys, the gate on actions, the
confirm/clone/ports/hardware forms, doctor, and help. `cmd/tui_views_test.go`
covers snapshots, events, the template mark, and CT scale. Keypress in, state
and rendered output out. The tests run against the in-memory demo cluster or a
scripted `shell.Fake`.

## Gates (refreshed 2026-10-12)

Four gates hold the line. Each one is a ratchet, seeded from this repo's own
state:

- **Lint** (`just lint`, the `lint` job). Every linter but errcheck must be
  clean across the repo. errcheck examines only the diff
  (`--new-from-merge-base`). New code cannot add an unchecked error. The older
  ones go away in their own time.
- **Coverage** (`just cover`). `.coverage-budget` holds the floor (60.7). CI fails
  when the total goes below it. CI also tells you to raise the floor when the
  total goes above it. A per-package table follows the gate. It is
  informational only. See [Coverage by package](#coverage-by-package).
- **Order independence.** The suite uses `-shuffle=on`. Much state in the
  process is global, and the tests share it. A test that passes only after
  some other test ran is a real risk here. A shuffle makes it fail the first
  time.
- **Loud skips.** `CORRAL_REQUIRE_TOOLS` names what an environment supplies
  (see `pkg/testenv`). A test that skips for a tool on that list fails
  instead. A runner that drops `incus` can no longer show green while it runs
  nothing.

Both tag sets run in CI and in `just test`: the plain suite, then the suite
with `-tags bootc`. The bootc plugin compiles only under its tag, so one set
alone cannot cover it.

Fuzz targets run their seed corpus with the normal suite. The nightly workflow
gives each target two minutes. That workflow also runs the `@live-only`
Playwright tier when `CORRAL_E2E_URL` points at the isolated e2e instance.

Known gaps: `config/` and `catalog/` remain thin. The adapter tests do not
reach the journal code in `pkg/qemu`. The QMP code now has a fake monitor
(`fakeQMPServer`), which the frame and screenshot tests drive.

`.github/workflows/vmtest.yml` is the first job that uses real KVM.

---

## Coverage by package

`scripts/coverage-report.sh` reads the same `cover.out` as the gate. It prints
a Markdown table, one row per package, sorted worst first. It bolds the five
weakest packages. It always exits 0, so it never fails the build. The gate stays
the only enforcer. CI appends the table to the step summary, so the weakest
package is visible on every run.

## Web client unit tests

`pkg/web/jstest/*.test.mjs` tests the web client's ES modules one at a time,
with no browser and no server. Each test imports the file that
`pkg/web/static/` ships, so the test and the browser run the same code. The
tests use `node:test` and stub `fetch`, so there is nothing to install. A
module that touches `document` imports `dom-stub.mjs` first, which supplies
the few DOM calls the client makes. The files are outside `static/`, so
`go:embed` does not ship them.

Run them with `just test-js` (Node 22 or newer). CI runs them first in
`.github/workflows/ui-smoke.yml`. Put the API request shape, error messages,
and state transitions here. Keep the rendered page in UI smoke.

## Mid-tier frontend flows

Two suites sit between unit tests and the live cluster. Both drive the real
dashboard. Neither needs production.

- **UI smoke** (`scripts/ui-smoke.mjs`, in `.github/workflows/ui-smoke.yml`).
  It serves `corral web --demo`. It checks the fleet table, the tree, and the
  tag filter bar. It checks the Incus split between containers and VMs. It
  checks column order and sort in the grid. It checks the summary SSH hint.
  It checks the console pop-out and the context menu.
- **Mid-tier Playwright** (`e2e/corral.spec.js` without `@live-only`, in
  `.github/workflows/e2e.yml`). It runs kind + emulated KubeVirt on every
  push to `main`. It also runs on each pull request that changes the
  `corral` binary, the UI or the specs. It covers the wizard for every source type and the
  catalog. It covers CDI import to Succeeded and scale on a stopped VM. It
  covers one cirros boot lifecycle and the bulk-action bar. It covers the
  export picker, tag chips and filters, and `/api/whoami`. It covers the
  read-only gate and the CT create-console-delete flow.

## Fuzz targets

Six targets, each beside the code it exercises:

- `FuzzFind` and `FuzzResolveBootc` in `pkg/catalog/catalog_fuzz_test.go`
- `FuzzLoad` in `pkg/config/config_fuzz_test.go`
- `FuzzMemoryMiB` in `pkg/proxmoxbe/ops_fuzz_test.go`
- `FuzzInstanceRef` in `pkg/types/instance_fuzz_test.go`
- `FuzzParseQuantity` in `pkg/web/prometheus_fuzz_test.go`

`go test ./...` runs every seed corpus. `.github/workflows/nightly.yml`
fuzzes each target for 120 seconds, one target at a time. It uploads any
crasher under `testdata/fuzz/`.

## Backend matrix conformance

`pkg/backend/matrix.go` lists 9 backends: kubevirt, qemu, incus, libvirt,
proxmox, virtualbox, vsphere, xen, nutanix. It records what each backend does
about every operation Corral ships. `pkg/backend/matrix_test.go` checks that
table against the declared capabilities, the snapshot adapter registry, and
`docs/backend-parity.md`. The table in the docs cannot drift from the code. No
backend can advertise a capability that nothing makes.

---

## Testing strategy: three layers

### Layer 1 — Unit tests (fast, offline, always run)

**Goal:** catch logic bugs before they reach the cluster. Run in CI on every
push. No external dependencies.

**Approach:** `pkg/shell` defines a `Runner` interface. `Real` shells out
through `os/exec`. `Fake` returns canned responses. Handlers take a `Runner`,
and tests script it.

```go
type Runner interface {
    Run(name string, args ...string) ([]byte, error)
    RunStdin(stdin string, name string, args ...string) ([]byte, error)
    LookPath(name string) (string, error)
}
```

`Fake` matches exact commands and prefixes (`AddResponse`,
`AddPrefixResponse`). It records every call for assertion. Tests preload it
with realistic JSON output from kubectl.

**What this covers:**

| Handler/function | Fake scenario |
|---|---|
| `ListVMs` | kubectl returns 2 VMs, 1 running (empty, error, single, multi) |
| `CreateVM` | kubectl apply succeeds (success, name-collision, namespace-creation) |
| `StartVM` | virtctl start succeeds (success, already-running) |
| `Scale` | VM is live-migratable → hotplug; not → offline (live-hotplug, offline, error) |
| `Snapshot` | feature gate present/absent (create, missing-gate) |
| `handleImages` | catalog.Find hits/misses |
| `handleImportDataVolume` | CDI present/missing |
| `handleCreateVM` | all 6 source types, bootc task, error paths |
| `handleExport` | VM running (reject) / stopped (stream) |
| Doctor checks | each diagnostic |
| Plugin install | download succeeds/fails |

**How it landed:**

1. `Runner` + `Real` + `Fake` in `pkg/shell/` — done.
2. `Runner` threaded through `kubevirt.Client`, `qemu`, and `web.Server` — done.
3. `Fake` with exact, prefix, and regex matching plus a call record — done.
4. Handler tests for the 5 highest-value handlers — done.
5. Backfill for the remaining handlers — done.

### Layer 2 — Integration tests (needs cluster, opt-in)

**Goal:** verify that real commands from kubectl/virtctl produce real VMs
that boot, accept SSH, and respond to lifecycle operations.

**Approach:** Go build tag `//go:build integration`. Run only when a
KubeVirt cluster is available (CI on a schedule or manual trigger, never on
every push). Tests create real VMs in an isolated namespace and clean up.

```go
//go:build integration

func TestIntegration_CreateBootDelete(t *testing.T) {
    ns := "corral-test-" + randomSuffix()
    client := kubevirt.NewClient(ns)
    client.EnsureNamespace(ns)
    t.Cleanup(func() { client.DeleteNamespace(ns) })

    // Create from catalog
    opts := types.CreateOpts{
        Name: "test-vm", Namespace: ns,
        ContainerDisk: "quay.io/containerdisks/fedora:42",
        Mem: "2G", CPU: 1, Disk: "5Gi",
    }
    require.NoError(t, client.CreateVM(opts))
    require.NoError(t, client.StartVM("test-vm"))

    // Wait for ready
    require.Eventually(t, func() bool {
        vms, _ := client.ListVMs()
        for _, v := range vms {
            if v.Name == "test-vm" && v.Ready { return true }
        }
        return false
    }, 3*time.Minute, 5*time.Second)

    // SSH in and run a command
    out, err := client.SSH("test-vm", "fedora", "", "hostname", "22", "")
    require.NoError(t, err)
    require.Contains(t, out, "test-vm")

    client.DeleteVM("test-vm")
}
```

**What to test at this layer:**

| Scenario | Time | Risk of flake |
|---|---|---|
| Catalog VM: create → start → SSH → stop → delete | ~5 min | Low (containerdisk is reliable) |
| ISO import: create → CDI import → start → verify boot | ~10 min | Medium (CDI download) |
| Snapshot: create VM → snapshot → restore → verify state | ~8 min | Low |
| Scale: create → hotplug CPU → verify guest sees new CPUs | ~5 min | Medium (needs guest agent) |
| QEMU: create → start → SSH → stop → delete | ~3 min | Low (local, no network) |
| Web UI E2E: Playwright against corral web | ~3 min | Medium (browser timing) |

**Cluster requirements:** longhorn RWX, VolumeSnapshotClass, HotplugVolumes
feature gate. The test suite checks for each capability and skips what
isn't available.

### Layer 3 — Frontend tests (browser, opt-in)

**Goal:** catch JS regressions in the SPA (broken forms, missing DOM
elements, API wiring errors).

**Approach:** [Playwright](https://playwright.dev/) with a single scenario
file. The Go server starts on a random port, Playwright opens Chrome and
exercises the UI.

```javascript
// e2e/corral.spec.js
test('create dialog shows catalog images', async ({ page }) => {
    await page.goto('http://localhost:PORT');
    await page.click('#btn-create');
    await page.selectOption('[name=sourceType]', 'catalog');
    // Catalog dropdown should be populated
    const options = await page.$$eval('[name=catalogImage] option', els =>
        els.map(e => e.textContent));
    expect(options.length).toBeGreaterThan(0);
    expect(options.some(o => o.includes('fedora'))).toBe(true);
});

test('image library loads datavolumes', async ({ page }) => {
    await page.goto('http://localhost:PORT');
    await page.waitForSelector('#dc-images');
    // Should show datavolumes or the "no images" placeholder
    const text = await page.textContent('#dc-images');
    expect(text).toBeTruthy();
});
```

**What this covers:**

| Page/component | Checks |
|---|---|
| Datacenter view | VM table renders, node cards show counts |
| Create dialog | Source-type toggle shows/hides fields, catalog populates, submit sends correct body |
| Image library | Import dialog flow, delete confirmation |
| VM detail — Summary | Status, CPU, memory, IP, actions available |
| VM detail — Hardware | Scale form, add-volume form, expand dialog |
| VM detail — Snapshots | List, create, delete |
| Tree sidebar | Node → VM navigation, drawer toggle (mobile) |

The suites run against `corral web --demo` (ui-smoke) or kind + emulated
KubeVirt (e2e.yml). `shell.Fake` and the demo cluster stand in for a real
cluster. No suite needs production.

---

## Implementation roadmap

### Phase 1 — Foundation (2-3 sessions) — done

1. **`pkg/shell/`** — `Runner` interface + `Real` (wraps
   `os/exec`) + `Fake` (map of command → canned output).
2. **`Runner` through constructors** — `kubevirt.NewClient`,
   `web` server setup, `qemu` manager code. Production defaults to `Real`,
   so existing code paths did not change.
3. **Scriptable `Fake`** — exact-match, prefix-match, and regex
   command matching. Tests assert on the call record. Realistic
   JSON output from kubectl as fixtures.

### Phase 2 — Handler unit tests (2-3 sessions) — done

1. Tests for `handleListVMs`, `handleCreateVM`, `handleVMAction`,
   `handleDeleteVM` (the hot path).
2. Tests for `handleImages`, `handleListDataVolumes`,
   `handleImportDataVolume`, `handleDeleteDataVolume`.
3. Tests for `handleScale`, `handleAddVolume`, `handleExpand`,
   `handleSnapshot*`, `handleClone`.
4. Tests for `handleDoctor`, `handlePlugins`, error paths (404, 503, 500).

The suite now holds ~1,840 test functions. Every HTTP route has at least one
happy-path and one error-path case.

### Phase 3 — Pure-logic unit tests (1 session) — done

1. `catalog/catalog_test.go` — Find, edge cases, plus `FuzzFind` and
   `FuzzResolveBootc`.
2. `config/config_test.go` — YAML parse, env fallback, missing file, plus
   `FuzzLoad`.
3. `doctor/doctor_test.go` — each check with `Fake`.

### Phase 4 — Integration tests (2 sessions) — done

1. The integration test harness (namespace lifecycle, cleanup, skip
   logic for missing capabilities).
2. `client_test.go` manifest tests plus a real apply and verify.
3. Catalog create → SSH → delete smoke test.
4. Snapshot + restore test.
5. QEMU create → SSH → delete test (local, needs KVM).

### Phase 5 — Frontend tests (1-2 sessions) — done

1. Playwright plus an `e2e/` directory with config.
2. The test server (starts the Go server with `Fake` on a random port).
3. Playwright scenarios for the important UI paths, in two tiers: the CI
   tier and the `@live-only` tier.
4. Wired into CI (`ui-smoke.yml`, `e2e.yml`, `nightly.yml`).

### Phase 6 — CI (1 session) — done

`.github/workflows/ci.yml` now runs:

```yaml
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - run: go test -race -shuffle=on -count=1 ./...
      - run: go test -tags bootc -race -shuffle=on -count=1 ./...

  coverage:
    steps:
      - run: scripts/coverage-gate.sh cover.out
      - run: scripts/coverage-report.sh cover.out

  integration:
    runs-on: self-hosted  # machine with kubectl context to the cluster
    if: github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'
    steps:
      - run: go test -tags integration ./... -count=1 -timeout 30m

  e2e:
    runs-on: ubuntu-latest
    steps:
      - run: go build -o corral .
      - run: npx playwright test
```

---

## What NOT to test

- **TUI layout** — how Bubble Tea and lipgloss lay out a frame is a framework
  concern. Assert on dispatch and on the *content* a view puts on screen. Does
  it name the instance? Does it surface the backend's refusal? Never assert on
  padding, borders, or column positions.
- **kubectl/virtctl correctness** — those are upstream tools. Test that we
  pass the right flags, not that they work.
- **Go `net/http` routes** — the Go project tests the stdlib. Test the handler logic,
  not the mux.
- **100% coverage** — the web UI has 25+ handlers. The target is 80% coverage
  of the hot paths (create, list, action, delete, images, scale, snapshots). Doctor, plugins, NADs, NICs are lower priority.
- **Bootc plugin binary** — test bootc through the kubevirt package with a
  real cluster in integration. The separate binary is a thin Cobra wrapper.

---

## Test utilities to build

### `pkg/shell/fake.go`

```go
type Fake struct {
    responses map[string]FakeResponse  // command → canned output
    calls     []Call                   // record of every invocation
}

type FakeResponse struct {
    Stdout string
    Stderr string
    Err    error
}
```

`NewFake` builds an empty `Fake`. `AddResponse` scripts an exact command.
`AddPrefixResponse` scripts a prefix. Tests assert on the recorded calls.

### `pkg/web/testutil.go`

```go
// NewTestServer returns a running server with a FakeRunner for handler tests.
func NewTestServer(t *testing.T) (*httptest.Server, *FakeRunner) { ... }

// Fixture helpers
func fakeVMList() []byte       // realistic kubectl get vms -A -o json
func fakeVMIList() []byte      // running VMIs with IPs
func fakeNodeList() []byte     // 2 nodes, 1 ready
```

### `e2e/` helpers

```javascript
// Start the test server, navigate, provide common selectors
async function setup(page) { ... }
async function createVM(page, name, sourceType, source) { ... }
async function deleteVM(page, name) { ... }
```

---

## Success criteria

| Metric | State |
|---|---|
| Unit test count | ~1,840 test functions in 174 files (target 100+ met) |
| Handler coverage | every route has ≥1 happy-path and ≥1 error-path test |
| Integration smoke test | create → SSH → delete passes reliably |
| CI gate | `go test ./...` plus `-tags bootc`, both with `-race -shuffle=on` |
| Frontend regression | ui-smoke on every `pkg/web` change; mid-tier Playwright on every push |
| Flake rate | <5% for integration tests (skip on capability mismatch) |

---

## Browser e2e: two tiers (2026-06-12)

The Playwright suite (`e2e/corral.spec.js`) drives the real web UI against a
real cluster, with multi-angle verification. Every "the UI says it worked"
gets a cross-check from the cluster side. The checks look at the VMI phase, the
virt-launcher pod state, the qemu container logs, and the guest serial output
over the TTY websocket.

**Safety:** every resource is `e2e-`-prefixed, deleted in `afterEach`
(verified gone), and the cleanup guard refuses to touch unprefixed names —
safe to run against production.

| Tier | What | Where it runs |
|---|---|---|
| CI (default) | wizard flows, catalog content, create for every source type, CDI import to Succeeded (PVC flow), scale on a stopped VM, one full boot lifecycle (cirros) | `.github/workflows/e2e.yml`: kind + KubeVirt `useEmulation` + CDI on every push to `main` and on PRs that change the `corral` binary, UI or specs — `npx playwright test --grep-invert "@live-only"` |
| `@live-only` | console websockets (VNC RFB handshake, serial output) + fullscreen/scaling controls, SSH login with an injected key, RDP probe, snapshots (longhorn), bootc build → boot → serial check, boot-from-import | manually against the real cluster: `corral web` + `cd e2e && npx playwright test` |

Knobs: `CORRAL_URL` (default `` http://localhost:8006 ``), `CORRAL_NS` (default
`tailvm`), `E2E_CONTAINERDISK` (lifecycle boot image; CI uses the cirros demo
containerdisk because fedora won't boot in sensible time under TCG).

### Update (2026-06-21)

- **Path-targeted CI.** Both workflows now run only when the diff touches their
  area. The fast gate for Go code runs on `**.go`/`go.mod`; the e2e suite runs on
  Go / `pkg/web/static` / `e2e` / `deploy`. Docs/marketplace-only PRs skip both; `main` pushes stay
  fully validated.
- **Real external tools run in CI.** The fast gate installs `qemu-utils` +
  `rclone`. So `TestConvertRawToQcow2_Real` (qcow2 export) and the backup
  plugin's `rclone copyto` round-trip now run for real. Before, they skipped.
- **Non-live e2e widened** to the new SPA flows. These are the multi-select bulk
  bar, the export format picker, tag chips + filter, `/api/whoami`, and the
  read-only gate. The read-only test uses a `page.route` mock, so it needs no
  Tailscale headers.
- **`@live-only` adds** live migration between nodes and the bootc rebuild
  round-trip in which SSH survives `--wipe`.
