#!/usr/bin/env bash
# VDI broker end-to-end test (#227–#229) against a real Kubernetes API server
# with KubeVirt: the kind cluster of .github/workflows/e2e.yml.
#
# It starts its own `corral web` with the broker on, an admin allowlist, and a
# short maximum session, so it does not change the instance the Playwright
# suite drives. Then it checks, over HTTP with Tailscale identity headers:
#
#   claim       the Lease is created for the caller; a retry returns the same desktop
#   ownership   another user cannot inspect, connect to, or release the desktop
#   connect     the broker resolves a protocol, or says to start the desktop first
#   release     the explicit release ends the claim and stops the VM
#   reclaim     the maximum session duration reclaims a claim, and the task log says so
#   upgrade     an assignment that the previous release's `corral vdi assign`
#               wrote stays with its user: another claim does not take it, and
#               the user's claim adopts it as a Lease
#
# Usage: scripts/e2e-vdi.sh [path/to/corral]   (default ./corral)
# VDI_E2E_OLD_REF is the previous release tag (default v0.6.0). The script
# builds its corral-vdi from a git worktree of that tag.
set -euo pipefail

CORRAL=${1:-./corral}
NS=${CORRAL_NS:-corral-vms}
IMAGE=${E2E_CONTAINERDISK:-quay.io/kubevirt/cirros-container-disk-demo:latest}
PORT=${VDI_E2E_PORT:-8007}
BASE=http://127.0.0.1:$PORT
POOL=e2edesk
MAX_SESSION=150
ALICE=alice@e2e
BOB=bob@e2e
BODY=$(mktemp)
LOG=${VDI_E2E_LOG:-corral-vdi-web.log}
PID=
OLD_REF=${VDI_E2E_OLD_REF:-v0.6.0}
OLD=$(mktemp -d)
LEGACY=legacy

cleanup() {
  [ -n "$PID" ] && kill "$PID" 2>/dev/null || true
  # Wait for the VMs to go: the workflow then checks that nothing is left.
  kubectl delete vm -n "$NS" -l "corral.dev/vdi-pool=$POOL" --ignore-not-found --timeout=180s >/dev/null || true
  kubectl delete lease -n "$NS" -l "corral.dev/vdi-pool=$POOL" --ignore-not-found >/dev/null || true
  git worktree remove --force "$OLD/src" 2>/dev/null || true
  rm -rf "$BODY" "$OLD"
}
trap cleanup EXIT

fail() {
  echo "FAIL - $*"
  echo "--- response body"; cat "$BODY" || true
  echo "--- corral web log"; tail -50 "$LOG" || true
  exit 1
}
ok() { echo "ok - $*"; }

# call WHO METHOD PATH: sends the request as WHO ("" = no identity) and
# prints the HTTP status; the body goes to $BODY.
call() {
  local who=$1 method=$2 path=$3
  local id=()
  [ -n "$who" ] && id=(-H "Tailscale-User-Login: $who")
  curl -sS -o "$BODY" -w '%{http_code}' -X "$method" "${id[@]}" "$BASE$path"
}
expect() { # expect WANT GOT MESSAGE
  [ "$2" = "$1" ] || fail "$3: HTTP $2, want $1"
  ok "$3"
}
holder() {
  kubectl get lease "corral-vdi-$1" -n "$NS" --ignore-not-found -o jsonpath='{.spec.holderIdentity}'
}

# The previous release's plugin, which wrote assignments as VM labels only.
git fetch -q --no-tags --depth 1 origin "refs/tags/$OLD_REF:refs/tags/$OLD_REF"
git worktree add -q --detach "$OLD/src" "$OLD_REF"
(cd "$OLD/src" && go build -o "$OLD/corral-vdi" ./cmd/corral-vdi)
ok "built corral-vdi $OLD_REF"

for i in 1 2; do
  kubectl apply -f - >/dev/null <<EOF
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: $POOL-$i
  namespace: $NS
  labels: {corral.dev/vdi-pool: $POOL}
spec:
  runStrategy: Halted
  template:
    spec:
      domain:
        devices:
          disks: [{name: root, disk: {bus: virtio}}]
        resources: {requests: {memory: 128Mi}}
      volumes: [{name: root, containerDisk: {image: $IMAGE}}]
EOF
done

CORRAL_VDI_BROKER=1 CORRAL_VDI_NAMESPACES="$NS" CORRAL_ADMINS=root@e2e \
  CORRAL_VDI_MAX_SESSION=${MAX_SESSION}s CORRAL_DEFAULT_BACKEND=kubevirt \
  "$CORRAL" web --addr "127.0.0.1:$PORT" >"$LOG" 2>&1 &
PID=$!
for _ in $(seq 1 30); do
  curl -fsS "$BASE/api/capabilities" >/dev/null 2>&1 && break
  sleep 1
done

expect 401 "$(call "" GET /api/vdi/pools)" "an anonymous caller is refused"
expect 200 "$(call "$ALICE" GET /api/vdi/pools)" "alice lists the pools"
free=$(jq -r --arg id "vm/$NS/$POOL" '.[] | select(.id == $id) | .free' "$BODY")
[ "$free" = 2 ] || fail "pool $POOL has $free free members, want 2"
ok "the labeled VMs form a pool with 2 free members"

# ── claim ────────────────────────────────────────────────────────
start=$(date +%s)
expect 200 "$(call "$ALICE" POST "/api/vdi/pools/vm/$NS/$POOL/claim")" "alice claims a desktop"
desk=$(jq -r .id "$BODY")
member=$(jq -r .member "$BODY")
[ "$(jq -r .owner "$BODY")" = "$ALICE" ] || fail "the desktop's owner is not alice"
[ "$(holder "$member")" = "$ALICE" ] || fail "Lease corral-vdi-$member is not held by alice"
ok "Lease corral-vdi-$member is held by alice"
expect 200 "$(call "$ALICE" POST "/api/vdi/pools/vm/$NS/$POOL/claim")" "alice claims again"
[ "$(jq -r .id "$BODY")" = "$desk" ] || fail "a retried claim returned $(jq -r .id "$BODY"), not $desk"
ok "a retried claim returns the same desktop"
[ "$(kubectl get vm "$member" -n "$NS" -o jsonpath='{.spec.runStrategy}')" != Halted ] || fail "the claim did not start $member"
ok "the claim started $member"

expect 200 "$(call "$BOB" POST "/api/vdi/pools/vm/$NS/$POOL/claim")" "bob claims a desktop"
bobdesk=$(jq -r .id "$BODY")
bobmember=$(jq -r .member "$BODY")
[ "$bobdesk" != "$desk" ] || fail "bob got alice's desktop"
ok "bob gets the other member"
expect 409 "$(call carol@e2e POST "/api/vdi/pools/vm/$NS/$POOL/claim")" "an exhausted pool refuses a third user"

# ── ownership ────────────────────────────────────────────────────
expect 403 "$(call "$BOB" GET "/api/vdi/desktops/$desk")" "bob cannot inspect alice's desktop"
expect 403 "$(call "$BOB" GET "/api/vdi/desktops/$desk/connect")" "bob cannot connect to alice's desktop"
expect 403 "$(call "$BOB" POST "/api/vdi/desktops/$desk/release")" "bob cannot release alice's desktop"
[ "$(holder "$member")" = "$ALICE" ] || fail "alice lost her claim to bob"
expect 200 "$(call root@e2e GET "/api/vdi/desktops?all=1")" "the admin lists every desktop"
[ "$(jq length "$BODY")" = 2 ] || fail "the admin sees $(jq length "$BODY") desktops, want 2"

# ── connect ──────────────────────────────────────────────────────
# Under software emulation the guest may not reach Running (#77). The broker
# must then say to start it; once it runs, it must pick a console. corral web
# runs outside the pod network, so the RDP probe cannot answer here.
for _ in $(seq 1 12); do
  code=$(call "$ALICE" GET "/api/vdi/desktops/$desk/connect")
  [ "$code" = 200 ] && break
  sleep 5
done
case "$code" in
  200)
    proto=$(jq -r .protocol "$BODY")
    [ "$proto" = vnc ] || [ "$proto" = terminal ] || fail "connect chose $proto"
    jq -e '.skipped | any(startswith("rdp:"))' "$BODY" >/dev/null || fail "connect does not say why it skipped RDP"
    ok "connect resolves $proto at $(jq -r .path "$BODY"), and says why RDP was skipped"
    ;;
  409)
    grep -q "start it" "$BODY" || fail "a desktop that is not running gave no action"
    ok "connect to a desktop that has not booted says to start it"
    ;;
  *) fail "connect: HTTP $code" ;;
esac

# ── explicit release ─────────────────────────────────────────────
expect 202 "$(call "$ALICE" POST "/api/vdi/desktops/$desk/release")" "alice releases her desktop"
task=$(jq -r .task "$BODY")
for _ in $(seq 1 60); do
  call "$ALICE" GET "/api/tasks/$task" >/dev/null
  status=$(jq -r .status "$BODY")
  [ "$status" != running ] && break
  sleep 2
done
[ "$status" = "done" ] || fail "the release task ended $status: $(jq -r .error "$BODY")"
[ -z "$(holder "$member")" ] || fail "the release left Lease corral-vdi-$member"
[ "$(kubectl get vm "$member" -n "$NS" -o jsonpath='{.spec.runStrategy}')" = Halted ] || fail "the release did not stop $member"
ok "the release ended the claim and stopped $member"
expect 404 "$(call "$ALICE" GET "/api/vdi/desktops/$desk/connect")" "a released desktop cannot be connected to"

# ── reclaim at the maximum session duration ──────────────────────
deadline=$((start + MAX_SESSION + 120))
while [ -n "$(holder "$bobmember")" ]; do
  [ "$(date +%s)" -lt "$deadline" ] || fail "bob's claim was not reclaimed after ${MAX_SESSION}s"
  sleep 5
done
ok "the broker reclaimed bob's claim at the maximum session duration"
[ "$(kubectl get vm "$bobmember" -n "$NS" -o jsonpath='{.spec.runStrategy}')" = Halted ] || fail "the reclaim did not stop $bobmember"
ok "the reclaim stopped $bobmember"
expect 200 "$(call root@e2e GET /api/tasklog)" "the admin reads the task log"
jq -e --arg t "$bobdesk" 'any(.[]; .action == "vdi reclaim (max-duration)" and .user == "corral-vdi/broker" and (.target | startswith($t)) and .status == "ok")' "$BODY" >/dev/null ||
  fail "the task log has no successful max-duration reclaim of $bobdesk"
ok "the task log records the reclaim"

# ── upgrade from the previous release ────────────────────────────
# Both members are free again. The previous release assigns one to $LEGACY
# with labels and an annotation, and without a Lease.
"$OLD/corral-vdi" -n "$NS" assign "$POOL" "$LEGACY" || fail "corral-vdi $OLD_REF assign"
legacymember=$(kubectl get vm -n "$NS" -l "corral.dev/vdi-pool=$POOL,corral.dev/vdi-assigned-to=$LEGACY" -o jsonpath='{.items[0].metadata.name}')
[ -n "$legacymember" ] || fail "corral-vdi $OLD_REF assign wrote no corral.dev/vdi-assigned-to=$LEGACY label"
[ -z "$(holder "$legacymember")" ] || fail "corral-vdi $OLD_REF assign wrote a Lease"
ok "corral-vdi $OLD_REF assigned $legacymember to $LEGACY with labels only"

expect 200 "$(call "$ALICE" GET /api/vdi/pools)" "alice lists the pools after the upgrade"
free=$(jq -r --arg id "vm/$NS/$POOL" '.[] | select(.id == $id) | .free' "$BODY")
[ "$free" = 1 ] || fail "pool $POOL has $free free members after the $OLD_REF assignment, want 1"
ok "the broker does not count the $OLD_REF assignment as free"

expect 200 "$(call carol@e2e POST "/api/vdi/pools/vm/$NS/$POOL/claim")" "carol claims a desktop"
carolmember=$(jq -r .member "$BODY")
[ "$carolmember" != "$legacymember" ] || fail "carol got $legacymember, which $OLD_REF assigned to $LEGACY"
expect 409 "$(call dave@e2e POST "/api/vdi/pools/vm/$NS/$POOL/claim")" "the $OLD_REF assignment is not free for a new user"
[ "$(kubectl get vm "$legacymember" -n "$NS" -o jsonpath='{.metadata.labels.corral\.dev/vdi-assigned-to}')" = "$LEGACY" ] ||
  fail "a new claim changed the $OLD_REF assignment of $legacymember"
ok "new claims do not take $legacymember from $LEGACY"

expect 200 "$(call "$LEGACY" POST "/api/vdi/pools/vm/$NS/$POOL/claim")" "$LEGACY claims a desktop"
legacydesk=$(jq -r .id "$BODY")
[ "$(jq -r .member "$BODY")" = "$legacymember" ] || fail "$LEGACY got $(jq -r .member "$BODY"), not the $OLD_REF desktop $legacymember"
[ "$(holder "$legacymember")" = "$LEGACY" ] || fail "Lease corral-vdi-$legacymember is not held by $LEGACY"
ok "the claim of $LEGACY adopts $legacymember as a Lease"

expect 202 "$(call "$LEGACY" POST "/api/vdi/desktops/$legacydesk/release")" "$LEGACY releases the adopted desktop"
task=$(jq -r .task "$BODY")
for _ in $(seq 1 60); do
  call "$LEGACY" GET "/api/tasks/$task" >/dev/null
  status=$(jq -r .status "$BODY")
  [ "$status" != running ] && break
  sleep 2
done
[ "$status" = "done" ] || fail "the release task ended $status: $(jq -r .error "$BODY")"
[ -z "$(holder "$legacymember")" ] || fail "the release left Lease corral-vdi-$legacymember"
[ -z "$(kubectl get vm "$legacymember" -n "$NS" -o jsonpath='{.metadata.labels.corral\.dev/vdi-assigned-to}')" ] ||
  fail "the release left the $OLD_REF assignment label on $legacymember"
ok "the release ends the adopted claim and clears the $OLD_REF label"
