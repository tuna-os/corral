# RFC-0003: Enterprise backends — VMware vSphere, Nutanix, XenServer/XCP-ng, VirtualBox

**Status:** Proposal. Draft; this RFC changes no code.
**Date:** 2026-10-11
**Author:** omp + coding assistant

## Summary

Add four backends to Corral's parity contract: VMware vSphere, Nutanix
Prism Central (AHV), Citrix XenServer / XCP-ng, and VirtualBox. Each one
arrives as its own client package plus a `pkg/backend` adapter registration,
matrix columns, and context wiring, as Proxmox did in ADR-0009. This RFC picks the API per backend, names the auth and context
shape, and orders the work cheapest-first. VirtualBox goes first because it
is a local CLI runner, like other Corral backends. The
other three are HTTP clients like `pkg/proxmoxbe`.

## Why now

The five-backend fleet (qemu, kubevirt, incus, libvirt, proxmox) covers
homelab and cluster-native virtualization. Enterprise estates live on
vSphere, Nutanix, and XenServer. Many developers have VirtualBox on their
desktops; a read-only-then-lifecycle pass over those four turns Corral from
"herds your VMs" into "herds all of your VMs". The contract stays the same.
`backend.For(ref)` and the family assertions support N backends. The
conformance tests of the matrix make the docs follow the code.

## What already exists (do not rebuild)

- The ops contract: `Power`, `Restarter`, `Suspender`, `Sizer`, `Storer`,
  `Mover`, `Cloner`, `Templater`, `Tagger`, `Metricser`, `Eventer`,
  `Addresser` in `pkg/backend/ops.go`. New backends satisfy families; they
  never grow `switch backend` arms.
- The registration seam: `backend.Register` factories built from a bare
  `types.InstanceRef` (`pkg/backend/adapters.go`). Adapters must not dial
  in their constructor — capability derivation probes the type offline.
- The consumer seams: `pkg/snapshot` (one adapter per backend, typed
  `Unsupported` refusals with remedies), `pkg/device`, `pkg/export`,
  `pkg/move` (Ingester/Inspector). Each has its own registry a new backend
  joins explicitly.
- The enforcement: `pkg/backend.Matrix` plus conformance tests against
  `types.CapabilitiesForBackend` and `docs/backend-parity.md`. A new matrix
  column that drifts from the adapter fails CI, which is the point.
- The two client shapes: command runners behind `shell.Runner`
  (`pkg/libvirt` via `virsh` is the template). HTTP clients sit behind
  `http.RoundTripper` with `httptest` fixtures (`pkg/proxmoxbe` is the
  template. It also sets the precedent of a 20s timeout for fleet
  aggregation).
- Context wiring: `config.ContextConfig` and a secret block for each backend
  (`ProxmoxContextConfig`) with environment-over-file precedence
  (`CORRAL_PROXMOX_TOKEN`). New backends extend this shape; they never
  invent a second one.

## Per-backend design

### 1. VirtualBox — `pkg/virtualbox` (first, S)

The only command runner of the four, and the cheapest proof that sixth-
backend mechanics work end to end.

- **Interface:** `VBoxManage` CLI over `shell.Runner`, exactly the
  `pkg/libvirt` shape. No SDK, no daemon auth, no new dependency.
- **Context:** local host only. `VBoxManage` manages the local VirtualBox;
  remote management needs the vboxwebsrv SOAP service. That service is
  out of scope for the first slice.
- **Ops mapping:** `list vms` + `showvminfo --machinereadable` (inventory,
  state, VRAM/CPUs); `startvm --type headless`; `controlvm acpipowerbutton`
  / `poweroff` (stop with graceful-then-hard fallback); `reset` (real
  restart); `pause`/`resume`; `snapshot take/delete/restore`;
  `clonevm`; `modifyvm --cpus/--memory` (offline scale);
  `guestproperty` + NAT port-forward rules for address/SSH.
- **Families day one:** Power, Restarter, Suspender, snapshot adapter,
  export via `clonevm`/`export` (appliance). Scale/volumes offline only;
  Mover is meaningless single-host (matrix `—` with reason, like qemu).
- **Consoles:** VRDE (VirtualBox's RDP) exists but the web bridge does not
  — same honest exception as Proxmox's ticket-without-bridge. Capability
  flags say no until bridged.
- **Risk:** Oracle's extension-pack license for VRDE/USB2; core
  lifecycle is GPL and unaffected. Gate VRDE work behind that note.

### 2. VMware vSphere — `pkg/vsphere` (second, M–L)

Highest demand, heaviest API. An HTTP client in the `proxmoxbe` shape.

- **Interface:** [govmomi](https://github.com/vmware/govmomi) (Apache-2.0,
  actively maintained) against `https://<vc>/sdk/vim25`. The `govc` CLI
  exists but the SDK is the supported programmatic interface. The same
  logic picked PVE's REST API over `pvesh`.
- **Auth/context:** vCenter endpoint + username + password/session ticket,
  carried like `ProxmoxContextConfig` (host, fingerprint/insecure,
  username; password from `CORRAL_VSPHERE_PASSWORD[_<CTX>]`, never the
  file). Fail in the constructor on unusable config so
  `corral context add` rejects immediately, per the `proxmoxbe.New`
  precedent.
- **Ops mapping:** Power/Restarter via power ops. Suspender via
  `SuspendVM`. Sizer via reconfig (hot-add when guest tools allow,
  else offline). Mover via RelocateVM/vMotion. vSphere does live
  migration as a routine task. Cloner via CloneVM. Templater via
  template mark. Snapshot adapter via Create/Remove/RevertSnapshot.
  Quiesce maps onto the offline/filesystem/crash consistency labels
  of Corral. Export via ExportVm/OVF.
- **Consoles:** MKS/webmks tickets exist; no websocket bridge yet —
  capability flags say no, matrix names the mechanism, same as Proxmox.
- **Risk:** govmomi is a large dependency. The SOAP surface changes
  across vCenter releases. Pin the module version. Record it
  like the iron-remote-desktop provenance gate. Fixture-test against
  `govmomi/simulator` (the honest substitute, same role as Proxmox's
  recorded-payload `httptest`).

### 3. Nutanix Prism Central — `pkg/nutanix` (third, M–L)

HTTP client. The choice of API generation is the key decision.

- **Interface:** Prism Central REST. Two Go clients exist:
  [prism-go-client](https://pkg.go.dev/github.com/nutanix-cloud-native/prism-go-client)
  (v3 API) and `ntnx-api-golang-client` (v4 API). **Spike first:** confirm
  which API generation the target estates run. Pick one; do not carry
  both. Default assumption is v4 for new work, v3 only if the spike says
  the fleet is there.
- **Auth/context:** PC VIP endpoint + basic auth, same context-block shape
  (host, fingerprint/insecure, username; password/secret from
  `CORRAL_NUTANIX_PASSWORD[_<CTX>]`).
- **Ops mapping:** Power/Restarter via VM power ops. Mover via native live
  migration (AHV's strength — expect full marks). Cloner via VM clone.
  Snapshot adapter via volume-group / recovery-point semantics. These map
  onto Corral consistency labels. The spike must confirm guest-quiesce
  behavior before Corral claims `filesystem`. The spike sets export.
- **Structural mismatch to record, not hide:** Nutanix organizes with
  **categories**, not free-form tags. The `Tagger` family maps to
  category assignment. The matrix note records the mismatch —
  the same honesty the `Metricser` display-strings decision encodes.
- **Risk:** the v3/v4 split, and the Element-vs-Central topology of
  Prism. Scope the first slice to AHV VMs that Prism Central manages.

### 4. Citrix XenServer / XCP-ng — `pkg/xen` (fourth, M)

XenAPI client; name the package after the API, not the vendor.

- **Interface:** XenAPI (XML-RPC over HTTPS) via a thin hand-rolled client.
  It uses the `pkg/proxmoxbe` shape — no SDK dependency. The
  [XenServer SDK](https://docs.xenserver.com/en-us/xenserver/developer/sdk-guide.html)
  lists Go among its languages. But
  [xen-api-sdk](https://github.com/xapi-project/xen-api-sdk) ships only
  C/C#/Java/PowerShell/Python. The community Go forks (terra-farm,
  sarah-soo) are too small to depend on. XenAPI is plain XML-RPC
  (`session.login_with_password`, then `VM.*`/`VDI.*`/`VBD.*` calls).
  The Power/snapshot surface that Corral needs is small enough to own.
  The same logic gave a hand-rolled client for PVE's REST API, not `pvesh`.
  The `xe` CLI (with docs for XCP-ng local management) is the
  fallback shape for single-host XCP-ng. The hand-rolled client is
  the primary path because it reaches pool masters, not only localhost.
- **Auth/context:** pool-master host + username + password
  (`CORRAL_XEN_PASSWORD[_<CTX>]`), same context-block shape.
- **Ops mapping:** Power/Restarter via `VM.start/shutdown/reboot`.
  Suspender via `VM.suspend`. Mover via `VM.pool_migrate` (live when
  storage allows). Cloner via `VM.clone`/`copy`. Snapshot adapter via
  `VM.snapshot`/`revert`. Quiesce goes via the guest agent. The spike
  must confirm this before Corral claims `filesystem`. Export via `VM.export`.
- **Note:** Call it XenServer/XCP-ng everywhere user-visible. "Citrix" is
  the vendor. Each pool uses XenAPI, and XCP-ng is what
  homelabs run.
- **Risk:** if we own the XML-RPC surface, we must follow XenAPI changes.
  But the calls that Corral needs (`session.*`, `VM.*`, `VDI.*`,
  `VBD.*`) have not changed for a decade. Fixture tests against
  recorded XML-RPC payloads (`httptest`, Proxmox precedent) turn a
  platform change into a test failure, not a field report.

## Shared work per backend (checklist, not optional)

1. Client package with `New(cfg)` constructor validation + `SetClient` /
   transport seam for tests.
2. `config.ContextConfig` extension + `CORRAL_<BACKEND>_*` env precedence.
3. `backend.Register` factory + adapter. The adapter satisfies only the
   families that exist today. Add no stub methods for an interface — the
   adapters.go header forbids them.
4. Matrix column + `types.CapabilitiesForBackend` agreement + `docs/`
   table regen (conformance tests enforce all three).
5. Snapshot/device/export/move registry entries where the backend
   has them; typed `Unsupported` refusals elsewhere.
6. `corral doctor` context for the new backend + fleet-list aggregation
   with timeout.
7. Manual-pass runbook (Proxmox precedent: CI cannot run vSphere,
   Prism, or XenServer; VirtualBox can run headless in CI like QEMU).
8. Fixture test in the per-family pattern. Add no matrix column without a
   fixture test. Use govmomi/simulator for the vSphere family (PR #472).
   Use recorded XML-RPC payloads of XenAPI via httptest for Xen (PR #473).
   Use recorded JSON of Prism v4 via httptest for Nutanix (PR #474).
   Use a shell.Runner fake that asserts the exact VBoxManage argv for
   VirtualBox (pkg/virtualbox).

Conformance = consistency (matrix column, capabilities agreement, docs
table); fixtures = behavior (the adapter makes the right calls against a
fake platform). One proves the backend claims the same shape, the other
proves it acts correctly — each backend PR needs both.

## Order and effort

| Slice | Effort | Why this order |
|---|---|---|
| VirtualBox | S | CLI runner, no new deps, proves 6th-backend mechanics (register → matrix → conformance → doctor) |
| vSphere | M–L | Biggest demand; govmomi + simulator fixtures are a known quantity |
| Nutanix | M–L | API-generation spike first; categories-vs-tags needs a matrix decision |
| XenServer/XCP-ng | M | Smallest estate of the four; hand-rolled XML-RPC keeps it dependency-free |

One backend per PR. Each PR extends the matrix by one column and closes
its own gaps; no PR widens `types.Backend` or reintroduces
`switch backend` dispatch.

## Explicitly not in this RFC

- No per-backend console bridge. Tickets/probes land. The websocket
  bridge is a separate slice per backend. The web-UI bottom-dock and
  resizable-workspace work in #342/#341 already stretches that surface.
- No Containers (CT) on the new backends. CT is a KubeVirt-pod/Incus
  concept; vSphere/Nutanix/Xen/VirtualBox get VMs only.
- No `move` source/destination parity day one. Proxmox export (#163) and
  Incus publish (#164) set the pattern; new backends join it later.
- No vendored SDK without provenance. govmomi gets the iron-remote-desktop
  treatment (source, version, gate). A prism client gets it too, if the
  spike for Nutanix picks one over a hand-rolled client. XenAPI is
  hand-rolled to avoid the need for one.
- Not a VDI broker play. RFC-0001 stays backend-agnostic; these backends
  are inventory targets a VDI pool could later draw from, nothing more.
