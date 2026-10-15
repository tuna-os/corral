# ADR-0013: The VDI broker is an opt-in route set in `corral web`

**Status:** accepted
**Date:** 2026-10-15

## Context

RFC-0001 Phase 2 needs a broker. An authenticated user claims a desktop
from a pool, starts it, inspects it, and releases it, without an admin.
Later work tracks console sessions (#228) and adds a one-click connect
(#229). RFC-0001 left the placement open (open question 6). The
choice is a long-lived process in the `corral-vdi` plugin, or routes behind
the peer or gateway seam of the existing web server.

Three facts decide it:

- **Identity.** The web server already gets a trusted identity for each
  request. It comes from the Tailscale ingress (ADR-0003) or the
  `corral-auth` gateway (ADR-0006), and `CORRAL_ADMINS` lists the admins. A
  separate process must trust the same headers, so it needs its own
  single-ingress guarantee. It also needs a copy of `peerServiceAuth`.
- **Session signal.** #228 reclaims on console disconnect. The console
  websockets (noVNC, xterm.js, and RDCleanPath) end in `corral web`, so only
  that process sees a session start or end. A plugin broker would have to
  proxy every console stream or receive hooks from the web server.
- **Connect.** #229 hands out the existing console routes of the web
  server, with their direct-first and peer-relay transports. ADR-0002
  phase 2 and RFC-0001 rule out a second display path.

## Decision

The broker is a set of `/api/vdi/` routes in `corral web` (`pkg/web/vdi.go`).
The policy behind them lives in `pkg/vdi`, which the `corral-vdi` plugin
also uses.

- **Off by default.** `CORRAL_VDI_BROKER=1` turns the routes on. When it is
  off, every `/api/vdi/` route answers 404 and no broker code runs.
  `CORRAL_VDI_NAMESPACES` lists the namespaces whose pools the broker
  serves. The broker cannot see pool labels in any other namespace.
- **Identity from the request pipeline only.** The broker takes the caller
  from `caller()`, behind `peerServiceAuth` and `adminGate`. A request body
  or query never names the user. A Corral peer's service identity is not a
  user and cannot hold a desktop.
- **Owner-based authorization.** `adminGate` lets a non-admin send a `POST`
  under `/api/vdi/` (`selfServiceMutation`). Each such route resolves the
  caller through `vdiRoute`, which refuses anonymous callers. The broker
  then checks that the caller holds the desktop's Lease. An admin may
  inspect, start, and release any desktop. The task log records a release
  of another user's desktop as `vdi force-release`.
- **The cluster holds the state.** The broker keeps no ownership state.
  Claims are the per-member Leases of #226. VM pools and CT pools (#230)
  keep their own claim and release primitives. The broker calls them
  through the `vdi.Cluster` interface.

## Consequences

- No new process, port, or identity adapter. A deployment that already
  trusts its identity headers gets VDI self-service when it sets two
  environment variables.
- The core binary carries the VDI routes. The code is small and depends
  only on `pkg/kubevirt` and `pkg/shell`, which the core already links.
  ADR-0007 keeps optional workflows in plugins. Here the plugin keeps the
  admin workflow (`corral vdi pool`, `assign`, `ct-pool`). The web server
  only gives the self-service seam, which needs its identity and consoles.
- The `/api/vdi/` prefix is an exception to "mutations need an admin". A
  new route under it must authorize itself; `pkg/web/vdi.go` says so.
- The service account of `corral web` needs `coordination.k8s.io` Lease
  permissions (`deploy/corral-web.yaml`). For CT pools, it also needs the
  pod, Service, and PVC permissions of the CT reconciler.
- In open mode (`CORRAL_ADMINS` unset), every caller is an admin, so any
  caller can release any desktop. The broker enforces ownership only when
  an admin allowlist exists. [docs/vdi.md](../vdi.md) states this.
- A broker in a separate process stays possible later. Only `pkg/web/vdi.go`
  binds the HTTP surface to `corral web`.
