# ADR-0004: Web UI adopts Alpine.js, no build step, migrated island-by-island

**Status:** accepted (first island implemented)
**Date:** 2026-07-01

## Context

The web UI is a single 1,732-line vanilla `app.js` with no build step. The Go
server serves it via `go:embed`, with no bundler and no node toolchain. That's
deliberate: corral is a single static Go binary, and the frontend has always
matched that (cf. the creds-leak cleanup that keeps secrets out of the client
bundle entirely).

The feature backlog now lands: namespace/pool tree groups, a per-VM options
editor, an ISO/template library, and the Containers subsystem. With it, the
single-file imperative-DOM style no longer scales. Every new feature is another
few hundred lines of manual `document.querySelector` wiring and manual
`innerHTML` string templates. It also adds hand-rolled poll loops with no
shared pattern for "here's some state, keep the DOM in sync with it."

### Why Alpine, not htmx or a SPA

- The UI is a **JSON API that a JS client consumes**. That JSON API is also
  the **single shared surface for the TUI, CLI, and Proxmox compat layer**.
  Alpine keeps that split, because it adds client-side reactivity over the
  existing `/api/*` JSON responses. htmx would want the server to render
  HTML fragments. That forks a parallel render path in Go that the
  TUI/CLI/Proxmox layer don't share.
- Live islands (noVNC, xterm, websocket task log, metrics charts) need
  imperative client JS for any framework choice. Alpine sits alongside
  those live islands, and it does not need to own the whole page.
- A bundler/SPA framework would add a node toolchain + npm dependency tree.
  This project deliberately has none of that. Today it is a single Go
  binary with `go:embed` static assets, and nothing to `npm install` before
  the server runs.

## Decision

**Adopt Alpine.js, vendored as one file, no CDN, no bundler.** Migrate
island-by-island. Each island is a self-contained piece of the page (a panel,
a dialog, a list). We convert each one to `x-data`/`x-for`/`x-show` at its own
pace. Meanwhile, the rest of `app.js` stays exactly as imperative as it is
today. We expect and accept a transitional state (part Alpine, part
imperative DOM). That's the point of island-by-island: each step ships on its
own.

### Vendoring

`pkg/web/static/alpine.min.js` is the CDN build of Alpine.js 3.15.12. We
fetched it once and committed it. We did not `npm install` it, because there's
no `package.json` here and this ADR doesn't add one. `index.html` loads it
with a plain `<script defer src="alpine.min.js">`, alongside the existing
xterm.js CDN includes. `go:embed` picks it up automatically via the existing
`pkg/web/static/*` embed pattern, so the build step does not change.

### First island: the task panel

The Proxmox-style task-activity panel is the pattern-setter. Its parts are
`#task-panel` in index.html, and `refreshTaskLog`/the panel-collapse toggle in
app.js. We picked it over the create wizard because it's small and
self-contained. It has a poll loop, a list render, and a collapse toggle, and
it shares no state with other islands. So it's a clean example for future
migrations to follow. It also does not make us explain the complexity that is
specific to the wizard.

Converted:
- Manual `innerHTML` row templates → `x-for="task in tasks"` template.
- Manual `.onclick` collapse toggle → `x-data`'s `collapsed` boolean +
  `@click` + `:class`.
- The existing 5-second poll loop stays exactly as it was (`setInterval`
  that calls `fetch`). Alpine manages the render, not the poll. There is no
  reason to invent an Alpine-specific timer pattern when `setInterval`
  already does the job.

### Convention for future islands

1. Keep the poll/fetch logic exactly as imperative as it already is. Alpine
   is for **render**, not for data fetches. Don't reach for `x-init` + fetch
   when a plain `setInterval` already works. That `setInterval` updates a
   data object, and Alpine manages that object.
2. Scope `x-data` to the smallest element that wraps the island and makes
   sense (the panel/dialog/section root). Don't hoist state to `<body>`.
   Islands stay islands.
3. Prefer `x-for`/`x-show`/`x-text`/`:class` over hand-built `innerHTML`
   strings. That's the entire point. No more `esc()`-and-concatenate
   templates, and no more manual find-and-replace of DOM nodes.
4. Do not touch code that has no reason to change. A migrated island's
   neighbors don't need to change because they're nearby. Island-by-island
   means partial completion is the steady state, not a TODO.

## Consequences

- No new build tools, no `package.json`, no npm. `go build` still produces
  the whole thing.
- The JSON API does not change; this is purely a change to how the client
  renders.
- The rest of `app.js` stays imperative until (and unless) something touches
  it for other reasons. That's accepted debt, not a regression, per the
  "island by island" frame above.
