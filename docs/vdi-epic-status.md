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
- **In-Browser RDP Proxy**: RDCleanPath proxy that supports IronRDP (ADR-0002 Phase 2).
- **Disk Snapshot Adapters (`#134`)**: Multi-backend snapshot/restore/retention in `pkg/snapshot`.
- **Export Adapters (`#131`)**: Per-backend export of disk images in `pkg/export`.
- **Peer Diagnostics (`#135`)**: Diagnostics for multi-site reachability and console routes in `pkg/doctor`.

## Roadmap Recommendation

Keep issue `#69` tracked as an epic until the hardware/media blockers in `#129` and `#132` have a resolution. `pkg/vdi` remains active in the codebase for static pool creation and manual assignment.
