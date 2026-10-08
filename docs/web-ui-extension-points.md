# Web UI extension points

This page shows where to add things to the web UI. It also gives three rules
that are easy to get wrong.

The UI is native ES modules with no build step (ADR-0004). Go serves
`pkg/web/static` with `go:embed`. There is no component framework. The
extension points are plain functions and lists. The shipped code already uses
each point below, so you can copy an example that works.

For where these patterns come from, and for the ones corral rejected, see
[the survey of reference interfaces](web-ui-patterns-survey.md).

A feature is done when a check in `scripts/ui-smoke.mjs` passes. That suite
drives the real UI against `corral web --demo`. It is the acceptance test for
the web epics. Nobody tests these screens by hand.

## The update model

Corral polls the fleet, and renders again when the data changed.

The poll is not a fixed timer. `pollLoop()` waits for the refresh to finish,
then waits 5 seconds plus twice the time that refresh took. Two things follow.
Corral asks a slow server less often, and nobody has to tune it. And two polls
can never be in flight together, which a fixed timer allowed as soon as the
server took longer than the interval.

`refresh()` holds that guarantee: it runs one refresh at a time. A refresh
asked for while one runs waits, and then happens. A caller that forces one has
changed something and has to see it. Do not reach past this function.

One more rule follows from the redraw:

**Do not rebuild DOM that holds state the browser owns.**

You can rebuild markup at any time. You cannot rebuild a node that holds
keyboard focus, an active drag, a text selection, a scroll position, or a
WebSocket. The browser ties all of these to the node, not to its content. If
you replace the node, the user loses them. Nothing reports an error. The
element the user worked on stops to exist.

This problem has two halves, and a tool for each.

## Lists: `ui/reconcile.js`

`reconcile(parent, desired, { keep })` matches children by key. It compares
them by signature. It keeps the old node when the key and the signature both
match. Mark each element with `keyed(el, key, sig)`.

The signature needs care. It must cover the data that built the row. It must
not cover only the text that the row shows. Row handlers hold a reference to
that data. A context menu, a drag payload and a cell renderer all do this.

If you reuse a node after a hidden field changed, those handlers use the old
data. Put the source data in the signature and let JSON compare it:

```js
keyed(row, `vm:${vmKey(vm)}`, [vm, lvl, selected]);
```

The module also guarantees two things. Both come from defects that the suite
found:

- A key can repeat. The same guest can appear in two pools. Each key holds a
  queue of nodes, and each match takes one node from it.
- The module always rebuilds an element that has no signature. Two missing
  signatures would otherwise compare as equal. The diff would then keep a node
  that nobody checked.

Users: the sidebar tree in `tree.js`, and the grid rows in `grid.js`.

## Connections: carry the element

Reconciliation does not help when the live thing is a connection. Keep the
element instead, and move it into the new markup:

1. Cache the element that holds the connection. Also cache a key that says
   what it shows.
2. On the next render, build the markup around it as usual. Then put the
   cached element in place of the new empty one, and return.
3. Clear the cache where the teardown happens. A dead element must never look
   like a live one.

Users: the VM console, terminal and RDP tabs in `content/vm.js`; the Multiview
tile grid in `content/multiview.js`; the inventory grid in
`content/vm-table.js`.

Copy two details:

- Cache the inner element. Do not cache the whole pane. The heading around the
  element often shows a value that changes when the cached part does not. A
  count of started VMs is one example. If you cache the pane, you freeze that
  value. This is the defect that the pattern repairs.
- Make the key independent of order if the user can reorder the contents. A
  drag rearranges the Multiview tiles and saves the new order. If the key held
  the order, the next poll would close all of the connections.

A move blurs the element that held focus. Therefore `refresh()` records focus
and the grid scroll position before the render, and restores them after it.
That is the last point at which it can read them.

## Gestures: `ui/interaction.js`

`interacting()` is true while a pointer is down or a drag is active. The poll
does not render during that time. It runs the skipped render when the gesture
ends. It does not store the fingerprint for a skipped render. The update is
late, but it is never lost.

A new gesture needs no code here. The module already covers it.

## Where to add a feature

### Addressable state

The view, the selection and the open tab live in the URL fragment, through
`ui/route.js`. Everything else about the workspace lives in local storage.
The line between them is simple. A link must mean the same thing for the
person you send it to. So where you are belongs in the address, and how wide
you keep your sidebar does not.

A new selection type needs nothing here. `route.js` encodes the type and the
one thing it names. On the way back it puts that payload into both `key` and
`name`, because the UI addresses a guest by key and a node by name. It holds
no table of types.

Two rules if you add a screen:

- Call `markRendered()` after you change the selection or the tab. That
  function writes the address. A tab click redraws one screen through its own
  renderer, so `renderContent()` is not the only place this happens.
- Never apply an address before the fleet has loaded. A link can name a guest.
  `renderContent()` falls back to the datacenter when it cannot find one, which
  drops the link and reports no error. `refresh()` applies the address once,
  after the first load.

### A capability

A capability is a thing a plugin provides, such as host power. It registers
what it adds to the UI, and core draws it. Core does not name the capability
anywhere.

Write one module under `content/`. Hold the data in the module. Then call
`registerCapability` from `ui/capabilities.js` with the hooks you have. Every
hook is optional:

| Hook | What it gives |
|---|---|
| `load()`, `clear()` | Fetch on each poll. A failure clears this capability only. |
| `fingerprint()` | What the poll compares. Leave it out and the data never draws. |
| `screens` | A map from a selection type to a renderer. |
| `widgets(scope)`, `layout(scope)` | Dashboard widgets, and where they sit first. |
| `treeRows(sink)` | Rows for the sidebar. |
| `menuItems(kind, subject)` | Entries for another object's context menu. |
| `alerts()` | Lines for the Alerts widget. |

Import the module for its side effect in `app.js`. That import is the only
mention of it in core.

Read the registry when you draw, never when your module loads. Core imports
the Datacenter screen before it imports the capability, so a widget map built
at load time came out empty. `content/hostpower.js` is the worked example, and
`content/datacenter.js` shows the lazy call.

### A dock panel

Add an entry to `PANELS` in `dock.js`. Add a `<div role="tabpanel"
id="dock-panel-<id>">` to `index.html`. The tab strip, the tab order and the
arrow keys come from the list.

### A dashboard widget

Add an entry to the `widgets` map for `mountDashboard(root, { scope, widgets,
layout })`. The shape is `{ id: { title, w, h, minW?, minH?, live?,
render(body) } }`. A `live` widget polls its own data, and `refresh()` does not
render it again. Each scope saves its own layout and gets Reset layout.

### A tree view

Add the id to `TREE_VIEWS` in `tree.js`. Add a button to `treeViewToggle()`.
Write a renderer.

A renderer receives a sink, not the container. The sink only answers
`appendChild`. The renderer collects the rows first, and then the diff places
them. For this reason, `pools.js` needs no knowledge of reconciliation.

If the view needs data that the fleet poll does not fetch, fetch it in
`refresh()` while that view shows. Pool View and Storage View do this. Also
force one refresh in `setTreeView`, or the tree stays empty for up to 5
seconds. Add the data to `renderFingerprint()`. If you do not, corral fetches
the data and then decides that nothing changed.

### A content screen

Add a `state.selected.type`. Add a branch to `renderContent()` in `app.js`
that calls a renderer in `content/`. The selection carries what the screen
needs, for example `{ type: 'storage', name }`.

### A data grid

Call `mountGrid(host, { id, columns, rows, rowKey, ... })`. It returns a
handle with `update(rows)` and `refresh()`. Use `update` for new data. Do not
mount a second grid.

A column of controls takes `plain: true`. That drops its sort button and its
filter box, because neither can act on a button. Give it a `render(row)` that
returns a node. Stop the click inside that node, or it reaches the row, and the
row also opens the guest. The actions column in `content/vm-table.js` is the
example.

The grid saves the column order, the hidden columns, the widths, the sort, the
filters, the saved views and the row density for each `id`. Row density has
three modes. Their heights are in `DENSITIES` in `grid.js` and in the
stylesheet as a custom property. The virtual scroller reads that number to
size its spacers, so the two values must agree.

### A resizable edge

Call `makeSplitter({ handle, axis, cssVar, storageKey, def, min, max, ... })`
from `ui/splitter.js`. Add `makeCollapsible` if the user can hide the pane. The
stylesheet owns the layout through the custom property. The module owns only
the input.

It follows the window splitter pattern from the W3C APG. This includes the
Enter key that collapses and restores the pane, which the pattern needs.

A new edge has two obligations:

- Publish `aria-valuemin` and `aria-valuemax` on every change if the maximum
  depends on the viewport. A value in the HTML becomes wrong when the user
  resizes the window.
- Keep the separator reachable while the pane stays collapsed. The separator
  owns the key that restores the pane. If you hide it, a keyboard-only
  operator cannot get the pane back.

Add anything new that persists to `resetWorkspaceLayout()` in `app.js`. A
layout that the user can change needs a way back to the default. The vSphere
Web Client let administrators close its Recent Tasks pane with no way to
restore it. The vendor told them to clear the browser cache.

## Write the check

Add a block to `scripts/ui-smoke.mjs`. Start it with a short comment that says
what breaks if the check fails. Then call `check(condition, 'name: what it
proves')`.

**If a check changes the demo state, change it back.** The demo server lives
longer than one run. Some checks stop a VM, write a theme or select a tree
view. If such a check leaves that state, the suite passes once and then fails.
The failure then appears in an unrelated check. Two fixes to the suite repaired
this defect. The `bulk-select` check and the theme check show the shape of a
repair.

Read values from the screen. Do not put them in the check as constants.
Earlier checks reorder the columns. They also hide columns, widen the sidebar
and select a view. Those choices persist. The first column is not always the
column that you expect.
