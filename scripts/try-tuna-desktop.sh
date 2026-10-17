#!/usr/bin/env bash
# try-tuna-desktop — test-drive Tuna Desktop in a VM with one command.
#
#   curl -fsSL https://raw.githubusercontent.com/tuna-os/corral/main/scripts/try-tuna-desktop.sh | bash
#
# Downloads Corral (verified), boots a digest-pinned, cosign-verified Tuna
# image in local QEMU, and opens the desktop in a browser tab over noVNC.
# Nothing is installed on your machine: stop with Ctrl-C (or `stop`) and no
# QEMU process, disk lock, or listening port remains.
#
# Options (env vars):
#   TRY_TUNA_IMAGE   OCI tag to try (default: ghcr.io/tuna-os/marlin:tuna, the
#                    v1 release tag — the `roost` flavor renamed; `marlin:roost`
#                    stays published as the same digest during the transition).
#                    The tag is resolved to a digest after pulling, the digest
#                    is cosign-verified, and the VM boots image@digest — never
#                    a floating tag.
#   TRY_TUNA_VM      VM name (default: try-tuna)
#   TRY_TUNA_MEM     guest memory (default: 4G; always unit-ful — corral
#                    appends "G" to a bare number and qemu asks for terabytes)
#   TRY_TUNA_CPUS    guest vCPUs (default: 4)
#   TRY_TUNA_DISK    guest disk (default: 40G)
#   TRY_TUNA_PORT    local port for the console (default: 8006)
#   TRY_TUNA_NO_OPEN=1  don't open a browser; just print the console URL
#
# Trust story: this pipe-to-bash fetch is TLS-only (same shape as
# scripts/install.sh). Everything it puts on your machine afterwards is
# verified: the corral binary against published SHA256SUMS (via install.sh),
# the bootc plugin against marketplace artifact checksums
# (`corral plugin install`), and the Tuna image by digest + cosign signature
# from the TunaOS image pipeline before it boots.
set -euo pipefail

IMAGE="${TRY_TUNA_IMAGE:-ghcr.io/tuna-os/marlin:tuna}"
VM="${TRY_TUNA_VM:-try-tuna}"
MEM="${TRY_TUNA_MEM:-4G}"
CPUS="${TRY_TUNA_CPUS:-4}"
DISK="${TRY_TUNA_DISK:-40G}"
PORT="${TRY_TUNA_PORT:-8006}"
WEB_PID=""

die() { echo "try-tuna-desktop: $*" >&2; exit 1; }
note() { echo "try-tuna-desktop: $*" >&2; }

case "$(uname -s)" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) die "unsupported OS: $(uname -s) (Linux/macOS only)" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch="amd64"; qemu_bin="qemu-system-x86_64" ;;
  aarch64|arm64) arch="arm64"; qemu_bin="qemu-system-aarch64" ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

# --- prerequisites: report the fix, not a stack trace --------------------

# QEMU provides the machine. The guest arch follows the host (no cross-arch
# emulation: an aarch64 host boots an aarch64 image).
if ! command -v "$qemu_bin" >/dev/null 2>&1 && ! command -v qemu-system-x86_64 >/dev/null 2>&1; then
  if [[ "$os" == "darwin" ]]; then
    die "QEMU not found — install it with: brew install qemu"
  fi
  die "QEMU not found — install it with: sudo apt install qemu-system-x86 qemu-utils (Debian/Ubuntu) or sudo dnf install qemu-kvm qemu-img (Fedora)"
fi
command -v qemu-img >/dev/null 2>&1 || die "qemu-img not found — it ships with the QEMU packages above"

# Acceleration decides whether the test drive is usable. Without it QEMU
# falls back to TCG, which boots a desktop image slowly enough to read as
# broken — say so up front.
if [[ "$os" == "linux" ]]; then
  if [[ ! -e /dev/kvm ]]; then
    note "warning: /dev/kvm is missing — the VM will run under TCG emulation and feel very slow."
    note "  fix: enable virtualization in firmware, then: sudo modprobe kvm kvm_intel (or kvm_amd),"
    note "  and add yourself to the kvm group: sudo usermod -aG kvm \$USER (log back in)."
  fi
else
  note "macOS uses Hypervisor.framework (hvf) — no extra setup."
fi

# podman builds the bootable disk from the OCI image (corral-bootc shells
# to it; privileged container ops may ask for sudo).
command -v podman >/dev/null 2>&1 || die "podman not found — install it with: sudo apt install podman (Linux) or brew install podman && podman machine init (macOS)"

# cosign verifies the image signature before anything boots from it.
command -v cosign >/dev/null 2>&1 || die "cosign not found — install it with: brew install cosign (macOS) or: go install github.com/sigstore/cosign/v2/cmd/cosign@latest (Linux)"

# Corral itself, verified the same way scripts/install.sh does it.
if ! command -v corral >/dev/null 2>&1; then
  note "installing corral (${os}/${arch}, checksum-verified)..."
  curl -fsSL https://raw.githubusercontent.com/tuna-os/corral/main/scripts/install.sh | sh -s -- >/dev/null || die "corral install failed"
  hash -r
fi
command -v corral >/dev/null 2>&1 || {
  note "corral installed but not on PATH — add ~/.local/bin to PATH, then re-run this script"
  die "corral not on PATH"
}

# The bootc plugin (verified against marketplace artifact checksums).
if ! corral plugin list 2>/dev/null | grep -q '^bootc'; then
  note "installing corral bootc plugin (checksum-verified)..."
  corral plugin install bootc || die "bootc plugin install failed"
fi
command -v corral-bootc >/dev/null 2>&1 || die "corral-bootc not on PATH after plugin install — check: corral plugin list"

stop_web() { [[ -n "$WEB_PID" ]] && kill "$WEB_PID" 2>/dev/null || true; WEB_PID=""; }
cleanup() {
  echo "" >&2
  note "stopping $VM..."
  corral stop "$VM" >/dev/null 2>&1 || true
  stop_web
  # Stop leaves nothing: no QEMU process, no listener. The disk image stays
  # so a re-drive is instant (`corral start $VM`); delete it with
  # `corral delete $VM` when done for good.
  local leftover=0
  if pgrep -f "qemu.*${VM}" >/dev/null 2>&1; then
    note "warning: a QEMU process for $VM is still alive"; leftover=1
  fi
  if curl -fsS --max-time 2 "http://127.0.0.1:${PORT}/" >/dev/null 2>&1; then
    note "warning: something still listens on 127.0.0.1:${PORT}"; leftover=1
  fi
  [[ "$leftover" == "0" ]] && note "clean: no QEMU process or listener remains."
}
stop_only() {
  cleanup
  exit 0
}

if [[ "${1:-}" == "stop" ]]; then
  stop_only
fi

# --- image: pull, pin, verify ----------------------------------------------
# The tag is resolved to an immutable digest over TLS, then
# cosign proves the digest came from the TunaOS image pipeline (keyless
# Sigstore, GitHub Actions OIDC — see tunaos docs/INSTALL.md "Verifying
# downloads"). The VM boots image@digest, so a tag move mid-drive cannot
# change what is running.
note "pulling $IMAGE..."
podman pull "$IMAGE" >/dev/null || die "image pull failed: $IMAGE"
DIGEST="$(podman image inspect --format '{{.Digest}}' "$IMAGE" 2>/dev/null)" || die "could not inspect digest for $IMAGE"
[[ -n "$DIGEST" ]] || die "empty digest for $IMAGE — refusing to boot an unpinned image"
BASE="${IMAGE%%:*}"
PINNED="${BASE}@${DIGEST}"
note "pinned: $PINNED"
cosign verify "$PINNED" \
  --certificate-identity "https://github.com/tuna-os/tunaOS/.github/workflows/reusable-build-image.yml@refs/heads/main" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" >/dev/null \
  || die "cosign verification failed for $PINNED — refusing to boot an unverified image"
note "signature verified."

# --- boot -------------------------------------------------------------------
if ! corral list 2>/dev/null | grep -q "^${VM}[[:space:]].*Running"; then
  if ! corral list 2>/dev/null | grep -q "^${VM}[[:space:]]"; then
    note "building $PINNED into VM $VM (this downloads GBs on first run)..."
    corral-bootc create "$VM" --image "$PINNED" --disk "$DISK" --mem "$MEM" --cpu "$CPUS" \
      || die "VM creation failed"
  fi
  note "starting $VM..."
  corral start "$VM" >/dev/null 2>&1 || true
  sleep 3
  corral list 2>/dev/null | grep -q "^${VM}[[:space:]].*Running" \
    || die "$VM is not Running after start — inspect with: corral logs $VM"
fi

# --- console: local-only web ---------------------------------------------------
# 127.0.0.1 keeps the endpoint off the network, and corral's web server
# refuses console WebSocket upgrades from any foreign Origin by default, so
# a foreign browser tab cannot drive the VNC console.
if curl -fsS --max-time 2 "http://127.0.0.1:${PORT}/" >/dev/null 2>&1; then
  note "reusing corral web already on 127.0.0.1:${PORT}"
else
  WEB_LOG="$(mktemp "${TMPDIR:-/tmp}/try-tuna-web.XXXXXX.log")"
  nohup corral web --addr "127.0.0.1:${PORT}" >"$WEB_LOG" 2>&1 &
  WEB_PID="$!"
  for _ in $(seq 1 20); do
    curl -fsS --max-time 2 "http://127.0.0.1:${PORT}/" >/dev/null 2>&1 && break
    sleep 0.5
  done
  curl -fsS --max-time 2 "http://127.0.0.1:${PORT}/" >/dev/null 2>&1 \
    || { stop_web; die "corral web did not come up — see $WEB_LOG"; }
fi

# Local QEMU VMs live under the reserved "local" namespace; the console key
# is peer/context/namespace/name with peer and context empty.
CONSOLE_URL="http://127.0.0.1:${PORT}/?console=%2F%2Flocal%2F${VM}"

if [[ -z "${TRY_TUNA_NO_OPEN:-}" ]]; then
  if [[ "$os" == "darwin" ]]; then
    open "$CONSOLE_URL" 2>/dev/null || true
  elif command -v xdg-open >/dev/null 2>&1; then
    xdg-open "$CONSOLE_URL" >/dev/null 2>&1 || true
  fi
fi

cat <<EOF

==> $VM is running Tuna Desktop.
    console: $CONSOLE_URL
    (a browser tab should have opened; Ctrl-C here stops the VM cleanly)
EOF

trap cleanup INT TERM
# Foreground wait: Ctrl-C lands in cleanup, which stops the VM and the web
# server, then verifies nothing remains.
while corral list 2>/dev/null | grep -q "^${VM}[[:space:]].*Running"; do
  sleep 5
done
note "$VM stopped."
stop_web
