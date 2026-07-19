# Corral web UI image — runs `corral web` in-cluster.
# Binaries are cross-compiled/downloaded into build/ first (see README):
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/corral .
#   curl -fsSLo build/kubectl https://dl.k8s.io/release/v1.36.1/bin/linux/amd64/kubectl
#   curl -fsSLo build/virtctl https://github.com/kubevirt/kubevirt/releases/download/v1.8.2/virtctl-v1.8.2-linux-amd64
# Builds on amd64 (CI) with no emulation; for a local cross-arch build use
#   podman build --arch amd64 -t ghcr.io/tuna-os/corral:latest -f Containerfile .
FROM docker.io/library/alpine:3.24

# qemu-img powers compressed qcow2 disk exports (the default raw.gz export
# works without it). One RUN step — runs in the target arch, hence the
# --arch/binfmt note above for local cross-builds.
RUN apk add --no-cache qemu-img

COPY --chmod=755 build/corral   /usr/local/bin/corral
COPY --chmod=755 build/kubectl  /usr/local/bin/kubectl
COPY --chmod=755 build/virtctl  /usr/local/bin/virtctl

ENV HOME=/tmp
EXPOSE 8006
ENTRYPOINT ["/usr/local/bin/corral", "web", "--addr", "0.0.0.0:8006"]
