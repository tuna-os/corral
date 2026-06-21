# Web UI frontend: Alpine.js, no build step

As the web UI grows (pools, options editor, ISO library, backup, the CT
subsystem), the single 1,732-line vanilla `app.js` stops scaling. We adopt
**Alpine.js** — a single vendored file, **no bundler, no node toolchain** — for
declarative reactivity, and migrate the UI **island by island** rather than in
a big-bang rewrite. The app stays embedded in the Go binary via `go:embed`.

Why Alpine and not htmx, despite both being "lightweight no-build":

- The web UI's architecture is **a JSON API consumed by a JS client**, and that
  same JSON API is the **single shared surface for the TUI, CLI, and Proxmox
  compatibility layer**. Alpine *preserves* this (it consumes the JSON API);
  htmx *inverts* it (server returns HTML fragments), which would fork rendering
  into a parallel HTML layer in Go and duplicate what other clients consume as
  JSON. That shared-API property is load-bearing and must not be forked.
- The UI has inherently client-side islands — noVNC console, xterm serial,
  websocket task log, live metrics — that htmx does nothing for. Alpine sits
  alongside them; htmx would leave them hand-rolled *and* add an HTML backend.

Why not a full SPA + bundler (React/Vue + Vite): it adds a node toolchain and
npm dependency tree to a project that deliberately has none (single Go binary,
minimal supply-chain surface — cf. the #45 creds-leak cleanup). Not worth it for
this feature surface.

Consequence: a transitional period where part of the UI is Alpine-driven and
part is still imperative DOM code. Accepted — island-by-island keeps each step
shippable.
