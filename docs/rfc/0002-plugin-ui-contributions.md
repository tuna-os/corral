# RFC-0002: Plugin contributions to the web UI

**Status:** Proposal. This RFC changes no code. It asks for a decision on
the plugin contract, which ADR-0007 owns.
**Date:** 2026-10-08
**Author:** drafted from a live session with James Reilly + Claude

## Summary

Let a plugin declare what it adds to the web UI, and let corral draw it. The
plugin sends data. Corral owns the markup.

Today a plugin can add a CLI command and declare a capability. It cannot add
anything to the web UI. Every screen, tree section, dock panel and dashboard
widget is a list in the embedded JavaScript. A plugin becomes visible only
when somebody edits core.

## Why now

The host-power hook is the evidence. ADR-0007 keeps provider code out of
core, and the Go side holds to that. Core calls any plugin that declares the
`host-power` capability, and knows nothing about AWS or Wake-on-LAN. The web UI
does not hold to it. Six core files carry 43 lines that exist only for that
one capability:

| File | Lines about host-power | After stage 0 |
|---|---|---|
| `pkg/web/static/content/datacenter.js` | 11 | none |
| `pkg/web/static/tree.js` | 10 | none |
| `pkg/web/static/menus.js` | 8 | none |
| `pkg/web/static/content/hostpower.js` | 7 | the capability itself |
| `pkg/web/static/app.js` | 5 | one import |
| `pkg/web/static/state.js` | 2 | none |

The second capability costs the same again, and so does the third. The
marketplace can carry plugins that corral cannot show.

Stage 0 has since moved all of that into the capability's own module. Core now
holds one line: the import that loads it. The rest of this RFC is about the
contract a plugin speaks, which stage 0 did not change.

## The borrowed idea

KubeStellar's UI takes a manifest from each plugin. One file declares the
backend routes, the navigation entries with a position and an order, the
widgets, and the permissions the plugin asks for. The host reads the manifest
and places the plugin itself.

That part is worth taking. **A plugin declares where it appears, and the host
places it.**

A core edit for each capability then becomes one line of metadata.

## What this RFC does not take

KubeStellar also loads plugin code into the page: a WASM module and a
`plugin-component.js` for each route. Corral should not.

- ADR-0004 keeps this UI as native ES modules with no build step, and
  `go:embed` ships every byte of it. Code from a marketplace would arrive by a
  different path with different trust.
- A plugin that supplies markup or script can read the session. It can also
  rewrite any screen, and call any API as the operator. Install then means
  "run this author's code in my console", not "add a command".
- corral already states what a plugin may do, in words the installer confirms.
  Arbitrary script makes that list meaningless.

So the proposal is narrower on purpose: **the plugin sends data, corral draws
it.**

## Proposed contract

A plugin adds a `ui` section to the metadata it already returns from
`--corral-plugin-metadata`:

```json
{
  "name": "aws-power",
  "capabilities": ["host-power"],
  "permissions": ["corral:read:nodes"],
  "ui": {
    "widgets": [
      { "id": "aws-spend", "title": "AWS spend", "command": "ui-widget aws-spend", "refresh": "60s" }
    ],
    "sections": [
      { "id": "aws-hosts", "title": "AWS hosts", "icon": "server", "command": "ui-section aws-hosts" }
    ]
  }
}
```

Corral runs the named command and reads one typed document on stdout. Corral
accepts a closed set of shapes, and escapes every string:

```json
{ "kind": "rows", "rows": [ { "label": "This month", "value": "$41.20", "state": "ok" } ] }
{ "kind": "table", "columns": ["Host", "State"], "rows": [["i-0abc", "stopped"]] }
{ "kind": "message", "text": "No instances carry the corral tag." }
```

`state` is one of `ok`, `warn`, `bad` or `muted`, and selects a pill that
corral already styles. A shape corral does not know is an error, and the
widget says so. There is no HTML and no script in any of it.

Actions reuse what exists. A row may carry an `action` that names a plugin
command, and corral confirms it the way it confirms every mutation. The
permissions the installer accepted still gate it.

## How it fits the current UI

`docs/web-ui-extension-points.md` lists the extension points: the dock
`PANELS` list, the dashboard `widgets` map, `TREE_VIEWS`, and the
`renderContent()` branch table. Each is an author-time list today. This RFC
makes two of them accept entries at run time, and leaves the rest alone.

Stage it:

0. **Done.** `pkg/web/static/ui/capabilities.js` holds a registry that accepts
   entries at run time, and host-power registers with it. The plugin contract
   did not change.
1. Add the `ui` metadata section and the typed documents. Teach the Extensions
   screen to name what a plugin adds, so install shows more than a name.
2. Let the marketplace carry a plugin whose only purpose is a screen.

Stage 0 was worth doing on its own, and it paid for itself in core code that
went away. Stages 1 and 2 remain a decision for a maintainer, because they
change the contract in ADR-0007.

Stage 0 needed browser coverage for the capability it moves, and demo mode had
none, because it installs no plugins. `pkg/web/hostpower_demo.go` now reports
machines in demo mode, so `scripts/ui-smoke.mjs` drives the tree rows, the
widget and the detail screen that the refactor touches. That fixture is not a
provider and does not change this contract.

## Risks

- **A slow plugin blocks a widget.** Each call needs a timeout. The widget
  also needs a state for "this plugin did not answer". The host-power hook
  uses 30 s; a widget wants much less.
- **A plugin can flood a screen.** The row and column counts need limits.
- **A typed document is still a contract.** Once a plugin ships against
  `kind: rows`, that shape is public and hard to change.
- **Stage 0 alone adds indirection.** One consumer does not justify a plugin
  API. It justifies a registry, which is smaller.

## Open questions

1. Does a plugin screen belong in the sidebar tree, or only under Extensions?
   The tree is the operator's model of the fleet, and a screen about cost is not
   part of a fleet.
2. Should a plugin widget appear on the dashboard by default, or wait in "Add
   widget" until somebody asks for it?
3. Is `kind: table` enough, or does the first real plugin need a chart? A
   chart means a data contract for series, which is a larger commitment.
