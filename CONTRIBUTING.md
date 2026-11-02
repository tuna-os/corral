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

## Pull Requests

- Keep PRs focused — one change per PR.
- Follow the existing code style and conventions.
- Update `docs/` if your change affects usage, backend parity, or architecture.
- Include the validation commands you ran in the PR description.

### Reviewing generated screenshots

The **Screenshots** workflow captures the demo fleet and retains the
`screenshots` artifact. On non-PR runs, changed Corral images also produce
`corral-screenshot-patch`; its job summary gives the exact download and
apply commands. A successful handoff means the artifact is ready, **not**
that a PR was created or the images published.

A maintainer applies the patch on a branch from current `main`, inspects
every changed image, commits only `docs/screenshots/generated/*.png`, and
opens a normal reviewable PR. If the patch is stale, rerun capture instead
of forcing it. Human review and required checks remain mandatory.
GitHub Actions self-approval stays disabled; no extra token or policy
change is needed. The optional cross-repository site PR is unchanged.

## Questions?

- [TunaOS Documentation](https://tunaos.org)
- [GitHub Issues](https://github.com/tuna-os/corral/issues)
