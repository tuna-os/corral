# Corral task runner. `just` (https://github.com/casey/just) — run `just` to list.
# Build tags: most code is plain; the bootc plugin needs `-tags bootc`.

_default:
    @just --list

# Build everything (both tag sets).
build:
    go build ./...
    go build -tags bootc ./...

# Run the full test suite (both tag sets), race detector on.
test:
    go test -race -count=1 ./...
    go test -race -count=1 -tags bootc ./...

# Format Go sources in place.
fmt:
    gofmt -w pkg cmd .

# Static analysis (both tag sets).
vet:
    go vet ./...
    go vet -tags bootc ./...

# The local pre-push gate — mirrors CI's `test` job.
ci: fmt-check vet build test

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
