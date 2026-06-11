# Corral web UI image — runs `corral web` in-cluster.
# Binaries are cross-compiled/downloaded into build/ first (see README):
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/corral .
#   curl -fsSLo build/kubectl https://dl.k8s.io/release/v1.36.1/bin/linux/amd64/kubectl
#   curl -fsSLo build/virtctl https://github.com/kubevirt/kubevirt/releases/download/v1.8.2/virtctl-v1.8.2-linux-amd64
# No RUN steps, so it cross-builds from any host arch:
#   podman build --arch amd64 -t ghcr.io/hanthor/corral:latest -f Containerfile .
FROM docker.io/library/alpine:3.21

# BIN selects the corral binary to ship: "corral" (default, lean) or
# "corral-bootc" (built with -tags bootc) for the optional bootc plugin.
ARG BIN=corral
COPY --chmod=755 build/${BIN}   /usr/local/bin/corral
COPY --chmod=755 build/kubectl  /usr/local/bin/kubectl
COPY --chmod=755 build/virtctl  /usr/local/bin/virtctl

ENV HOME=/tmp
EXPOSE 8006
ENTRYPOINT ["/usr/local/bin/corral", "web", "--addr", "0.0.0.0:8006"]
