# ADR-0004: Web UI adopts Alpine.js, no build step, migrated island-by-island

**Status:** accepted (first island implemented)
**Date:** 2026-07-01

## Context

The web UI is a single 1,732-line vanilla `app.js` with no build step — served
via `go:embed`, no bundler, no node toolchain. That's deliberate: corral is a
single static Go binary, and the frontend has always matched that (cf. the
creds-leak cleanup that keeps secrets out of the client bundle entirely).

As the feature backlog lands (namespace/pool tree grouping, per-VM options
editor, ISO/template library, the Containers subsystem), the single-file
imperative-DOM style stops scaling: every new feature is another few hundred
lines of manual `document.querySelector` wiring, manual `innerHTML` string
templating, and hand-rolled polling loops with no shared pattern for "here's
some state, keep the DOM in sync with it."

### Why Alpine, not htmx or a SPA

- The UI is a **JSON API consumed by a JS client**, and that JSON API is the
  **single shared surface for the TUI, CLI, and Proxmox compat layer**.
  Alpine preserves that split — it's client-side reactivity over the existing
  `/api/*` JSON responses. htmx would want the server to render HTML
  fragments, forking a parallel rendering path in Go that the TUI/CLI/Proxmox
  layer don't share.
- Live islands (noVNC, xterm, websocket task log, metrics charts) need
  imperative client JS regardless of framework choice — Alpine sits alongside
  them without needing to own the whole page.
- A bundler/SPA framework would add a node toolchain + npm dependency tree to
  a project that deliberately has none — single Go binary, `go:embed` static
  assets, nothing to `npm install` before the server runs.

## Decision

**Adopt Alpine.js, vendored as one file, no CDN, no bundler.** Migrate
island-by-island — each island is a self-contained piece of the page (a
panel, a dialog, a list) converted to `x-data`/`x-for`/`x-show` at its own
pace, while the rest of `app.js` stays exactly as imperative as it is today.
Transitional state (part Alpine, part imperative DOM) is expected and
accepted — that's the point of island-by-island: each step ships on its own.

### Vendoring

`pkg/web/static/alpine.min.js` — Alpine.js 3.15.12's CDN build, fetched once
and committed (not `npm install`ed — there's no `package.json` here and this
ADR isn't introducing one). `index.html` loads it with a plain `<script
defer src="alpine.min.js">`, alongside the existing xterm.js CDN includes.
`go:embed` picks it up automatically via the existing `pkg/web/static/*`
embed pattern — no build-step change.

### First island: the task panel

The Proxmox-style task-activity panel (`#task-panel` in index.html,
`refreshTaskLog`/the panel-collapse toggle in app.js) is the pattern-setter.
Picked over the create wizard because it's small and self-contained — a
poll loop, a list render, a collapse toggle — with no cross-island state
sharing, so it's a clean example to point future migrations at without also
having to explain wizard-specific complexity.

Converted:
- Manual `innerHTML` row templating → `x-for="task in tasks"` template.
- Manual `.onclick` collapse toggle → `x-data`'s `collapsed` boolean +
  `@click` + `:class`.
- The existing 5-second poll loop stays exactly as it was (`setInterval`
  calling `fetch`) — Alpine manages the render, not the polling; no reason
  to invent an Alpine-specific timer pattern when `setInterval` already does
  the job.

### Convention for future islands

1. Keep the poll/fetch logic exactly as imperative as it already is — Alpine
   is for **render**, not for data-fetching. Don't reach for `x-init` +
   fetch when a plain `setInterval` updating an Alpine-managed data object
   already works.
2. Scope `x-data` to the smallest wrapping element that makes sense (the
   panel/dialog/section root) — don't hoist state to `<body>`. Islands stay
   islands.
3. Prefer `x-for`/`x-show`/`x-text`/`:class` over hand-built `innerHTML`
   strings — that's the entire point (no more `esc()`-and-concatenate
   templating, no more manually finding-and-replacing DOM nodes).
4. Leave untouched code untouched. A migrated island's neighbors don't need
   to change just because they're nearby — island-by-island means partial
   completion is the steady state, not a TODO.

## Consequences

- No new build tooling, no `package.json`, no npm — `go build` still
  produces the whole thing.
- The JSON API is unchanged; this is purely a client-side rendering change.
- The rest of `app.js` stays imperative until (and unless) something touches
  it for other reasons — that's accepted debt, not a regression, per the
  "island by island" framing above.
