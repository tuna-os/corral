#!/usr/bin/env bash
# Build the corral web container image and smoke-test its probes.
#
#   [CONTAINER_RUNTIME=podman] [IMAGE=corral-web:smoke] [PORT=18006] \
#     scripts/smoke-web-image.sh
#
# The script builds a static corral binary (CGO_ENABLED=0, like CI), stages
# build/<arch>/{corral,kubectl,virtctl} next to the Containerfile, builds the
# image, starts it, and curls /healthz and /readyz from the host. It stops and
# removes the test container on exit. It needs network access once, to fetch
# kubectl/virtctl when they are missing from build/<arch>/.
#
# Exit code is 0 only when both probes answer. No VPN or live cluster needed.
set -euo pipefail

cd "$(dirname "$0")/.."

RUNTIME="${CONTAINER_RUNTIME:-}"
if [ -z "$RUNTIME" ]; then
  if command -v podman >/dev/null 2>&1; then
    RUNTIME=podman
  elif command -v docker >/dev/null 2>&1; then
    RUNTIME=docker
  else
    echo "FAIL: need podman or docker (or set CONTAINER_RUNTIME)" >&2
    exit 1
  fi
fi

IMAGE="${IMAGE:-corral-web:smoke}"
PORT="${PORT:-18006}"
KUBECTL_VERSION="${KUBECTL_VERSION:-v1.36.1}"
VIRTCTL_VERSION="${VIRTCTL_VERSION:-v1.8.2}"

ARCH="$(go env GOARCH)"
STAGE="build/$ARCH"
mkdir -p "$STAGE"

if [ ! -x "$STAGE/corral" ]; then
  echo "Building static corral binary for $ARCH..."
  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
    go build -ldflags="-s -w" -o "$STAGE/corral" .
fi
if [ ! -f "$STAGE/kubectl" ]; then
  echo "Fetching kubectl $KUBECTL_VERSION..."
  curl -fsSLo "$STAGE/kubectl" \
    "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/$ARCH/kubectl"
fi
if [ ! -f "$STAGE/virtctl" ]; then
  echo "Fetching virtctl $VIRTCTL_VERSION..."
  curl -fsSLo "$STAGE/virtctl" \
    "https://github.com/kubevirt/kubevirt/releases/download/${VIRTCTL_VERSION}/virtctl-${VIRTCTL_VERSION}-linux-$ARCH"
fi
chmod +x "$STAGE"/corral "$STAGE"/kubectl "$STAGE"/virtctl

echo "Building image $IMAGE with $RUNTIME..."
"$RUNTIME" build -f Containerfile -t "$IMAGE" .

NAME="corral-image-smoke-$$"
cleanup() { "$RUNTIME" rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "Starting $NAME on port $PORT..."
"$RUNTIME" run -d --name "$NAME" -p "127.0.0.1:$PORT:8006" "$IMAGE" >/dev/null

if ! ./scripts/check-web-probes.sh "http://127.0.0.1:$PORT" 30; then
  echo "--- container logs ---"
  "$RUNTIME" logs "$NAME" 2>&1 | head -20
  exit 1
fi
echo "Image smoke test passed: $IMAGE serves /healthz and /readyz."
