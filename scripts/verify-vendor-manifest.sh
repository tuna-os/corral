#!/usr/bin/env bash
# Verify that all vendored browser assets match their recorded digests in MANIFEST.json.
# This ensures vendored files cannot drift or be edited in-place undetected.

set -euo pipefail

MANIFEST="pkg/web/static/vendor/MANIFEST.json"
STATIC_DIR="pkg/web/static"

if [[ ! -f "$MANIFEST" ]]; then
    echo "error: $MANIFEST not found"
    exit 1
fi

# Use jq to iterate over assets and verify their digests.
# For assets outside vendor/ (like alpine.min.js), resolve relative to STATIC_DIR.
errors=0
while IFS= read -r line; do
    path=$(echo "$line" | jq -r '.path')
    sha256=$(echo "$line" | jq -r '.sha256')
    bytes=$(echo "$line" | jq -r '.bytes')
    
    file_path="$STATIC_DIR/$path"
    
    if [[ ! -f "$file_path" ]]; then
        echo "error: $path not found (expected at $file_path)"
        ((errors++))
        continue
    fi
    
    # Verify SHA-256 digest
    actual_sha256=$(sha256sum "$file_path" | awk '{print $1}')
    if [[ "$actual_sha256" != "$sha256" ]]; then
        echo "error: $path digest mismatch"
        echo "  expected: $sha256"
        echo "  actual:   $actual_sha256"
        ((errors++))
        continue
    fi
    
    # Verify file size
    actual_bytes=$(stat --format=%s "$file_path")
    if [[ "$actual_bytes" != "$bytes" ]]; then
        echo "error: $path size mismatch"
        echo "  expected: $bytes bytes"
        echo "  actual:   $actual_bytes bytes"
        ((errors++))
        continue
    fi
    
    echo "ok: $path ($actual_bytes bytes)"
done < <(jq -c '.assets[]' "$MANIFEST")

if [[ $errors -gt 0 ]]; then
    echo ""
    echo "error: $errors asset(s) failed verification"
    exit 1
fi

echo ""
echo "✓ All vendored assets verified successfully"
