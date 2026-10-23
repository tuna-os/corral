// Corral web UI — Proxmox-style dashboard for KubeVirt.
// Vanilla JS; noVNC + xterm.js vendored under static/vendor/ (offline-safe).
//
// Entry module: lifecycle coordination, boot, workspace events, and wiring.
// Screen routing and dispatch live in router.js; poll coordination and timing
// live in poll.js; content screens self-register; shared state in state.js.

import { api, findCT, findVM, sendJSON, vmKey } from './api.js';
import { disconnectConsoles } from './console.js';
import { applyRoute, renderContent, router, select, syncRoute } from './router.js';
import { ctAction } from './content/ct.js';
// Content modules self-register with router.js or ui/capabilities.js on load.
import './content/ct.js';
import './content/datacenter.js';
import './content/desktops.js';
import './content/doctor.js';
import './content/extensions.js';
import './content/hostpower.js';
import './content/multiview.js';
import './content/namespace.js';
import './content/node.js';
import './content/pluginui.js';
import './content/settings.js';
import './content/storage.js';
import './content/topology.js';
import { renderIdentityLost, renderOffline } from './content/unavailable.js';
import { vmAction } from './content/vm.js';
import './content/vm.js';
import { applyInventoryGridAddress, inventoryGridAddress } from './content/vm-table.js';
import { loadCreateTargets, loadInstanceTypes } from './create.js';
// The task dock registers its Alpine component on load; it exports nothing.
import './dock.js';
import { icon } from './icons.js';
import { poolMenuItems, unassignedMenuItems } from './menus.js';
import { bindPalette, initKeys, openPalette } from './palette.js';
import { bindPools, loadPools, poolState } from './pools.js';
import { PollingState } from './poll.js';
import { loadCapabilities, loadWhoami } from './session.js';
import { emit, state } from './state.js';
import { focusTreeFilter, nextTreeView, renderTree, setTreeView, treeRow, treeView, vmRow } from './tree.js';
import { capabilityFingerprint, loadCapabilityData } from './ui/capabilities.js';
import { onRouteChange } from './ui/route.js';
import { $, esc, toast } from './ui/dom.js';
import { activeContextMenu, attachContextMenu } from './ui/menu.js';
import { initInteractionTracking, interacting, onSettled } from './ui/interaction.js';
import { cycleThemeMode, themeMode } from './ui/theme.js';
import { onPageVisible, pageHidden } from './ui/visibility.js';
import { initWorkspace, resetWorkspaceLayout, toggleTree } from './ui/workspace.js';

// Wire router to tree view and inventory grid address
router.onRenderTree = renderTree;
router.getTreeView = () => treeView;
router.onSetTreeView = (v) => setTreeView(v);
router.getGridAddress = inventoryGridAddress;
router.onApplyGridAddress = (grid) => applyInventoryGridAddress(grid);
router.onDisconnectConsoles = disconnectConsoles;

// Re-export router functions for existing callers that import from app.js
export { applyRoute, renderContent, router, select, syncRoute };

// A console deep link is also the pop-out contract. It uses the canonical VM
// key rather than only a name, so duplicate names on peers/contexts are safe.
const consoleRoute = new URLSearchParams(location.search).get('console');
let consoleRouteApplied = false;
// The hash is applied once, after the first fleet load. See applyRoute().
let routeApplied = false;

// The floor between polls. The real wait is this plus twice how long the last
// poll took; see startPolling() in poll.js.
const POLL_MS = 5000;

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
  pollingState.markRendered();
  syncRoute();
}

function renderApp() {
  const contentEl = $('#content');
  const contentScroll = contentEl ? contentEl.scrollTop : 0;
  const gridScroller = contentEl?.querySelector('.grid-scroll');
  const gridScroll = gridScroller ? { top: gridScroller.scrollTop, left: gridScroller.scrollLeft } : null;
  const wasFocused = contentEl?.contains(document.activeElement) ? document.activeElement : null;

  renderTree();
  renderContent();

  if (wasFocused?.isConnected && document.activeElement === document.body) {
    wasFocused.focus({ preventScroll: true });
  }
  if (contentEl) contentEl.scrollTop = contentScroll;
  const scrollerNow = contentEl?.querySelector('.grid-scroll');
  if (scrollerNow && gridScroll) {
    scrollerNow.scrollTop = gridScroll.top;
    scrollerNow.scrollLeft = gridScroll.left;
  }
}

async function fetchFleetData(force = false) {
  state.vms = await api('/api/vms');
  try { state.nodes = await api('/api/nodes'); } catch { state.nodes = []; }
  await loadCapabilityData();
  if (treeView === 'pool') await loadPools();
  if (treeView === 'storage' || state.selected.type === 'storage') {
    try { state.images = await api('/api/images'); } catch { /* keep what we had */ }
    try { state.dataVolumes = await api('/api/datavolumes'); } catch { state.dataVolumes = []; }
  }
  try { state.cts = await api('/api/cts'); } catch { state.cts = []; }
  emit('inventory', { vms: state.vms, cts: state.cts, nodes: state.nodes });

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

  if (!routeApplied) {
    routeApplied = true;
    if (location.hash) {
      applyRoute();
    }
  }
}

export const pollingState = new PollingState({
  interval: POLL_MS,
  fetchData: fetchFleetData,
  fingerprint: renderFingerprint,
  render: renderApp,
  shouldDefer: () => interacting(),
  shouldSkip: () => Boolean(activeContextMenu),
  onIdentityLost: (retry) => {
    renderTree();
    renderIdentityLost(retry);
  },
  onOffline: (err, retry) => {
    renderTree();
    renderOffline(err.message, retry);
  },
  onError: (err) => {
    toast(`Refresh failed: ${err.message}`);
  },
});

export function refresh(force = false) {
  return pollingState.poll(force);
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

// ── Boot ──────────────────────────────────────────────────────────

initWorkspace();
initInteractionTracking();

onSettled(() => {
  pollingState.onSettled();
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
pollingState.start({ hidden: pageHidden });
// Back from a hidden tab: fetch now rather than show data up to one interval
// old as if it were current. See ui/visibility.js.
onPageVisible(() => refresh());