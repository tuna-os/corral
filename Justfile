# Corral task runner. `just` (https://github.com/casey/just) — run `just` to list.
# Build tags: most code is plain; the bootc plugin needs `-tags bootc`.

# Version stamp for `corral version`. Defaults to the current git describe;
# a plain `go build` still self-reports via the toolchain's VCS stamps.
export CORRAL_VERSION := `git describe --tags --always --dirty 2>/dev/null || echo dev`
_ldflags := "-X github.com/tuna-os/corral/cmd.version=" + CORRAL_VERSION

_default:
    @just --list

# Build everything (both tag sets).
build:
    go build -ldflags "{{_ldflags}}" ./...
    go build -tags bootc -ldflags "{{_ldflags}}" ./...

# Install corral + the bootc plugin into ~/.local/bin and the plugin dir.
# This is the supported "get current" path — run it after `git pull`.
install:
    go build -ldflags "{{_ldflags}}" -o ~/.local/bin/corral .
    go build -tags bootc -ldflags "{{_ldflags}}" -o "${XDG_DATA_HOME:-$HOME/.local/share}/corral/plugins/corral-bootc" ./cmd/corral-bootc
    go build -ldflags "{{_ldflags}}" -o "${XDG_DATA_HOME:-$HOME/.local/share}/corral/plugins/corral-incus" ./cmd/corral-incus
    @echo "✓ installed: $(~/.local/bin/corral version)"

# Run the full test suite (both tag sets), race detector on, order shuffled.
test:
    go test -race -shuffle=on -count=1 ./...
    go test -race -shuffle=on -count=1 -tags bootc ./...

# The generated layer, built inside a real bootc image. Needs podman or docker
# and about a minute — no KVM, no root. Run this before touching the shell
# pkg/vmtest generates: it is where three CI failures in a row actually lived.
vmtest-layer image="quay.io/fedora/fedora-bootc:41":
    CORRAL_VMTEST_IMAGE={{image}} go test -tags e2elayer -timeout 15m -count=1 -v ./pkg/vmtest/

# The whole bootc VM tester against a real image. Needs root (or --sudo), KVM,
# and podman: it pulls a multi-gigabyte image and boots it. Minutes, not seconds.
vmtest-e2e image="quay.io/fedora/fedora-bootc:41":
    sudo -E CORRAL_VMTEST_IMAGE={{image}} go test -tags e2evmtest -timeout 45m -count=1 -v ./pkg/vmtest/

# Coverage with the ratchet gate CI runs (.coverage-budget).
cover:
    go test -count=1 -coverprofile=cover.out ./...
    scripts/coverage-gate.sh cover.out

# Format Go sources in place.
fmt:
    gofmt -w pkg cmd .

# Static analysis (both tag sets).
vet:
    go vet ./...
    go vet -tags bootc ./...

# Lint (both gates CI runs). Needs golangci-lint built with this repo's Go:
#   go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
lint:
    golangci-lint run --disable=errcheck ./...
    golangci-lint run --default=none --enable=errcheck --new-from-merge-base=origin/main ./...

# The local pre-push gate — mirrors CI's `test` and `lint` jobs.
ci: fmt-check vet build test lint

# Fail if anything isn't gofmt-clean (what CI checks).
fmt-check:
    @unformatted="$(gofmt -l pkg cmd .)"; \
      if [ -n "$unformatted" ]; then echo "gofmt needed on:"; echo "$unformatted"; exit 1; fi

# Run the web UI locally against the current kube context.
web addr="127.0.0.1:8006":
    go run -tags bootc . web --addr {{addr}}

# Refresh the ublue/bluefin/tuna bootc catalog from ghcr (drops >60d-stale). Needs gh + curl.
regen-catalog:
    python3 scripts/regen-catalog.py
    gofmt -w pkg/catalog/catalog_generated.go
    go build ./... && go test ./pkg/catalog/
    @echo "✓ catalog regenerated — review 'git diff pkg/catalog/catalog_generated.go'"
