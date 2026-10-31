// Corral web UI — Proxmox-style dashboard for KubeVirt.
// Vanilla JS; noVNC + xterm.js vendored under static/vendor/ (offline-safe).
//
// Entry module: the poll loop, the content router (select/renderContent) and
// boot. Each screen, the tree, the menus and the consoles live in their own
// native ES module (#340); shared state and events go through state.js.

import { api, findCT, findVM, sendJSON, vmKey } from './api.js';
import { disconnectConsoles } from './console.js';
import { ctAction, renderCT } from './content/ct.js';
import { renderDatacenter } from './content/datacenter.js';
import { renderDoctor } from './content/doctor.js';
import { renderExtensions } from './content/extensions.js';
// Imported for their side effects: each module registers a capability (VDI
// desktops, host power, plugin screens) with ui/capabilities.js. Core names
// nothing in them.
import './content/desktops.js';
import './content/hostpower.js';
import './content/pluginui.js';
import { disconnectMultiview, renderMultiview } from './content/multiview.js';
import { renderNamespace } from './content/namespace.js';
import { renderNode } from './content/node.js';
import { renderSettings } from './content/settings.js';
import { renderStorage } from './content/storage.js';
import { renderTopology } from './content/topology.js';
import { renderIdentityLost, renderOffline } from './content/unavailable.js';
import { renderVM, vmAction } from './content/vm.js';
import { applyInventoryGridAddress, inventoryGridAddress } from './content/vm-table.js';
import { loadCreateTargets, loadInstanceTypes } from './create.js';
// The task dock registers its Alpine component on load; it exports nothing.
import './dock.js';
import { icon } from './icons.js';
import { poolMenuItems, unassignedMenuItems } from './menus.js';
import { bindPalette, initKeys, openPalette } from './palette.js';
import { bindPools, loadPools, poolState } from './pools.js';
import { singleFlight, startPolling } from './poll.js';
import { loadCapabilities, loadWhoami } from './session.js';
import { emit, state } from './state.js';
import { focusTreeFilter, nextTreeView, renderTree, setTreeView, treeRow, treeView, vmRow } from './tree.js';
import { capabilityFingerprint, capabilityScreen, loadCapabilityData } from './ui/capabilities.js';
import { decodeSelection, encodeSelection, onRouteChange, readRoute, writeRoute } from './ui/route.js';
import { $, esc, toast } from './ui/dom.js';
import { activeContextMenu, attachContextMenu } from './ui/menu.js';
import { initInteractionTracking, interacting, onSettled } from './ui/interaction.js';
import { cycleThemeMode, initThemePreferences, themeMode } from './ui/theme.js';
import { onPageVisible, pageHidden } from './ui/visibility.js';
import { initWorkspace, resetWorkspaceLayout, toggleTree } from './ui/workspace.js';

initThemePreferences();

// A console deep link is also the pop-out contract. It uses the canonical VM
// key rather than only a name, so duplicate names on peers/contexts are safe.
const consoleRoute = new URLSearchParams(location.search).get('console');
let consoleRouteApplied = false;
// The hash is applied once, after the first fleet load. See applyRoute().
let routeApplied = false;
// Set while the tailnet identity is missing, so its screen is drawn once and
// the poll that keeps checking does not redraw it every few seconds.
let identityLost = false;
// The last poll failure already reported, so a repeat of it stays quiet.
let lastRefreshFailure = '';

// Fingerprint of the last-rendered state. The 5s poll only re-renders when
// the data (or what's selected) actually changed — otherwise innerHTML
// replacement would reset scroll position and text selection on every tick.
let lastRenderFp = '';
// Set when a poll declined to render because a gesture was in flight, so the
// render can be run as soon as the gesture ends.
let renderDeferred = false;
// Whether the offline empty state is showing — rendered once, not on every
// failed 5s poll, so it doesn't clobber pages that work offline (Extensions).
let offlineShown = false;

// The floor between polls. The real wait is this plus twice how long the last
// poll took; see startPolling() in poll.js.
const POLL_MS = 5000;

// One refresh at a time, and one asked for meanwhile runs after it; see
// singleFlight() in poll.js. A declared function, so the modules that import
// refresh can hold it before this module's body has run.
const refreshSingle = singleFlight(refreshOnce);
export function refresh(force = false) {
  return refreshSingle(force);
}

async function refreshOnce(force = false) {
  try {
    state.vms = await api('/api/vms');
  } catch (e) {
    // The identity the tailnet vouches for stopped arriving. That is not a
    // missing cluster, so it gets its own screen and its own advice.
    if (e.status === 401) {
      if (!identityLost) {
        identityLost = true;
        renderTree();
        renderIdentityLost(() => refresh(true));
      }
      return;
    }
    // Couldn't list VMs from any configured backend → genuinely nothing to
    // show. A blank page with a toast reads as "broken"; show setup guidance
    // once instead, and keep the static tree rows (Extensions works offline).
    if (!lastRenderFp && !offlineShown) {
      offlineShown = true;
      renderTree();
      renderOffline(e.message, () => { offlineShown = false; refresh(true); });
    } else if (lastRenderFp && e.message !== lastRefreshFailure) {
      // Once per distinct failure. A server that is down fails every poll,
      // and a toast each time buries the page under the same sentence.
      toast(`Refresh failed: ${e.message}`);
    }
    lastRefreshFailure = e.message;
    return;
  }
  lastRefreshFailure = '';
  // Identity came back. The pane still shows the identity screen, and the
  // data may be exactly what the last render drew, so the fingerprint would
  // decide there is nothing to do. Force the render that replaces the screen.
  if (identityLost) {
    identityLost = false;
    force = true;
  }
  // Nodes are the cluster topology view; a local-only deployment (QEMU/Incus/
  // libvirt) has none, and a nodes failure must never blank a working VM list.
  try { state.nodes = await api('/api/nodes'); } catch { state.nodes = []; }
  // Each registered capability fetches its own data, and one that fails
  // cannot blank the rest of the page. See ui/capabilities.js.
  await loadCapabilityData();
  offlineShown = false;
  if (treeView === 'pool') await loadPools();
  // The image catalogue changes far more slowly than the fleet and is only
  // needed by Storage View, so it is fetched while that view is showing rather
  // than on every poll. Best-effort: a storage failure must not blank the
  // fleet, exactly as a nodes failure must not.
  if (treeView === 'storage' || state.selected.type === 'storage') {
    try { state.images = await api('/api/images'); } catch { /* keep what we had */ }
    try { state.dataVolumes = await api('/api/datavolumes'); } catch { state.dataVolumes = []; }
  }
  try { state.cts = await api('/api/cts'); } catch { state.cts = []; } // best-effort — don't fail the whole refresh over CTs
  emit('inventory', { vms: state.vms, cts: state.cts, nodes: state.nodes });
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
    if (location.hash) { applyRoute(); return; }
  }
  const fp = renderFingerprint();
  if (!force && fp === lastRenderFp) return; // nothing changed — keep the DOM
  // A poll must not pull the rows out from under an open context menu; the
  // next tick after it closes renders the change.
  if (!force && activeContextMenu) return;
  // Nor out from under a gesture. The fingerprint is deliberately not stored
  // here: leaving it stale is what makes the render after the gesture ends see
  // the change and redraw, instead of deciding nothing happened.
  if (!force && interacting()) { renderDeferred = true; return; }
  lastRenderFp = fp;

  // The tree reconciles its rows in place, so its scroll position, focus and
  // any in-flight drag survive on their own. The content pane still rebuilds
  // from markup, so it keeps the save-and-restore until it reconciles too.
  const contentEl = $('#content');
  const contentScroll = contentEl ? contentEl.scrollTop : 0;
  // A view rebuilds the markup around whatever the operator was using, which
  // blurs the focused element and resets the grid's own scroller. Both are
  // noted here because this is the last moment they can still be read, and put
  // back afterwards if the element outlived the render — the inventory grid is
  // reused rather than rebuilt, so its rows usually do.
  const gridScroller = contentEl?.querySelector('.grid-scroll');
  const gridScroll = gridScroller ? { top: gridScroller.scrollTop, left: gridScroller.scrollLeft } : null;
  const wasFocused = contentEl?.contains(document.activeElement) ? document.activeElement : null;
  renderTree();
  // Every screen that holds a live connection now carries its own element
  // across a render — the VM console tabs in content/vm.js, the Multiview grid
  // in content/multiview.js — so the pane is always safe to render and nothing
  // has to be skipped to protect it.
  renderContent();
  // Focus before the scroll, and without letting the focus move the view.
  //
  // Putting focus on an element scrolls it into view. Restoring the scroll
  // first and focusing second therefore undoes the scroll whenever the render
  // moved that element — rows added or removed above it — and the pane jumps
  // to the focused row instead of staying where the operator left it. Proxmox
  // carries an override on its grid view for this exact order, with the same
  // reason written on it. `preventScroll` says it outright, and the scroll
  // restore still comes after, so the operator's position always wins.
  //
  // Only if the render actually dropped focus: if something else has taken it
  // in the meantime, putting it back would steal it.
  if (wasFocused?.isConnected && document.activeElement === document.body) {
    wasFocused.focus({ preventScroll: true });
  }
  if (contentEl) contentEl.scrollTop = contentScroll;
  const scrollerNow = contentEl?.querySelector('.grid-scroll');
  if (scrollerNow && gridScroll) { scrollerNow.scrollTop = gridScroll.top; scrollerNow.scrollLeft = gridScroll.left; }
}

// Everything the tree and content pane draw from. One definition, because the
// poll compares against it and markRendered writes it: when the two disagreed
// — the second was missing a capability's data — a render could be judged
// necessary every tick for a change that was already on screen. The image
// catalogue is in here too, or Storage View would fetch its images and then
// conclude there was nothing new to draw. A capability contributes its own
// data through the registry, so this list does not have to name it.
function renderFingerprint() {
  return JSON.stringify([
    state.vms, state.cts, state.nodes, capabilityFingerprint(),
    state.images, state.dataVolumes, state.selected, state.tab,
  ]);
}

// markRendered records the just-rendered state so the next poll tick doesn't
// re-render (and reset scroll) for a change the user already saw.
export function markRendered() {
  lastRenderFp = renderFingerprint();
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

// openVM selects a VM and, optionally, one of its tabs. "console" means the
// best console the VM has: VNC when it has one, the serial terminal if not.
function openVM(key, wantTab) {
  const vm = findVM(key);
  if (!vm) return;
  const cap = vm.capabilities || {};
  const t = wantTab === 'console' && !cap.vnc && cap.tty ? 'terminal' : (wantTab || 'summary');
  // Already there: leave a live console connected rather than reconnecting it.
  if (state.selected.type === 'vm' && state.selected.key === key && state.tab === t) return;
  select({ type: 'vm', key }, t);
}

// openPool shows a pool the only place pools are drawn: the Pool View tree.
async function openPool(path) {
  if (treeView !== 'pool') setTreeView('pool');
  await loadPools();
  renderTree();
  const row = [...$('#tree').querySelectorAll('.tree-item')].find((r) => r.title === path);
  if (!row) return;
  $('#tree').classList.add('open');
  row.scrollIntoView({ block: 'nearest' });
  row.classList.add('flash');
  setTimeout(() => row.classList.remove('flash'), 1500);
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
function applyRoute() {
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
      return renderVM(main, vm);
    }
  }
  if (state.selected.type === 'ct') {
    const c = findCT(state.selected.key);
    if (!c) { state.selected = fallbackSelection(); }
    else {
      lastParentNode = c.node || '';
      return renderCT(main, c);
    }
  }
  if (state.selected.type === 'node') return renderNode(main, state.selected.name);
  if (state.selected.type === 'namespace') return renderNamespace(main, state.selected.name);
  if (state.selected.type === 'extensions') return renderExtensions(main);
  if (state.selected.type === 'doctor') return renderDoctor(main);
  if (state.selected.type === 'multiview') return renderMultiview(main);
  if (state.selected.type === 'topology') return renderTopology(main);
  if (state.selected.type === 'storage') return renderStorage(main, state.selected.name);
  if (state.selected.type === 'settings') return renderSettings(main);
  // A capability may own a selection type. Core does not list those types.
  const fromCapability = capabilityScreen(state.selected.type);
  if (fromCapability) return fromCapability(main, state.selected.key);
  return renderDatacenter(main);
}

// ── Boot ──────────────────────────────────────────────────────────

initWorkspace();
initInteractionTracking();
// The poll skips a render while the operator is mid-gesture; this is what runs
// it once they let go, so a change that landed during a drag is not held until
// the next tick.
onSettled(() => {
  if (!renderDeferred) return;
  renderDeferred = false;
  refresh();
});
$('#btn-menu').innerHTML = icon('menu');
$('#btn-create').innerHTML = `${icon('plus')} Create VM`;
$('#btn-palette').innerHTML = `${icon('search')}<span class="btn-label">Search</span>`;
$('#btn-palette').onclick = () => openPalette();

// Pool View borrows the tree's row builders rather than growing its own, so a
// pool row and a node row stay visually identical — the difference is what a
// drop onto one means, not how it looks.
bindPools({
  api, sendJSON, toast, esc, icon, refresh, treeRow, vmRow, vmKey, findVM,
  attachContextMenu, poolMenuItems, unassignedMenuItems,
});

// The palette reads the fleet through getters: the poll replaces these arrays
// rather than mutating them, so a captured reference would go stale.
bindPalette({
  esc, icon, vmKey,
  vms: () => state.vms,
  cts: () => state.cts,
  nodes: () => state.nodes,
  pools: poolState,
  ensurePools: async () => { if (!(poolState().folders || []).length) { await loadPools(); } },
  go: select,
  openVM,
  openPool,
  vmAction: (key, act) => { const vm = findVM(key); if (vm) vmAction(vm, act); },
  ctAction: (key, act) => { const c = findCT(key); if (c) ctAction(c, act); },
  createVM: () => $('#btn-create').click(),
  createCT: () => $('#btn-create-ct').click(),
  selectedVMKey: () => (state.selected.type === 'vm' ? state.selected.key : null),
  focusFilter: focusTreeFilter,
  nextTreeView: () => { const v = nextTreeView(); toast(`${v.charAt(0).toUpperCase()}${v.slice(1)} View`); },
  resetLayout: resetWorkspaceLayout,
  toggleTree: () => toggleTree(),
  themeMode,
  cycleTheme: () => { cycleThemeMode(); renderContent(); },
});
initKeys();

// An address given to us decides where we start, applied by the first refresh
// once the fleet is there to look in. Back and forward are live immediately.
onRouteChange(applyRoute);

loadWhoami();
loadCapabilities(loadCreateTargets);
loadInstanceTypes();
refresh();
// Nobody looking means nothing fetched; onPageVisible below catches up.
startPolling(refresh, { interval: POLL_MS, hidden: pageHidden });
// Back from a hidden tab: fetch now rather than show data up to one interval
// old as if it were current. See ui/visibility.js.
onPageVisible(() => refresh());