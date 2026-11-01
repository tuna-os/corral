// Corral web UI: boot, visibility and interaction lifecycle wiring.
import { api, findCT, findVM, sendJSON, vmKey } from './api.js';
import { ctAction } from './content/ct.js';
import { vmAction } from './content/vm.js';
import './content/screens.js';
import { loadCreateTargets, loadInstanceTypes } from './create.js';
import './dock.js';
import { icon } from './icons.js';
import { poolMenuItems, unassignedMenuItems } from './menus.js';
import { bindPalette, initKeys, openPalette } from './palette.js';
import { bindPools, loadPools, poolState } from './pools.js';
import { startPolling } from './poll.js';
import { POLL_MS, refresh } from './refresh.js';
import { pollingState } from './refresh-state.js';
import { applyRoute, renderContent, select } from './router.js';
import { loadCapabilities, loadWhoami } from './session.js';
import { state } from './state.js';
import { focusTreeFilter, nextTreeView, renderTree, setTreeView, treeRow, treeView, vmRow } from './tree.js';
import { $, esc, toast } from './ui/dom.js';
import { attachContextMenu } from './ui/menu.js';
import { initInteractionTracking, onSettled } from './ui/interaction.js';
import { onRouteChange } from './ui/route.js';
import { cycleThemeMode, themeMode } from './ui/theme.js';
import { onPageVisible, pageHidden } from './ui/visibility.js';
import { initWorkspace, resetWorkspaceLayout, toggleTree } from './ui/workspace.js';

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
// The poll skips a render while the operator is mid-gesture; this is what runs
// it once they let go, so a change that landed during a drag is not held until
// the next tick.
onSettled(() => {
  if (!pollingState.takeDeferred()) return;
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