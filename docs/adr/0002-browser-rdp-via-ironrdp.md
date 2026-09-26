# ADR-0002: RDP support — detection now, in-browser IronRDP later

**Status:** accepted (phase 1 implemented)
**Date:** 2026-06-12

## Context

Corral already gives every VM an in-browser VNC console (noVNC) and serial
terminal (xterm.js). RDP is the better desktop protocol where it's
available — and it's available more widely than Windows alone:

- **Windows VMs** (corral-windows plugin) run RDP natively; the plugin's
  `--rdp` flag already exposes 3389 through the corral proxy service.
- **Modern Linux desktops** ship RDP servers: GNOME Remote Desktop
  (FreeRDP-based, default remote-desktop path since GNOME 46) and xrdp.
  A Fedora/Ubuntu desktop VM is likely to answer on 3389.

We want corral to notice RDP wherever it runs and offer it, and
eventually to render RDP in the browser like it does VNC.

For the in-browser part the candidate is **IronRDP** (Devolutions' Rust RDP
implementation) compiled to WebAssembly. It is the only maintained
RDP client that can run in a browser. The alternative, Apache Guacamole, needs the
`guacd` C daemon plus a Java servlet — far too heavy for corral's
single-binary architecture.

## Decision

### Phase 1 (implemented)

1. **Detection** — `GET /api/vms/{ns}/{name}/rdp` probes TCP 3389 on the
   VM's pod IP (1.5 s timeout) and reports `{"open": bool}`. Works for any
   guest OS; no labels or guest agent needed. The VM Summary panel shows an
   RDP row with connection instructions when the port answers.
2. **Transport** — `GET /api/rdp/{ns}/{name}` is a binary websocket that
   bridges to the VM's 3389 via `virtctl port-forward`, the same pattern as
   the VNC bridge. Raw RDP-over-websocket: a local wsproxy
   or any websocket-capable RDP client can use it today. It is also the
   transport the future browser client will ride.

### Phase 2 (planned, not yet implemented)

In-browser RDP using IronRDP's WASM build. Honest sizing, from a read of
IronRDP's web client:

- `ironrdp-web` / the `iron-remote-desktop-rdp` web component does **not**
  speak raw RDP over a websocket. It expects a proxy that
  speaks Devolutions' **RDCleanPath** protocol. That protocol is a small
  exchange of DER-encoded PDUs. In it, the proxy dials the target and does the
  TLS handshake with the RDP server. Then it relays the upgraded stream +
  server cert chain back to the browser.
- So phase 2 = two steps. First, write an RDCleanPath endpoint in Go on the
  existing bridge (TLS dial to the guest's 3389, PDU frames). Then embed
  the IronRDP web component. The component is npm-distributed. Corral's
  no-build-step rule means we vendor the built ESM/WASM artifacts under
  `pkg/web/static/vendor/`. A CDN load is possible, but it pins us to
  jsdelivr availability for a console feature.
- NLA/CredSSP: IronRDP supports it. The user types credentials into the
  browser component, and the component uses them in the CredSSP exchange.
  Corral never stores them.

## Consequences

- Corral discovers any VM with an RDP server and gives it connection
  instructions today. The code hardcodes nothing OS-specific.
- The websocket bridge is already the right transport for phase 2 — the
  RDCleanPath work is additive, no rework.
- Until phase 2 lands, browser users connect with a native RDP client via
  `virtctl port-forward` (or the windows plugin's `--rdp` proxy service).
  The UI says exactly that.

## Update (2026-07-01)

`rdpBridge` and `vncBridge` (pkg/web) today shell out to `virtctl`
directly with no seam and no tests. An architecture review flagged this,
alongside a concrete bug it caused. Bootc VMs never got RDP exposed on the
tailnet, because the port lists at each VM-creation call site had drifted out
of sync. We are now moving the bridge behind a deeper `pkg/kubevirt.ConsoleDialer`
seam (`Dial(ns, name, console) (net.Conn, error)`, real adapter wraps
`virtctl`, fake adapter for tests). This doesn't change the phase 2 decision
above — RDCleanPath, `ironrdp-web`, vendored WASM. It changes which
internal seam that work sits on: phase 2 builds on `ConsoleDialer`, not on
today's inline `exec.Command` calls.
