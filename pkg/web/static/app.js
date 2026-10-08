// Corral web UI — Proxmox-style dashboard for KubeVirt.
// Vanilla JS; noVNC + xterm.js vendored under static/vendor/ (offline-safe).
//
// Entry module: the poll loop, the content router (select/renderContent) and
// boot. Each screen, the tree, the menus and the consoles live in their own
// native ES module (#340); shared state and events go through state.js.

import { api, findCT, findVM, vmKey } from './api.js';
import { disconnectConsoles } from './console.js';
import { ctAction, renderCT } from './content/ct.js';
import { renderDatacenter } from './content/datacenter.js';
import { renderDoctor } from './content/doctor.js';
import { renderExtensions } from './content/extensions.js';
import { renderHostPower } from './content/hostpower.js';
import { disconnectMultiview, renderMultiview } from './content/multiview.js';
import { renderNamespace } from './content/namespace.js';
import { renderNode } from './content/node.js';
import { renderSettings } from './content/settings.js';
import { renderVM, vmAction } from './content/vm.js';
import { updateSourceFields } from './create.js';
// The task dock registers its Alpine component on load; it exports nothing.
import './dock.js';
import { icon } from './icons.js';
import { poolMenuItems, unassignedMenuItems } from './menus.js';
import { bindPalette, initKeys, openPalette } from './palette.js';
import { bindPools, loadPools, poolState } from './pools.js';
import { emit, state } from './state.js';
import { focusTreeFilter, renderTree, setTreeView, treeRow, treeView, vmRow } from './tree.js';
import { $, esc, toast } from './ui/dom.js';
import { makeCollapsible, makeSplitter } from './ui/splitter.js';
import { activeContextMenu, attachContextMenu } from './ui/menu.js';

// A console deep link is also the pop-out contract. It uses the canonical VM
// key rather than only a name, so duplicate names on peers/contexts are safe.
const consoleRoute = new URLSearchParams(location.search).get('console');
let consoleRouteApplied = false;

// Fingerprint of the last-rendered state. The 5s poll only re-renders when
// the data (or what's selected) actually changed — otherwise innerHTML
// replacement would reset scroll position and text selection on every tick.
let lastRenderFp = '';
// Whether the offline empty state is showing — rendered once, not on every
// failed 5s poll, so it doesn't clobber pages that work offline (Extensions).
let offlineShown = false;

// First-load failure page: couldn't list any VMs. Tailor the guidance to the
// configured backends — only nag about connecting a KubeVirt cluster when
// kubevirt is actually a configured target. A host that only runs local
// QEMU/Incus/libvirt VMs should never be told to go install KubeVirt.
async function renderOffline(msg) {
  const content = $('#content');
  if (!content) return;
  let backends = [];
  try {
    const r = await api('/api/contexts');
    backends = (r.contexts || []).map((c) => c.backend);
  } catch { /* fall through to the cluster-oriented default */ }
  // Unknown backends (contexts fetch failed) defaults to the cluster message,
  // which also carries the local-CLI fallback hint.
  const hasKubevirt = backends.length === 0 || backends.includes('kubevirt');

  if (hasKubevirt) {
    content.innerHTML = `
      <div class="empty-state">
        <div class="empty-icon">🤠</div>
        <h1>No cluster connected</h1>
        <p class="muted">Corral couldn't reach a Kubernetes cluster:
          <code>${esc(msg)}</code></p>
        <p>This dashboard drives the KubeVirt backend. To get going:</p>
        <ul>
          <li>Point <code>kubectl</code> at a cluster (<code>KUBECONFIG</code> or
            <code>~/.kube/config</code>), then hit Retry.</li>
          <li>Run <code>corral doctor --fix</code> to install anything the
            cluster is missing (KubeVirt, CDI, …).</li>
          <li>No cluster handy? Local QEMU VMs work from the CLI:
            <code>corral create myvm --image fedora</code></li>
        </ul>
        <button class="btn primary" id="offline-retry">Retry</button>
      </div>`;
  } else {
    const names = [...new Set(backends.filter((b) => b !== 'kubevirt'))].join(', ') || 'local';
    content.innerHTML = `
      <div class="empty-state">
        <div class="empty-icon">🤠</div>
        <h1>No VMs yet</h1>
        <p class="muted">Couldn't list VMs from the ${esc(names)} backend:
          <code>${esc(msg)}</code></p>
        <p>Create your first VM from the CLI, then hit Retry:</p>
        <ul>
          <li><code>corral create myvm --image fedora</code></li>
        </ul>
        <button class="btn primary" id="offline-retry">Retry</button>
      </div>`;
  }
  const b = $('#offline-retry');
  if (b) b.onclick = () => { offlineShown = false; refresh(true); };
}

export async function refresh(force = false) {
  try {
    state.vms = await api('/api/vms');
  } catch (e) {
    // Couldn't list VMs from any configured backend → genuinely nothing to
    // show. A blank page with a toast reads as "broken"; show setup guidance
    // once instead, and keep the static tree rows (Extensions works offline).
    if (!lastRenderFp && !offlineShown) {
      offlineShown = true;
      renderTree();
      renderOffline(e.message);
    } else if (lastRenderFp) {
      toast(`Refresh failed: ${e.message}`);
    }
    return;
  }
  // Nodes are the cluster topology view; a local-only deployment (QEMU/Incus/
  // libvirt) has none, and a nodes failure must never blank a working VM list.
  try { state.nodes = await api('/api/nodes'); } catch { state.nodes = []; }
  try { state.hostPower = await api('/api/hostpower'); } catch { state.hostPower = { hosts: [] }; }
  offlineShown = false;
  if (treeView === 'pool') await loadPools();
  try { state.cts = await api('/api/cts'); } catch { state.cts = []; } // best-effort — don't fail the whole refresh over CTs
  emit('inventory', { vms: state.vms, cts: state.cts, nodes: state.nodes });
  // A pop-out console opens straight onto the console tab, which the guard
  // below never renders on poll — render it once here.
  let renderPopout = false;
  if (consoleRoute && !consoleRouteApplied) {
    consoleRouteApplied = true;
    const vm = findVM(consoleRoute);
    if (vm) {
      state.selected = { type: 'vm', key: consoleRoute };
      state.tab = 'console';
      document.body.classList.add('console-popout');
      document.title = `${vm.name} console · Corral`;
      renderPopout = true;
    }
  }
  const fp = JSON.stringify([state.vms, state.cts, state.nodes, state.hostPower, state.selected, state.tab]);
  if (!force && fp === lastRenderFp) return; // nothing changed — keep the DOM
  // A poll must not pull the rows out from under an open context menu; the
  // next tick after it closes renders the change.
  if (!force && activeContextMenu) return;
  lastRenderFp = fp;

  // The tree reconciles its rows in place, so its scroll position, focus and
  // any in-flight drag survive on their own. The content pane still rebuilds
  // from markup, so it keeps the save-and-restore until it reconciles too.
  const contentEl = $('#content');
  const contentScroll = contentEl ? contentEl.scrollTop : 0;
  renderTree();
  // Don't clobber live consoles (or the multiview grid) on poll.
  if (renderPopout || (state.tab !== 'console' && state.tab !== 'terminal' && state.selected.type !== 'multiview')) renderContent();
  if (contentEl) contentEl.scrollTop = contentScroll;
}

async function loadCaps() {
  try { state.caps = await api('/api/capabilities'); } catch { /* keep defaults */ }
  // bootc/windows are optional plugins — hide their source options when absent.
  if (!state.caps.bootc) {
    document.querySelector('[name=sourceType] option[value=bootc]')?.remove();
  }
  await loadCreateContexts();
  // The Windows create flow is compiled into the web server (manifest gen in
  // pkg/kubevirt), so it's always available — no plugin install needed.
  try { state.availableNADs = await api('/api/nads'); } catch { state.availableNADs = []; }
}

async function loadCreateContexts() {
  const select = document.querySelector('#create-form [name=target]');
  if (!select) return;
  try {
    const result = await api('/api/contexts');
    const targets = result.contexts || [];
    select.innerHTML = targets.map((c) => `<option value="${esc(c.name)}" data-backend="${esc(c.backend)}" ${c.name === result.default ? 'selected' : ''}>${esc(c.name)} · ${esc(c.backend)}${c.context ? ` · ${esc(c.context)}` : ''}</option>`).join('');
  } catch {
    select.innerHTML = '<option value="cluster" data-backend="kubevirt">cluster · kubevirt</option>';
  }
  updateSourceFields();
}

// loadWhoami fetches the caller identity and flips the UI into read-only mode
// for non-admins (the server still enforces this; the UI just hides controls).
async function loadWhoami() {
  try { state.me = await api('/api/whoami'); } catch { return; }
  const el = $('#whoami');
  if (el) {
    const badge = state.me.admin ? '' : '<span class="ro-badge">read-only</span>';
    if (state.me.login) el.innerHTML = `<span class="who-name">${esc(state.me.name || state.me.login)}</span>${badge}`;
    else if (state.me.enforced) el.innerHTML = badge; // behind the gate but unidentified
    else el.textContent = '';
  }
  document.body.classList.toggle('read-only', !state.me.admin);
}

async function loadInstanceTypes() {
  let d;
  try { d = await api('/api/instancetypes'); } catch { return; }
  const fill = (sel, items, head) => {
    const el = document.querySelector(sel);
    if (!el) return;
    el.innerHTML = `<option value="">${head}</option>` +
      (items || []).map((n) => `<option value="${esc(n)}">${esc(n)}</option>`).join('');
  };
  fill('[name=instancetype]', d.instancetypes, '— manual CPU/mem —');
  fill('[name=preference]', d.preferences, '(none)');
}

// markRendered records the just-rendered state so the next poll tick doesn't
// re-render (and reset scroll) for a change the user already saw.
export function markRendered() {
  lastRenderFp = JSON.stringify([state.vms, state.cts, state.nodes, state.selected, state.tab]);
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

export function renderContent() {
  const main = $('#content');
  if (state.selected.type === 'vm') {
    const vm = findVM(state.selected.key);
    if (!vm) { state.selected = { type: 'dc' }; }
    else return renderVM(main, vm);
  }
  if (state.selected.type === 'ct') {
    const c = findCT(state.selected.key);
    if (!c) { state.selected = { type: 'dc' }; }
    else return renderCT(main, c);
  }
  if (state.selected.type === 'node') return renderNode(main, state.selected.name);
  if (state.selected.type === 'namespace') return renderNamespace(main, state.selected.name);
  if (state.selected.type === 'extensions') return renderExtensions(main);
  if (state.selected.type === 'doctor') return renderDoctor(main);
  if (state.selected.type === 'hostpower') return renderHostPower(main, state.selected.key);
  if (state.selected.type === 'multiview') return renderMultiview(main);
  if (state.selected.type === 'settings') return renderSettings(main);
  return renderDatacenter(main);
}

// ── Sidebar resizing ──────────────────────────────────────────────
//
// Workspace layout: the sidebar width, the dock height, and whether either is
// collapsed. All three are operator preferences (#290, #341), so all three are
// remembered per browser and all three go through one primitive in
// ui/splitter.js rather than a bespoke handler each.

const TREE_WIDTH_DEFAULT = 270;
const TREE_WIDTH_MIN = 180;
const DOCK_HEIGHT_DEFAULT = 220;
const DOCK_HEIGHT_MIN = 90;

let treeCollapse = null;

/** Collapse or restore the sidebar. Exported for the header button. */
export function toggleTree(force) {
  return treeCollapse ? treeCollapse.toggle(force) : false;
}

function initWorkspace() {
  const tree = $('#tree');
  const treeHandle = $('#tree-resizer');
  if (tree && treeHandle) {
    // Collapsing is a body class so the stylesheet owns what it looks like;
    // this module only owns the input that flips it.
    treeCollapse = makeCollapsible({
      className: 'tree-collapsed',
      storageKey: 'corral.treeCollapsed',
    });
    makeSplitter({
      handle: treeHandle,
      axis: 'x',
      cssVar: '--tree-w',
      storageKey: 'corral.treeWidth',
      def: TREE_WIDTH_DEFAULT,
      min: TREE_WIDTH_MIN,
      max: () => Math.max(TREE_WIDTH_MIN, Math.round(window.innerWidth * 0.6)),
      sizeFromPointer: (ev) => ev.clientX - tree.getBoundingClientRect().left,
      current: () => tree.getBoundingClientRect().width,
      collapsible: treeCollapse,
    });
  }

  const dockHandle = $('#dock-resizer');
  const dock = $('#task-panel');
  if (dockHandle && dock) {
    // The dock grows upward, so the pointer maps to the distance from the
    // bottom of the window rather than to a coordinate.
    makeSplitter({
      handle: dockHandle,
      axis: 'y',
      cssVar: '--dock-h',
      storageKey: 'corral.dockHeight',
      def: DOCK_HEIGHT_DEFAULT,
      min: DOCK_HEIGHT_MIN,
      max: () => Math.max(DOCK_HEIGHT_MIN, Math.round(window.innerHeight * 0.6)),
      sizeFromPointer: (ev) => window.innerHeight - ev.clientY,
      current: () => $('#task-panel-body')?.getBoundingClientRect().height || DOCK_HEIGHT_DEFAULT,
    });
  }
}

// ── Mobile drawer ─────────────────────────────────────────────────

$('#btn-menu').onclick = () => {
  if (window.innerWidth <= 760) $('#tree').classList.toggle('open');
  else toggleTree();
};
export function closeDrawer() { $('#tree').classList.remove('open'); }

// ── Boot ──────────────────────────────────────────────────────────

initWorkspace();
$('#btn-menu').innerHTML = icon('menu');
$('#btn-create').innerHTML = `${icon('plus')} Create VM`;
$('#btn-palette').innerHTML = `${icon('search')}<span class="btn-label">Search</span>`;
$('#btn-palette').onclick = () => openPalette();

// Pool View borrows the tree's row builders rather than growing its own, so a
// pool row and a node row stay visually identical — the difference is what a
// drop onto one means, not how it looks.
bindPools({
  api, toast, esc, icon, refresh, treeRow, vmRow, vmKey, findVM,
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
});
initKeys();

loadWhoami();
loadCaps();
loadInstanceTypes();
refresh();
setInterval(refresh, 5000);