// Selection, deep links and screen dispatch. Screen modules register at boot.
import { findCT, findVM } from './api.js';
import { disconnectConsoles } from './console.js';
import { disconnectMultiview } from './content/multiview.js';
import { applyInventoryGridAddress, inventoryGridAddress } from './content/vm-table.js';
import { pollingState, renderFingerprint } from './refresh-state.js';
import { emit, state } from './state.js';
import { renderTree, setTreeView, treeView } from './tree.js';
import { capabilityScreen } from './ui/capabilities.js';
import { $ } from './ui/dom.js';
import { decodeSelection, encodeSelection, readRoute, writeRoute } from './ui/route.js';
import { screenFor } from './ui/screens.js';

// A console deep link is also the pop-out contract. It uses the canonical VM
// key rather than only a name, so duplicate names on peers/contexts are safe.
const consoleRoute = new URLSearchParams(location.search).get('console');
let consoleRouteApplied = false;
// The hash is applied once, after the first fleet load. See applyRoute().
let routeApplied = false;

export function applyInitialRoute() {
  // A pop-out console opens straight onto the console tab. It needs no nudge to
  // render any more: setting the selection and the tab changes the fingerprint,
  // and there is no longer a guard that would skip a console tab anyway.
  if (consoleRoute && !consoleRouteApplied) {
    consoleRouteApplied = true;
    const vm = findVM(consoleRoute);
    if (vm) {
      state.selected = { type: 'vm', key: consoleRoute };
      state.tab = 'console';
      document.body.classList.add('console-popout');
      document.title = `${vm.name} console · Corral`;
    }
  }
  // An address can name a guest, and a guest only exists once the fleet has
  // loaded. Applying it before that, renderContent() would fail to find the
  // guest and fall back to the datacenter, which loses the link silently.
  if (!routeApplied) {
    routeApplied = true;
    if (location.hash) { applyRoute(); return true; }
  }
  return false;
}

// markRendered records the just-rendered state so the next poll tick doesn't
// re-render (and reset scroll) for a change the user already saw.
export function markRendered() {
  pollingState.markRendered(renderFingerprint());
  // Also here, not only in renderContent(): a tab click redraws one screen by
  // calling its renderer directly, so renderContent() is not the single place
  // navigation settles. The poll calls this too, which costs nothing because
  // writeRoute() ignores a write that would not change the address.
  syncRoute();
}

export function select(sel, openTab = 'summary') {
  disconnectConsoles();
  if (state.selected.type === 'multiview' && sel.type !== 'multiview') disconnectMultiview();
  state.selected = sel;
  state.tab = openTab;
  renderTree();
  renderContent();
  markRendered();
  emit('select', { selected: state.selected, tab: state.tab });
}

// ── Content panel ─────────────────────────────────────────────────

// Keep the address in step with what is drawn. See ui/route.js for why the
// view, the selection and the tab go in the URL and the layout does not.
//
// The console popout is deliberately exempt: it is addressed by ?console= and
// is a window showing one screen, not a place to navigate from.
export function syncRoute() {
  if (document.body.classList.contains('console-popout')) return;
  writeRoute({
    view: treeView,
    sel: encodeSelection(state.selected),
    tab: state.tab,
    // The inventory grid's sort and filters, when a grid is on screen. A link
    // to a filtered list then opens on the same list.
    grid: inventoryGridAddress(),
  });
}

// Apply an address to the page. Used at boot, and again whenever the back or
// forward button moves us.
//
// The URL wins over what this browser had stored, because a link has to mean
// the same thing for the person who was sent it.
export function applyRoute() {
  const route = readRoute();
  if (route.view !== treeView) setTreeView(route.view);
  const selected = decodeSelection(route.sel);
  // Straight onto the state rather than through select(): select() writes the
  // address, and this is the one path that must not, or the back button would
  // immediately push the entry it had just left.
  disconnectConsoles();
  state.selected = selected;
  state.tab = route.tab;
  renderTree();
  renderContent();
  // After the render, so a grid the render just mounted takes the sort and
  // filters at once; one not yet mounted takes them on its first mount.
  applyInventoryGridAddress(route.grid);
  markRendered();
  emit('select', { selected: state.selected, tab: state.tab });
}

// Where to go when the thing on screen stops existing.
//
// A guest can vanish under you: somebody deletes it, or a migration finishes
// and it is gone from the node you were watching. This used to drop to the
// datacenter, which is the furthest possible place from where you were.
// Proxmox walks up the parent chain and selects the nearest ancestor that
// still exists, so you land beside the guest's siblings instead of at the top.
//
// The parent has to be recorded while the guest is still here, because once it
// is gone there is nothing left to ask. renderContent() keeps it on each draw.
let lastParentNode = '';

function fallbackSelection() {
  if (lastParentNode && state.nodes.some((n) => n.name === lastParentNode)) {
    return { type: 'node', name: lastParentNode };
  }
  return { type: 'dc' };
}

export function renderContent() {
  const main = $('#content');
  syncRoute();
  if (state.selected.type === 'vm') {
    const vm = findVM(state.selected.key);
    if (!vm) { state.selected = fallbackSelection(); }
    else {
      lastParentNode = vm.node || '';
      return screenFor('vm')(main, vm);
    }
  }
  if (state.selected.type === 'ct') {
    const c = findCT(state.selected.key);
    if (!c) { state.selected = fallbackSelection(); }
    else {
      lastParentNode = c.node || '';
      return screenFor('ct')(main, c);
    }
  }
  const screen = screenFor(state.selected.type);
  if (screen) return screen(main, state.selected.name);
  // A capability may own a selection type. Core does not list those types.
  const fromCapability = capabilityScreen(state.selected.type);
  if (fromCapability) return fromCapability(main, state.selected.key);
  return screenFor('dc')(main);
}
