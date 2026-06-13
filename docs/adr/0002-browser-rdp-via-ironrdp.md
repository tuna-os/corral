# ADR-0002: RDP support — detection now, in-browser IronRDP later

**Status:** accepted (phase 1 implemented)
**Date:** 2026-06-12

## Context

Corral already gives every VM an in-browser VNC console (noVNC) and serial
terminal (xterm.js). RDP is the better desktop protocol where it's
available — and it's available more widely than just Windows:

- **Windows VMs** (corral-windows plugin) run RDP natively; the plugin's
  `--rdp` flag already exposes 3389 through the corral proxy service.
- **Modern Linux desktops** ship RDP servers: GNOME Remote Desktop
  (FreeRDP-based, default remote-desktop path since GNOME 46) and xrdp.
  A Fedora/Ubuntu desktop VM is likely to answer on 3389.

We want corral to notice RDP wherever it's running and offer it, and
eventually to render RDP in the browser like it does VNC.

For the in-browser part the candidate is **IronRDP** (Devolutions' Rust RDP
implementation) compiled to WebAssembly — the only maintained
browser-capable RDP client. The alternative, Apache Guacamole, requires the
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
   the VNC bridge. Raw RDP-over-websocket: usable today by a local wsproxy
   or any websocket-capable RDP client, and it is the transport the future
   browser client will ride.

### Phase 2 (planned, not yet implemented)

In-browser RDP using IronRDP's WASM build. Honest sizing, from reading
IronRDP's web client:

- `ironrdp-web` / the `iron-remote-desktop-rdp` web component does **not**
  speak raw RDP over a websocket. It expects a proxy implementing
  Devolutions' **RDCleanPath** protocol (a small DER-encoded PDU exchange
  where the proxy dials the target, performs the TLS handshake with the RDP
  server, and relays the upgraded stream + server cert chain back to the
  browser).
- So phase 2 = implement an RDCleanPath endpoint in Go on top of the
  existing bridge (TLS dial to the guest's 3389, PDU framing), then embed
  the IronRDP web component. The component is npm-distributed; corral's
  no-build-step rule means vendoring the built ESM/WASM artifacts under
  `pkg/web/static/vendor/` (CDN loading is possible but pins us to
  jsdelivr availability for a console feature).
- NLA/CredSSP: IronRDP supports it; credentials are typed into the browser
  component and used in the CredSSP exchange — corral never stores them.

## Consequences

- Any VM with an RDP server gets discovered and gets connection
  instructions today; nothing OS-specific is hardcoded.
- The websocket bridge is already the right transport for phase 2 — the
  RDCleanPath work is additive, no rework.
- Until phase 2 lands, browser users connect with a native RDP client via
  `virtctl port-forward` (or the windows plugin's `--rdp` proxy service);
  the UI says exactly that.
