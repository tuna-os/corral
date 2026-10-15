#!/usr/bin/env bash
# Keyless-signs release binaries and attests an SPDX SBOM for each, then
# verifies both with the exact signer identity before anything is published
# (docs/enterprise-readiness.md, tunaos#1187). Runs in GitHub Actions with
# `id-token: write`; no key or secret is involved.
#
#   scripts/sign-release-assets.sh <signer-identity> <binary>...
#
# Next to every <binary> it writes:
#   <binary>.sigstore.json       Sigstore bundle over the binary
#   <binary>.spdx.json           SPDX JSON SBOM (syft)
#   <binary>.sbom.sigstore.json  signed in-toto attestation: SBOM -> binary digest
#
# Needs cosign (v3) and jq on PATH; syft on PATH or in $SYFT.
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <signer-identity> <binary>..." >&2
  exit 2
fi
identity=$1
shift
issuer=${SIGNER_ISSUER:-https://token.actions.githubusercontent.com}
syft=${SYFT:-syft}
export COSIGN_YES=true

for bin in "$@"; do
  "$syft" scan "file:$bin" -o "spdx-json=$bin.spdx.json"
  jq -e '.spdxVersion and (.packages | length > 0)' "$bin.spdx.json" >/dev/null
  cosign sign-blob --bundle "$bin.sigstore.json" "$bin"
  cosign attest-blob --type spdxjson --predicate "$bin.spdx.json" \
    --bundle "$bin.sbom.sigstore.json" "$bin"
  cosign verify-blob --bundle "$bin.sigstore.json" \
    --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$bin"
  cosign verify-blob-attestation --bundle "$bin.sbom.sigstore.json" --type spdxjson \
    --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$bin"
done
