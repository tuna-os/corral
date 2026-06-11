# Corral Testing Plan

## Current state

7 test files, ~35 tests total, ~760 lines of test code. All are unit tests for
pure-logic functions (manifest generation, registry CRUD, type marshaling,
plugin resolution). No integration tests, no HTTP handler tests, no frontend
tests, no tests that touch a real cluster or QEMU.

| Package | Tests | What they cover | What's missing |
|---|---|---|---|
| `kubevirt/` | 14 | manifest generation, VM-list parsing, helpers | VM CRUD (create/start/stop/delete), vol/snapshot/clone ops, cloud-init injection, SSH |
| `registry/` | 6 | file-based CRUD | concurrent access, corruption recovery |
| `qemu/` | 4 | basic ops | all QEMU lifecycle (start/stop/SSH/logs), service template generation |
| `types/` | 2 | JSON round-trip | validation, edge cases |
| `plugin/` | 2 | resolve/install | marketplace fetch, actual download, remove |
| `web/` | 2 | server startup | **all 35+ HTTP handlers**, WS bridges, bootc task tracking, error paths |
| `cmd/` | 5 | CLI parsing | actual command execution, flag interactions, TUI |
| `catalog/` | 0 | — | Find(), catalog integrity |
| `config/` | 0 | — | YAML parsing, env-var fallback, auth-key masking |
| `doctor/` | 0 | — | each diagnostic check, Fix() |

---

## Testing strategy: three layers

### Layer 1 — Unit tests (fast, offline, always run)

**Goal:** catch logic bugs before they reach the cluster. Run in CI on every
push. No external dependencies.

**Approach:** inject a `CommandRunner` interface everywhere code shells out
to `kubectl`, `virtctl`, or `systemctl`. Today those calls are raw
`exec.Command` scattered through the codebase. By introducing:

```go
type Runner interface {
    Run(name string, args ...string) ([]byte, error)
    RunStream(name string, args ...string) error  // for SSH, VNC, logs
}
```

…and a `RealRunner` (default) + `FakeRunner` (test), unit tests can simulate
any cluster state without a real cluster.

**What this unblocks:**

| Handler/function | FakeRunner scenario | Test count |
|---|---|---|
| `ListVMs` | kubectl returns 2 VMs, 1 running | 4 (empty, error, single, multi) |
| `CreateVM` | kubectl apply succeeds | 3 (success, name-collision, namespace-creation) |
| `StartVM` | virtctl start succeeds | 2 (success, already-running) |
| `Scale` | VM is live-migratable → hotplug; not → offline | 3 (live-hotplug, offline, error) |
| `Snapshot` | feature gate present/absent | 2 (create, missing-gate) |
| `handleImages` | catalog.Find hits/misses | 2 |
| `handleImportDataVolume` | CDI present/missing | 2 |
| `handleCreateVM` | all 6 source types, bootc task, error paths | ~12 |
| `handleExport` | VM running (reject) / stopped (stream) | 2 |
| Doctor checks | each diagnostic | ~10 |
| Plugin install | download succeeds/fails | 2 |

**Implementation order:**

1. Introduce `Runner` interface + `RealRunner` in a new `pkg/shell/` package
2. Thread `Runner` through `kubevirt.Client`, `qemu`, and `web.Server`
3. Add `FakeRunner` with scriptable responses
4. Write handler tests for the 5 highest-value handlers
5. Backfill for remaining handlers

**Cost:** moderate refactor (~200 lines of plumbing). High payoff — unlocks
~80 unit tests that today can't exist.

### Layer 2 — Integration tests (needs cluster, opt-in)

**Goal:** verify that real kubectl/virtctl commands produce real VMs that
actually boot, accept SSH, and respond to lifecycle operations.

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
feature gate. The test suite probes capabilities and skips what isn't
available.

### Layer 3 — Frontend tests (browser, opt-in)

**Goal:** catch JS regressions in the SPA (broken forms, missing DOM
elements, API wiring errors).

**Approach:** [Playwright](https://playwright.dev/) with a single scenario
file. The Go server starts on a random port, Playwright opens Chrome and
exercises the UI.

```javascript
// test/e2e/corral.spec.js
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

**What to test:**

| Page/component | Checks |
|---|---|
| Datacenter view | VM table renders, node cards show counts |
| Create dialog | Source-type toggle shows/hides fields, catalog populates, submit sends correct body |
| Image library | Import dialog flow, delete confirmation |
| VM detail — Summary | Status, CPU, memory, IP, actions available |
| VM detail — Hardware | Scale form, add-volume form, expand dialog |
| VM detail — Snapshots | List, create, delete |
| Tree sidebar | Node → VM navigation, drawer toggle (mobile) |

**Prerequisites:** the Playwright test server needs a `FakeRunner` backing
the API, or a static cluster snapshot. Using the fake runner (Layer 1)
avoids needing a real cluster for frontend tests.

---

## Implementation roadmap

### Phase 1 — Foundation (2-3 sessions)

1. **Introduce `pkg/shell/`** — `Runner` interface + `RealRunner` (wraps
   `os/exec`) + `FakeRunner` (map of command → canned output).
2. **Thread `Runner` through constructors** — `kubevirt.NewClient(ns,
   runner)`, `web.Serve(addr, runner)`, `qemu.NewManager(runner)`.
   Default to `RealRunner` so existing code paths don't change.
3. **Write FakeRunner** — supports exact-match, prefix-match, and regex
   command matching. Records calls for assertion. Preload with realistic
   kubectl JSON output.

### Phase 2 — Handler unit tests (2-3 sessions)

1. Tests for `handleListVMs`, `handleCreateVM`, `handleVMAction`,
   `handleDeleteVM` (the hot path).
2. Tests for `handleImages`, `handleListDataVolumes`,
   `handleImportDataVolume`, `handleDeleteDataVolume`.
3. Tests for `handleScale`, `handleAddVolume`, `handleExpand`,
   `handleSnapshot*`, `handleClone`.
4. Tests for `handleDoctor`, `handlePlugins`, error paths (404, 503, 500).

Target: 60-80 handler tests, covering every HTTP route with at least one
happy-path and one error-path case.

### Phase 3 — Pure-logic unit tests (1 session)

1. `catalog/catalog_test.go` — Find, edge cases
2. `config/config_test.go` — YAML parse, env fallback, missing file
3. `doctor/doctor_test.go` — each check with FakeRunner

### Phase 4 — Integration tests (2 sessions)

1. Write the integration test harness (namespace lifecycle, cleanup, skip
   logic for missing capabilities).
2. Port `client_test.go` manifest-generation tests into integration (they
   already exist as unit tests — add real apply + verify).
3. Catalog create → SSH → delete smoke test.
4. Snapshot + restore test.
5. QEMU create → SSH → delete test (local, needs KVM).

### Phase 5 — Frontend tests (1-2 sessions)

1. Install Playwright, write a `test/e2e/` directory with config.
2. Write the test server (starts Go server with FakeRunner on random port).
3. Write ~10 Playwright scenarios covering the critical UI paths.
4. Wire into `just test-e2e` or `npm test` in the project root.

### Phase 6 — CI (1 session)

Update `.github/workflows/ci.yml`:

```yaml
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - run: go test ./... -count=1 -short

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

- **TUI rendering** — Bubble Tea's rendering is a framework concern. Test
  the action dispatch logic, not terminal output.
- **kubectl/virtctl correctness** — those are upstream tools. Test that we
  pass the right flags, not that they work.
- **Go `net/http` routing** — the stdlib is tested. Test the handler logic,
  not the mux.
- **100% coverage** — the web UI has 25+ handlers; 80% coverage of the
  hot paths (create, list, action, delete, images, scale, snapshots) is the
  target. Doctor, plugins, NADs, NICs are lower priority.
- **Bootc plugin binary** — test bootc through the kubevirt package with a
  real cluster in integration; the separate binary is a thin Cobra wrapper.

---

## Test utilities to build

### `pkg/shell/fake.go`

```go
type FakeRunner struct {
    Responses map[string]FakeResponse  // command → canned output
    Calls     []Call                   // record of every invocation
}

type FakeResponse struct {
    Stdout string
    Stderr string
    Err    error
}
```

### `pkg/web/testutil.go`

```go
// NewTestServer returns a running server with a FakeRunner for handler tests.
func NewTestServer(t *testing.T) (*httptest.Server, *FakeRunner) { ... }

// Fixture helpers
func fakeVMList() []byte       // realistic kubectl get vms -A -o json
func fakeVMIList() []byte      // running VMIs with IPs
func fakeNodeList() []byte     // 2 nodes, 1 ready
```

### `test/e2e/helpers.js`

```javascript
// Start the test server, navigate, provide common selectors
async function setup(page) { ... }
async function createVM(page, name, sourceType, source) { ... }
async function deleteVM(page, name) { ... }
```

---

## Success criteria

| Metric | Target |
|---|---|
| Unit test count | 100+ (up from ~35) |
| Handler coverage | 100% of routes have ≥1 test |
| Integration smoke test | create → SSH → delete passes reliably |
| CI gate | `go test ./...` runs in <30s on PR |
| Frontend regression | critical paths covered by Playwright |
| Flake rate | <5% for integration tests (skip on capability mismatch) |
