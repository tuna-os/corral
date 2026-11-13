#!/usr/bin/env bash
# ci-scope.sh: decide which path-scoped lanes of ci.yml a change can affect.
#
# Reads the changed paths (one per line) on stdin and prints one
# "<scope>=true|false" line per scope, ready for $GITHUB_OUTPUT. With --all,
# prints every scope as true (nightly, a dispatch on main, the run-heavy label,
# an unreadable push range).
#
# A change to this script or to ci.yml selects every scope.
#
#   code   test + lint: anything that is not prose or repository metadata.
#          docs/backend-parity.md is code: pkg/backend's tests read it.
#   e2e    e2e-kind: Go that the `corral web` under test can run, the served UI,
#          the e2e specs and the deploy manifests. It drops tests, the TUI, the
#          plugin binaries (cmd/corral-*) and the pkg/ trees the corral binary
#          does not import (`go list -tags bootc -deps .`).
#   ui     ui-smoke: the e2e set plus the web client and its demo cluster.
#   qemu   native-qemu-lifecycle (macOS, arm64 and amd64 runners): the QEMU
#          backend and the CLI paths it drives.
#   image  web-image-smoke: the container image and the binary it ships.
#   prose  ste: Markdown, which the ste-lint budget measures.
set -euo pipefail

scopes=(code e2e ui qemu image prose)
declare -A hit=()

select_all() { local s; for s in "${scopes[@]}"; do hit[$s]=1; done; }

if [ "${1:-}" = "--all" ]; then
    select_all
else
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        case "$f" in
            .github/scripts/ci-scope.sh | .github/workflows/ci.yml) select_all ;;
        esac
        # code: everything except prose and repository metadata. Other
        # workflows have their own triggers and are not read by any test.
        case "$f" in
            docs/backend-parity.md) hit[code]=1 ;;
            docs/* | *.md | *.mdx | .ste-budget | LICENSE | .github/CODEOWNERS | .github/ISSUE_TEMPLATE/* | \
                .github/actionlint.yaml | .github/zizmor.yml | .github/workflows/actions.lock) ;;
            .github/workflows/*) [ "$f" = .github/workflows/ci.yml ] && hit[code]=1 ;;
            *) hit[code]=1 ;;
        esac
        # e2e: the filter e2e.yml used to carry.
        case "$f" in
            *_test.go | cmd/tui*.go | cmd/corral-*/* | pkg/device/* | pkg/lifecycle/* | pkg/schedule/* | pkg/testenv/*) ;;
            *.go | go.mod | go.sum | e2e/* | pkg/web/static/* | deploy/* | Containerfile | scripts/e2e-vdi.sh)
                hit[e2e]=1; hit[ui]=1 ;;
        esac
        case "$f" in
            pkg/web/* | pkg/demo/* | scripts/ui-smoke.mjs) hit[ui]=1 ;;
        esac
        case "$f" in
            pkg/qemu/* | pkg/types/* | pkg/shell/* | pkg/registry/* | pkg/config/* | \
                cmd/create.go | cmd/delete.go | cmd/wait.go | cmd/ops.go | cmd/helpers.go | cmd/commands.go | \
                cmd/root.go | cmd/context.go | main.go | go.mod | go.sum) hit[qemu]=1 ;;
        esac
        case "$f" in
            Containerfile | scripts/smoke-web-image.sh | go.mod | go.sum | deploy/* | pkg/web/* | cmd/web*.go | main.go) hit[image]=1 ;;
        esac
        case "$f" in
            *.md | *.mdx | .ste-budget) hit[prose]=1 ;;
        esac
    done
fi

for s in "${scopes[@]}"; do
    if [ -n "${hit[$s]:-}" ]; then echo "$s=true"; else echo "$s=false"; fi
done
