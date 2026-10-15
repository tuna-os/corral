# VDI Epic Status & Dependency Chain (#69)

This document records the dependency chain, current implementation state, and hardware/media gating status for the VDI epic ([issue #69](https://github.com/tuna-os/corral/issues/69)).

## Status Summary

The VDI epic waits on hardware and media prerequisites (`#129` and `#132`). The core Phase 1 desktop pool primitives (`pkg/vdi` and `cmd/corral-vdi`) have landed, and so have the platform adapters that support them (`pkg/snapshot`, `pkg/export`, `pkg/lifecycle`, `pkg/schedule`, `pkg/doctor`). The unit tests in CI give them full coverage.

## Hardware & Media Blockers

1. **GPU / Device Passthrough (`#129`)**:
   - Software-rendered desktops are a demonstration, not high-performance VDI.
   - Blocked on hosts with spare passthrough-capable devices. If you bind a GPU to `vfio-pci` on a workstation, the workstation loses its physical display.
   - The CI environment has no dedicated devices for GPU passthrough.

2. **Windows Guests (`#132`)**:
   - Blocked on non-redistributable Windows ISOs and `virtio-win` drivers (we cannot host them in public CI repositories).
   - Each verification pass needs a long unattended install (30–60 minutes).

3. **Lifecycle at Pool Scale (`#133`)**:
   - Partially unblocked. `pkg/lifecycle` handles power operations on the canonical `InstanceRef`, and `pkg/schedule` manages windowed start/stop execution across contexts.

## Landed Primitives & Adapters

- **Phase 1 VDI Plugin (`pkg/vdi`, `cmd/corral-vdi`)**: Static pools, golden VM clones via `kubevirt.Client.Clone`, label-based assignment (`corral.dev/vdi-pool`, `corral.dev/vdi-assigned-to`), and CLI lifecycle (`pool create/list/delete`, `assign`, `unassign`, `connect`).
- **Atomic claims (`#226`)**: One `coordination.k8s.io` Lease for each pool member. The first writer wins, and Corral takes over an expired claim with a `resourceVersion` compare-and-swap. A release deletes the Lease only with a `resourceVersion` precondition (fixed in #530).
- **CT-backed ephemeral pools (`#230`)**: `corral vdi ct-pool` and `pkg/vdi/ctpool.go`. A ConfigMap holds the pool definition and the recorded status. A reconciler creates, starts, replaces, and scales down CT members, and never destroys a claimed member. Claims use the `#226` Lease. A release destroys the member and creates it again from the image. A pool in persistent mode gives an explicit error. Guide: [docs/vdi-ct-pools.md](vdi-ct-pools.md).
- **Self-service broker (`#227`)**: Opt-in `/api/vdi/` routes in `corral web` ([ADR-0013](adr/0013-vdi-broker-placement.md)). An authenticated user claims, starts, inspects, and releases their own desktop from a VM or CT pool. The caller comes from the trusted identity headers, and the holder of the member's Lease is the owner. Admins in `CORRAL_ADMINS` may act on any desktop. Guide: [docs/vdi.md](vdi.md#self-service-broker).
- **In-Browser RDP Proxy**: RDCleanPath proxy that supports IronRDP (ADR-0002 Phase 2).
- **Disk Snapshot Adapters (`#134`)**: Multi-backend snapshot/restore/retention in `pkg/snapshot`.
- **Export Adapters (`#131`)**: Per-backend export of disk images in `pkg/export`.
- **Peer Diagnostics (`#135`)**: Diagnostics for multi-site reachability and console routes in `pkg/doctor`.

## Phase 2 Children

| Issue | Scope | State |
|---|---|---|
| `#226` | Atomic claim and release | Done |
| `#227` | Authenticated self-service broker API | Done; [ADR-0013](adr/0013-vdi-broker-placement.md) |
| `#228` | Session tracking and reclaim | Open |
| `#229` | Protocol-aware one-click connect | Open |
| `#230` | CT-backed ephemeral pools | Done (#520, #528, #530, #544, #557) |

## Roadmap Recommendation

Keep issue `#69` tracked as an epic until the hardware/media blockers in `#129` and `#132` have a resolution. `pkg/vdi` remains active in the codebase for static pool creation and manual assignment.
