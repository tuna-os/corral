#!/usr/bin/env bash
# Smoke-test the Corral web API against a running deployment, to verify features
# actually work end-to-end. Read-only — it never mutates VMs.
#
#   scripts/smoke-web.sh [BASE_URL]
#
# BASE_URL defaults to https://corral.manatee-basking.ts.net. Exits non-zero if
# any check fails.
set -uo pipefail

BASE="${1:-https://corral.manatee-basking.ts.net}"
fail=0

check() { # name  expected-code  method  path
  local name=$1 want=$2 method=${3:-GET} path=$4
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' -X "$method" "$BASE$path")
  if [ "$code" = "$want" ]; then
    printf '  \033[32m✓\033[0m %-22s %s\n' "$name" "$code"
  else
    printf '  \033[31m✗\033[0m %-22s got %s, want %s  (%s %s)\n' "$name" "$code" "$want" "$method" "$path"
    fail=1
  fi
}

echo "Corral web smoke test → $BASE"
echo "Cluster-wide:"
check "UI"            200 GET /
check "vms"           200 GET /api/vms
check "nodes"         200 GET /api/nodes
check "capabilities"  200 GET /api/capabilities
check "instancetypes" 200 GET /api/instancetypes
check "datavolumes"   200 GET /api/datavolumes
check "nads"          200 GET /api/nads

# Pick the first VM to exercise per-VM endpoints.
vm=$(curl -s "$BASE/api/vms" | python3 -c 'import json,sys
d=json.load(sys.stdin)
print(f"{d[0]["namespace"]}/{d[0]["name"]}" if d else "")' 2>/dev/null)

if [ -n "$vm" ]; then
  echo "Per-VM ($vm):"
  check "vm info"   200 GET "/api/vms/$vm"
  check "events"    200 GET "/api/vms/$vm/events"
  check "metrics"   200 GET "/api/vms/$vm/metrics"
  check "snapshots" 200 GET "/api/vms/$vm/snapshots"
  # guestinfo is 200 with the agent, 503 without — both mean the route works.
  gi=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/vms/$vm/guestinfo")
  case "$gi" in 200|503) printf '  \033[32m✓\033[0m %-22s %s\n' "guestinfo" "$gi" ;;
    *) printf '  \033[31m✗\033[0m %-22s %s\n' "guestinfo" "$gi"; fail=1 ;; esac
else
  echo "  (no VMs to exercise per-VM endpoints)"
fi

[ "$fail" = 0 ] && echo "All checks passed." || echo "Some checks FAILED."
exit "$fail"
