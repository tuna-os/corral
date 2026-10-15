# WebRTC for ephemeral Linux desktop pools — spike decision record

**Status:** Defer implementation; keep VNC/RDP/TTY as the supported paths

**Date:** 2026-08-31

**Scope:** Only sessions on ephemeral Linux desktops. Explicitly out of
scope: Windows, VM-console replacement, and a production WebRTC dependency.

## Executive decision

Do not add WebRTC or Selkies to Corral yet. Selkies is a credible streamer for
container desktops, with active upkeep. Its WebRTC mode can improve interactive
video over high-latency links. However, it is not a drop-in transport for
Corral's KubeVirt VM consoles. It captures a desktop from inside a container,
and it owns the display/input/audio stack. It also adds ICE, TURN,
media-image, GPU-device, and authentication operations that Corral would have
to operate.

The external path via the QEMU D-Bus display is a better long-term seam for
integration with VM-backed pools. But it is not a shipped Corral/KubeVirt path. QEMU now
documents `-display dbus` and its `qemu-vnc` D-Bus consumer. This spike found
no supported KubeVirt abstraction that exposes that bus to a Corral service.
If we build against it now, Corral would couple to custom
virt-launcher/QEMU configuration.

**Recommendation:** defer product implementation until two things are true. First,
an ephemeral container prototype runs on a representative GPU-enabled
cluster. Second, the KubeVirt/QEMU D-Bus path has a supported way to expose
the bus. Do not make this a required VDI path. Revisit when the pool meets the
exit criteria below.

## Evidence reviewed

We time-boxed the spike to a review of source and documentation, plus a plan
for a prototype that you can run. The checked-out Selkies `main` revision was
`7bcdd54e93b6323078cdcd93f4a8954bef0b3468` (2026-08-31). Its documentation
states:

- the reference container carries the desktop, browser and audio stack;
- WebRTC is opt-in (`SELKIES_MODE=webrtc`), while the default WebSocket mode
  uses one TCP port;
- H.264 is the WebRTC video encoder. Hardware encode uses NVENC or VA-API
  when the host exposes the GPU and driver. If not, software encode is
  possible;
- keyboard, mouse, gamepad, microphone and audio are separate media/input
  paths; and
- containerized Kubernetes deployments without host networking need STUN.
  They usually also need a TURN service that someone operates outside the
  cluster. Relay traffic adds latency.

References:

- [Selkies start guide](https://github.com/selkies-project/selkies/blob/main/docs/start.md)
- [Selkies settings](https://github.com/selkies-project/selkies/blob/main/docs/settings.md)
- [Selkies firewall/TURN guidance](https://github.com/selkies-project/selkies/blob/main/docs/firewall.md)
- [Selkies secure mode](https://github.com/selkies-project/selkies/blob/main/docs/secure-mode.md)
- [Selkies license inventory](https://github.com/selkies-project/selkies/blob/main/docs/licensing.md)

The checked-out QEMU `staging` revision was
`d2e570cc0f97b936902a5b1b86b73c0f5998b475` (2026-08-28). QEMU has a
D-Bus display interface and a documented `qemu-vnc` helper:

- [QEMU: D-Bus display interface](https://gitlab.com/qemu-project/qemu/-/blob/master/docs/interop/dbus-display.rst)
- [QEMU: VNC D-Bus helper](https://gitlab.com/qemu-project/qemu/-/blob/master/docs/tools/qemu-vnc.rst)

Corral's current architecture uses `virtctl vnc --proxy-only`, serial
console bridges, and the existing RDP bridge. This record does not change
those paths.

## Prototype design (no production dependency)

The smallest useful experiment is one **ephemeral Linux desktop in a container**,
not a VM console. Run it in a disposable namespace with a Selkies reference
image and an explicit `SELKIES_MODE=webrtc` setting. Do not add Selkies to
Corral's image catalog or runtime.

The prototype must record:

1. cold-start time, reconnect time, and time to first frame;
2. glass-to-glass latency at idle and during pointer/scroll movement;
3. video bitrate, packet loss, jitter and RTT for direct ICE and TURN relay;
4. CPU/GPU utilization, encoder selection, memory and container footprint;
5. keyboard, pointer, clipboard, resize and disconnect/reconnect behavior;
6. speaker audio and microphone behavior, with the permission boundaries;
7. authentication handoff from the broker identity to the streamer; and
8. behavior with no GPU, VA-API, and an assigned GPU device.

A run is valid only when you also measure the same desktop through the
existing Corral VNC path as a baseline. A WebRTC result without a baseline is
not evidence of improvement.

### Reproducible disposable run

The following is an experiment recipe, not a Corral feature. Pin the image
to a reviewed digest before you run it in a real cluster:

```bash
# In a disposable namespace, with a reviewed digest of the documented image:
read -rsp 'temporary spike password: ' SELKIES_PASSWORD; echo
kubectl create namespace vdi-webrtc-spike
kubectl -n vdi-webrtc-spike run selkies-spike \
  --image=ghcr.io/selkies-project/selkies/desktop:main-ubuntu26.04@<reviewed-digest> \
  --port=8080 \
  --env=SELKIES_MODE=webrtc \
  --env=SELKIES_BASIC_AUTH_USER=spike \
  --env=SELKIES_BASIC_AUTH_PASSWORD="$SELKIES_PASSWORD"
# Expose only through a private port-forward for the first run.
kubectl -n vdi-webrtc-spike port-forward pod/selkies-spike 8080:8080
```

The illustrative command needs an image digest. It uses a temporary password
that a shell variable holds; never put a real password in shell history. A
proper follow-up should use a checked-in Deployment/Secret template in a
separate experiment repository, not Corral production manifests.

This environment did not have Podman/Docker, a Kubernetes cluster, a browser,
or a GPU. So this spike does not claim that it completed the live connection
or the numerical measurements. That is an explicit result. The prototype
needs a representative cluster, and a simulation must not stand in for a
production decision.

## Comparison of integration seams

| Dimension | Selkies inside ephemeral container | External QEMU D-Bus display bridge |
|---|---|---|
| First-frame/latency | Potentially excellent with H.264/WebRTC and a local GPU; TURN can add latency | Depends on bridge and encoder; preserves guest display semantics |
| Bandwidth | Adaptive WebRTC media, but video plus audio and ICE/TURN operations | Bridge must define encoding and transport; no automatic WebRTC benefit |
| GPU | Requires `/dev/dri`, driver/libva or NVENC image compatibility and scheduling | Uses the VM's virtual/assigned display; bridge still needs an encoder |
| Input/audio | Selkies owns input injection and PulseAudio/PipeWire capture in the image | Must translate QEMU display/input/audio interfaces and permissions |
| Authentication | Selkies tokens/basic auth plus Corral/broker identity mapping | Corral controls the bridge boundary, but must secure the D-Bus socket |
| Ingress/relay | STUN/TURN, UDP policy, relay capacity and credentials are required | Can reuse Corral's existing WebSocket ingress/peer relay, but media encoding remains work |
| Operational footprint | Large desktop/media image, container privileges/devices, TURN | Custom virt-launcher/QEMU exposure and a new bridge service |
| Pool fit | Good candidate for CT-backed ephemeral pools once CT reconciliation exists | Better candidate for VM-backed pools, but upstream seam is unsettled |

## Security and operations

A WebRTC data channel is not an authorization boundary. The broker must mint
short-lived, member-scoped credentials and revoke them on release. The
streamer must reject input from viewer sessions server-side, and not only
hide browser controls. TURN credentials must be short-lived, and relay
traffic must have a capacity limit. Keep browser permissions for microphone,
webcam, clipboard and file transfer off, unless the pool explicitly needs
them.

GPU access changes the threat model. To expose `/dev/dri` or vendor devices
to a pooled container, you need three things. You need a node/device policy
and compatible image drivers. You also need a way to prevent one tenant from
use of the device outside the desktop process. A no-GPU software fallback is
necessary for correctness. But we do not assume that it meets latency or
density targets.

The peer relay and private-ingress model that Corral has now can carry the
WebRTC signal messages.
But it does not automatically provide UDP media reachability or TURN. A
WebRTC implementation must document relay topology, egress cost, metrics,
credential rotation and failure behavior. It must do this before we consider
it for a multi-user pool.

## Proceed / defer / reject criteria

**Proceed** only if a representative run shows these results against VNC:

- a material interactive improvement (target: p95 input-to-photon under
  100 ms on the intended network);
- no unacceptable input/audio regressions;
- a measurable bandwidth or quality advantage; and
- an operable private STUN/TURN/authentication design.

The image must run without privileged host access, except for the GPU devices
that the scheduler explicitly assigns to the pod. The result must also fit
the startup and density budget of the pool.

**Defer** (the current decision) if any of these is true:

- the advantage depends on a custom GPU image;
- TURN has no clear operational bounds;
- we cannot safely map the broker/session identity; or
- the D-Bus path has no supported KubeVirt exposure.

**Reject** WebRTC for this pool type if any of these is true:

- the no-GPU path, in measurements, fails to meet the interactive targets;
- audio/input or browser permission behavior is unacceptable; or
- the operation of the media and relay plane costs more than the
  latency/bandwidth benefit.

A rejection here does not affect VNC/RDP/TTY.

## Follow-up

No production implementation for Corral follows from this spike. The next artifact comes
if the required cluster becomes available. It is a disposable experiment
repository that holds the pinned image, namespace policy, and TURN
configuration. It also holds the measurement script and the VNC comparison
report. Corral should add a `streaming` capability or a broker route for
ephemeral Linux pools only after that report passes the proceed criteria.
