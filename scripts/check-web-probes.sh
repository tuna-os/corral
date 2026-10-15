#!/usr/bin/env bash
# Probe a running corral web instance the way a supervisor does.
#
#   scripts/check-web-probes.sh [BASE_URL] [TRIES]
#
# BASE_URL defaults to http://127.0.0.1:8006. TRIES defaults to 30.
# Exits 0 when /healthz returns "ok" and /readyz reports {"status":"ready"}.
# Exits non-zero otherwise. Read-only: it never changes state.
set -uo pipefail

BASE="${1:-http://127.0.0.1:8006}"
TRIES="${2:-30}"

for ((i = 1; i <= TRIES; i++)); do
  if [ "$(curl -s -m 3 "$BASE/healthz")" = "ok" ]; then
    break
  fi
  if [ "$i" = "$TRIES" ]; then
    echo "FAIL: $BASE/healthz never returned ok after $TRIES tries"
    exit 1
  fi
  sleep 1
done
echo "OK: $BASE/healthz returned ok"

ready="$(curl -s -m 5 "$BASE/readyz")"
if [ "$ready" = '{"status":"ready"}' ]; then
  echo "OK: $BASE/readyz reports ready"
  exit 0
fi
echo "FAIL: $BASE/readyz returned: $ready"
exit 1
