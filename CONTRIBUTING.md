# Contributing

Thanks for your interest in a contribution! This project is part of the [TunaOS](https://tunaos.org) ecosystem.

<!-- hive-contribute-plea: donated-compute appeal, keep in sync across repos -->
## Contribute compute — no code needed

**[Donate time with your AI CLI](https://hub.tunaos.org/#contribute).**

Your machine runs one task at a time using your signed-in AI coding CLI.
The relay alternates between eligible **School** (OS) and **Reef** (apps)
hives after each task. If one hive has no work, the other can continue.

The guide covers prerequisites, verified `just` setup, credentials, provider
costs, and how to stop with Ctrl+C. Your subscription quota or API charges apply.
Never post account credentials or API keys in issues or pull requests.

## Getting Started

1. Fork the repo and clone it locally.
2. Install the Go version declared in [`go.mod`](go.mod) and the
   [`just`](https://just.systems) command runner.
3. Read the project [README](README.md), [architecture overview](docs/architecture.md),
   and [test strategy](docs/testing.md).
4. Open an issue to discuss your change before you submit a PR.

## Validate Your Changes

Corral builds two tag sets — plain and `-tags bootc` (the bootc plugin) —
and CI checks both. Run the local pre-push gate before you open a PR:

```bash
just ci
```

This runs, in order: `gofmt` (fmt-check), `go vet` (both tag sets),
`go build` (both tag sets), `go test -race` (both tag sets), and `golangci-lint`. You can
also run the steps individually:

```bash
just fmt     # gofmt -w
just vet     # go vet, both tag sets
just build   # go build, both tag sets
just test    # go test -race, both tag sets
just lint    # golangci-lint (repo-wide except errcheck, errcheck on diff)
just cover   # go test -coverprofile, fails if below .coverage-budget floor
just test-js # node --test, the web client's ES module unit tests
```

Some tests use real tools (qemu-img, rclone conversions). If you touch that
code path, install them locally, or rely on CI to run them.

For bootc image verification, run `just vmtest-layer` to test the generated layer or `just vmtest-e2e` for complete VM boot tests.

### CI checks outside `just ci`

CI also runs these checks. `just ci` does not run them.

- **Prose (the `ste` job).** CI lints each changed Markdown file for
  Simplified Technical English. `.ste-budget` sets the number of findings
  that CI accepts, and that number is 0. Write short sentences in the active
  voice. Do not raise the budget to make a PR pass.
- **Ratchet (the `Ratchet` workflow).** `.ratchet.toml` sets a budget for
  statements in non-test Go that discard a call's results (`_ = f()`). A PR
  that adds one fails. A PR that removes one also fails until you write the
  lower budget in `.ratchet.toml`. The error message gives that number. A PR
  that raises a budget needs the `ratchet-raise` label.
- **Path-scoped jobs.** CI selects jobs from the files that a PR changes
  (`.github/scripts/ci-scope.sh`). A PR that changes only Markdown runs `ste`
  but not `test` or `lint`. `docs/backend-parity.md` is the exception, because
  tests read it. A draft PR skips the e2e, UI, QEMU and image jobs. The
  `run-heavy` label runs all jobs.

## Pull Requests

- Keep PRs focused — one change per PR.
- Follow the existing code style and conventions.
- Update `docs/` if your change affects usage, backend parity, or architecture.
- Include the validation commands you ran in the PR description.

### Reviewing generated screenshots

The **Screenshots** workflow captures the demo fleet and keeps the
`screenshots` artifact. On non-PR runs, changes to Corral images also produce
`corral-screenshot-patch`. The job summary gives the exact download and apply
commands. A successful handoff means the artifact is ready; it does not mean
that a PR exists or images are public.

A maintainer applies the patch on a branch from current `main` and inspects
every changed image. They commit only `docs/screenshots/generated/*.png` and
open a normal PR for review. If the patch is stale, run capture again; do not
force the patch. Human review and required checks remain mandatory.

GitHub Actions self-approval stays disabled. This needs no extra token or
policy change. The optional PR into the site repository stays unchanged.

## Questions?

- [TunaOS Documentation](https://tunaos.org)
- [GitHub Issues](https://github.com/tuna-os/corral/issues)
