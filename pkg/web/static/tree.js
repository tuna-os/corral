// Sidebar tree: server, namespace and pool views, the filter box and the
// multi-select click model shared with the inventory grid.

import { ctKey, vmKey } from './api.js';
import { closeDrawer, markRendered, renderContent, select } from './app.js';
import { hostPowerDot, hostPowerKey } from './content/hostpower.js';
import { migrateVM } from './content/vm.js';
import { icon } from './icons.js';
import {
  ctMenuItems, hostPowerMenuItems, namespaceMenuItems, nodeMenuItems, vmMenuItems,
} from './menus.js';
import { dropZone, loadPools, makeDraggable, renderTreePools } from './pools.js';
import { state } from './state.js';
import { $, esc } from './ui/dom.js';
import { attachContextMenu } from './ui/menu.js';

// ── Tree (sidebar) ────────────────────────────────────────────────
// Two views over the same VM list, mirroring PVE's Server/Folder view
// toggle (see docs/adr — namespace is the stable grouping axis for
// KubeVirt: unlike node, it doesn't change under live migration).

const TREE_VIEW_KEY = 'corral-tree-view';
const TREE_VIEWS = ['server', 'namespace', 'pool'];
// 'folder' was this view's name before user-defined pools existed. Two things
// called folders side by side was confusing, so it became Namespace View — but
// the old value is still in people's localStorage, and silently resetting their
// sidebar to Server View would be a worse greeting than a one-line migration.
function storedTreeView() {
  const stored = localStorage.getItem(TREE_VIEW_KEY);
  if (stored === 'folder') return 'namespace';
  return TREE_VIEWS.includes(stored) ? stored : 'server';
}
export let treeView = storedTreeView();

export function setTreeView(v) {
  treeView = v;
  localStorage.setItem(TREE_VIEW_KEY, v);
  // Pool View reads a different source than the fleet poll, so switching into
  // it fetches once rather than waiting out the next 5s cycle.
  if (v === 'pool') loadPools().then(renderTree);
  else renderTree();
}

export function treeRow({ lvl, icon, label, sub, sel, onclick, dot }) {
  const div = document.createElement('div');
  div.className = `tree-item lvl-${lvl}${sel ? ' selected' : ''}`;
  div.setAttribute('tabindex', '0');
  // The label is its own element so it can be ellipsized: a bare text node is
  // an anonymous flex item and refuses to shrink, which is how a long VM name
  // used to push the row past the sidebar edge (#290).
  div.innerHTML = `${dot ? `<span class="dot ${dot}"></span>` : ''}${icon}` +
    ` <span class="tree-label">${esc(label)}</span>` +
    (sub ? ` <span class="muted">${esc(sub)}</span>` : '');
  div.onclick = (e) => { onclick(e); closeDrawer(); };
  div.addEventListener('keydown', (e) => {
    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key) && e.target === div) {
      // Roving focus over the rows the filter leaves visible.
      const rows = [...document.querySelectorAll('#tree .tree-item')].filter((r) => !r.hidden && r.offsetParent !== null);
      const at = rows.indexOf(div);
      const to = e.key === 'Home' ? 0 : e.key === 'End' ? rows.length - 1 : at + (e.key === 'ArrowDown' ? 1 : -1);
      if (rows[to]) { e.preventDefault(); rows[to].focus(); }
      return;
    }
    if (e.key === 'Enter' || e.key === ' ') {
      if (e.target === div || !e.target.closest('button')) {
        e.preventDefault();
        div.click();
      }
    }
  });
  return div;
}

function treeViewToggle() {
  const div = document.createElement('div');
  div.className = 'tree-view-toggle';
  div.innerHTML = `
    <button type="button" class="btn sm${treeView === 'server' ? ' active' : ''}" data-view="server">Server View</button>
    <button type="button" class="btn sm${treeView === 'namespace' ? ' active' : ''}" data-view="namespace" title="Group by Kubernetes namespace">Namespace View</button>
    <button type="button" class="btn sm${treeView === 'pool' ? ' active' : ''}" data-view="pool" title="User-defined pools; drag to regroup or to move between backends">Pool View</button>`;
  div.querySelectorAll('[data-view]').forEach((b) => {
    b.onclick = () => setTreeView(b.dataset.view);
  });
  return div;
}

// The tree filter (`/` focuses it) narrows the guest rows by name. It is built
// once and kept across re-renders: the 5s poll rebuilds the tree, and a
// rebuilt input would drop focus and the caret mid-word.
let treeFilter = '';
let treeFilterEl = null;

function treeFilterBox() {
  if (treeFilterEl) return treeFilterEl;
  treeFilterEl = document.createElement('input');
  treeFilterEl.id = 'tree-filter';
  treeFilterEl.type = 'search';
  treeFilterEl.placeholder = 'Filter guests…  /';
  treeFilterEl.setAttribute('aria-label', 'Filter the tree by guest name');
  treeFilterEl.addEventListener('input', () => { treeFilter = treeFilterEl.value.trim().toLowerCase(); applyTreeFilter(); });
  treeFilterEl.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { treeFilterEl.value = ''; treeFilter = ''; applyTreeFilter(); treeFilterEl.blur(); }
  });
  return treeFilterEl;
}

function applyTreeFilter() {
  $('#tree').querySelectorAll('.tree-item[data-guest]').forEach((row) => {
    row.hidden = !!treeFilter && !(row.dataset.search || row.dataset.guest.toLowerCase()).includes(treeFilter);
  });
}

export function focusTreeFilter() {
  $('#tree').classList.add('open'); // the drawer, on a phone
  const el = treeFilterBox();
  el.focus();
  el.select();
}

export function renderTree() {
  const tree = $('#tree');
  const filter = treeFilterBox();
  // Everything but the filter box goes; removing a focused input blurs it.
  for (const child of [...tree.children]) if (child !== filter) child.remove();
  if (!filter.isConnected) tree.appendChild(filter);
  tree.appendChild(treeViewToggle());

  const dcRow = treeRow({
    lvl: 0, icon: icon('datacenter'), label: 'Datacenter',
    sel: state.selected.type === 'dc',
    onclick: () => select({ type: 'dc' }),
  });
  attachContextMenu(dcRow, () => [{ icon: 'datacenter', label: 'Open Datacenter', action: () => select({ type: 'dc' }) }]);
  tree.appendChild(dcRow);

  const docRow = treeRow({
    lvl: 0, icon: icon('health'), label: 'Cluster health',
    sel: state.selected.type === 'doctor',
    onclick: () => select({ type: 'doctor' }),
  });
  attachContextMenu(docRow, () => [{ icon: 'health', label: 'Open Cluster health', action: () => select({ type: 'doctor' }) }]);
  tree.appendChild(docRow);

  const extRow = treeRow({
    lvl: 0, icon: icon('extension'), label: 'Extensions',
    sel: state.selected.type === 'extensions',
    onclick: () => select({ type: 'extensions' }),
  });
  attachContextMenu(extRow, () => [{ icon: 'extension', label: 'Open Extensions', action: () => select({ type: 'extensions' }) }]);
  tree.appendChild(extRow);

  const mvRow = treeRow({
    lvl: 0, icon: icon('cube'), label: 'Multiview',
    sub: 'live consoles',
    sel: state.selected.type === 'multiview',
    onclick: () => select({ type: 'multiview' }),
  });
  attachContextMenu(mvRow, () => [{ icon: 'cube', label: 'Open Multiview', action: () => select({ type: 'multiview' }) }]);
  tree.appendChild(mvRow);

  const setRow = treeRow({
    lvl: 0, icon: icon('cog'), label: 'Settings',
    sub: 'theme & branding',
    sel: state.selected.type === 'settings',
    onclick: () => select({ type: 'settings' }),
  });
  attachContextMenu(setRow, () => [{ icon: 'cog', label: 'Open Settings', action: () => select({ type: 'settings' }) }]);
  tree.appendChild(setRow);

  // Hosts that a host-power plugin can switch on and off (e.g. an on-demand
  // cloud VM node kept stopped when idle). Shown only when a plugin reports any.
  for (const h of state.hostPower.hosts || []) {
    const hpRow = treeRow({
      lvl: 0, icon: icon('server'), label: h.name,
      sub: h.state,
      dot: hostPowerDot(h.state),
      sel: state.selected.type === 'hostpower' && state.selected.key === hostPowerKey(h),
      onclick: () => select({ type: 'hostpower', key: hostPowerKey(h) }),
    });
    attachContextMenu(hpRow, () => hostPowerMenuItems(h));
    tree.appendChild(hpRow);
  }

  if (treeView === 'pool') renderTreePools(tree);
  else if (treeView === 'namespace') renderTreeNamespaces(tree);
  else renderTreeServer(tree);
  applyTreeFilter();
}

// CTs sit in the same per-node/per-namespace groups as VMs, distinguished
// only by icon — matching real Proxmox, which puts VMs and CTs in one
// resource tree per node/pool rather than segregating them.
function ctRow(c, lvl) {
  const row = treeRow({
    lvl, icon: icon('container'), label: c.name,
    sub: c.namespace,
    dot: c.ready ? 'on' : c.phase === 'Stopped' ? 'off' : 'mid',
    sel: state.selected.type === 'ct' && state.selected.key === ctKey(c),
    onclick: () => select({ type: 'ct', key: ctKey(c) }),
  });
  row.dataset.guest = c.name;
  attachContextMenu(row, () => ctMenuItems(c));
  return row;
}

// Server View: Datacenter → Node → VMs/CTs, grouped by .node. Guests with
// no placed node (stopped, unscheduled) render as top-level orphans.
// dropTargetNode accepts a dragged VM and proposes migrating it to that node.
// Invalid drops (node not ready, VM already there, non-KubeVirt VM) are
// refused with the reason as the row's tooltip.
function dropTargetNode(row, node) {
  dropZone(row, {
    defaultTitle: node.ready ? `Drop a VM here to migrate it to ${node.name}` : `Node ${node.name} (not ready)`,
    checkValid: (vm) => {
      if (!node.ready) return { ok: false, reason: `Node ${node.name} is not ready` };
      if (vm) {
        if (vm.node === node.name) return { ok: false, reason: `${vm.name} is already on ${node.name}` };
        if (vm.backend && vm.backend !== 'kubevirt') {
          return { ok: false, reason: `${vm.name} (${vm.backend}) cannot migrate to a cluster node` };
        }
      }
      return { ok: true };
    },
    onDrop: (vm) => { if (vm) migrateVM(vm, node.name); },
  });
}

function renderTreeServer(tree) {
  const byNode = (nodeName) => state.vms.filter((v) => v.node === nodeName);
  const ctsByNode = (nodeName) => state.cts.filter((c) => c.node === nodeName);
  const placed = new Set();
  const ctPlaced = new Set();

  for (const n of state.nodes) {
    const row = treeRow({
      lvl: 1, icon: icon('server'), label: n.name, sub: n.roles,
      dot: n.ready ? 'on' : 'off',
      sel: state.selected.type === 'node' && state.selected.name === n.name,
      onclick: () => select({ type: 'node', name: n.name }),
    });
    attachContextMenu(row, () => nodeMenuItems(n.name));
    dropTargetNode(row, n);
    tree.appendChild(row);
    for (const vm of byNode(n.name)) {
      placed.add(vmKey(vm));
      tree.appendChild(vmRow(vm, 2));
    }
    for (const c of ctsByNode(n.name)) {
      ctPlaced.add(ctKey(c));
      tree.appendChild(ctRow(c, 2));
    }
  }

  const orphans = state.vms.filter((v) => !placed.has(vmKey(v)));
  for (const vm of orphans) tree.appendChild(vmRow(vm, 1));
  const ctOrphans = state.cts.filter((c) => !ctPlaced.has(ctKey(c)));
  for (const c of ctOrphans) tree.appendChild(ctRow(c, 1));
}

// Namespace View: Datacenter → Namespace → VMs/CTs (templates included —
// they're still VMs in their namespace, just labeled differently by vmRow).
// Namespace is stable across live migration, unlike node.
//
// This groups by an axis the *backend* defines. Pool View groups by one the
// operator defines (ADR-0008); the names have to differ or nobody can tell
// which tree they are looking at.
function renderTreeNamespaces(tree) {
  const byNS = new Map();
  for (const vm of state.vms) {
    const ns = vm.namespace || '(none)';
    if (!byNS.has(ns)) byNS.set(ns, { vms: [], cts: [] });
    byNS.get(ns).vms.push(vm);
  }
  for (const c of state.cts) {
    const ns = c.namespace || '(none)';
    if (!byNS.has(ns)) byNS.set(ns, { vms: [], cts: [] });
    byNS.get(ns).cts.push(c);
  }
  const namespaces = [...byNS.keys()].sort();

  for (const ns of namespaces) {
    const { vms: nsVMs, cts: nsCTs } = byNS.get(ns);
    const parts = [];
    if (nsVMs.length) parts.push(`${nsVMs.length} VM${nsVMs.length === 1 ? '' : 's'}`);
    if (nsCTs.length) parts.push(`${nsCTs.length} CT${nsCTs.length === 1 ? '' : 's'}`);
    const row = treeRow({
      lvl: 1, icon: icon('folder'), label: ns, sub: parts.join(', '),
      sel: state.selected.type === 'namespace' && state.selected.name === ns,
      onclick: () => select({ type: 'namespace', name: ns }),
    });
    attachContextMenu(row, () => namespaceMenuItems(ns));
    tree.appendChild(row);
    for (const vm of nsVMs) tree.appendChild(vmRow(vm, 2));
    for (const c of nsCTs) tree.appendChild(ctRow(c, 2));
  }
}

export function vmRow(vm, lvl) {
  const row = treeRow({
    lvl, icon: icon(vm.isTemplate ? 'template' : 'cube'), label: vm.name,
    sub: vm.isTemplate ? 'template' : vm.namespace,
    dot: vm.ready ? 'on' : (vm.running ? 'mid' : 'off'),
    sel: state.selected.type === 'vm' && state.selected.key === vmKey(vm),
    onclick: (e) => treeVMClick(vmKey(vm), e),
  });
  row.dataset.vmKey = vmKey(vm);
  row.dataset.search = [vm.name, vm.ip, ...(vm.tags || [])].filter(Boolean).join(' ').toLowerCase();
  row.classList.toggle('multi-selected', state.selectedVMKeys.has(vmKey(vm)));
  row.setAttribute('aria-selected', state.selectedVMKeys.has(vmKey(vm)) ? 'true' : 'false');
  const check = document.createElement('input');
  check.type = 'checkbox';
  check.className = 'tree-vm-check';
  check.checked = state.selectedVMKeys.has(vmKey(vm));
  check.setAttribute('aria-label', `Select ${vm.name}`);
  check.onclick = (event) => event.stopPropagation();
  check.onchange = () => {
    check.checked ? state.selectedVMKeys.add(vmKey(vm)) : state.selectedVMKeys.delete(vmKey(vm));
    selectionAnchorKey = vmKey(vm);
    row.classList.toggle('multi-selected', check.checked);
    renderContent();
  };
  row.prepend(check);
  row.dataset.guest = vm.name;
  attachContextMenu(row, () => vmMenuItems(vm));
  makeDraggable(row, vm);
  return row;
}

// Tree selection: a plain click opens the VM and sets the range anchor;
// Ctrl/Cmd toggles one VM and Shift selects the visible range from the anchor.
// The set is the one the inventory grid uses, so both stay in step.
let selectionAnchorKey = null;

function treeVMClick(key, e) {
  if (e?.shiftKey && selectionAnchorKey) {
    const keys = [...document.querySelectorAll('#tree .tree-item[data-vm-key]')]
      .filter((r) => !r.hidden).map((r) => r.dataset.vmKey);
    const a = keys.indexOf(selectionAnchorKey);
    const b = keys.indexOf(key);
    if (a >= 0 && b >= 0) {
      state.selectedVMKeys.clear();
      keys.slice(Math.min(a, b), Math.max(a, b) + 1).forEach((k) => state.selectedVMKeys.add(k));
    }
  } else if (e?.ctrlKey || e?.metaKey) {
    if (state.selectedVMKeys.has(key)) state.selectedVMKeys.delete(key);
    else state.selectedVMKeys.add(key);
    selectionAnchorKey = key;
  } else {
    selectionAnchorKey = key;
    select({ type: 'vm', key });
    return;
  }
  renderTree();
  renderContent();
  markRendered();
}