#!/usr/bin/env bash
# Build the browser-only demo site (#284) into $1 (default: dist/web-demo).
# The result is plain static files: host them on any web server, such as
# GitHub Pages. See web-demo/README.md.
set -euo pipefail

out="${1:-dist/web-demo}"
root="$(cd "$(dirname "$0")/.." && pwd)"

rm -rf "$out"
mkdir -p "$out/_demo"
out="$(cd "$out" && pwd)"
cp "$root/web-demo/index.html" "$root/web-demo/sw.js" "$out/"
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/_demo/"
(cd "$root" && GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o "$out/_demo/corral.wasm" ./cmd/corral-wasm)
# GitHub Pages runs Jekyll unless told not to, and Jekyll drops _demo/.
touch "$out/.nojekyll"
echo "web demo built in $out ($(du -sh "$out" | cut -f1))"
