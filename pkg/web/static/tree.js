// Sidebar tree: server, namespace and pool views, the filter box and the
// multi-select click model shared with the inventory grid.

import { ctKey, vmKey } from './api.js';
import { closeDrawer, markRendered, refresh, renderContent, select } from './app.js';
import { migrateVM } from './content/vm.js';
import { icon } from './icons.js';
import {
  ctMenuItems, namespaceMenuItems, nodeMenuItems, vmMenuItems,
} from './menus.js';
import { dropZone, loadPools, makeDraggable, renderTreePools } from './pools.js';
import { state } from './state.js';
import { capabilityTreeRows } from './ui/capabilities.js';
import { $, esc } from './ui/dom.js';
import { attachContextMenu } from './ui/menu.js';
import { keyed, reconcile } from './ui/reconcile.js';

// ── Tree (sidebar) ────────────────────────────────────────────────
// Two views over the same VM list, mirroring PVE's Server/Folder view
// toggle (see docs/adr — namespace is the stable grouping axis for
// KubeVirt: unlike node, it doesn't change under live migration).

const TREE_VIEW_KEY = 'corral-tree-view';
const TREE_VIEWS = ['server', 'namespace', 'pool', 'storage'];
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
  // Storage View reads the image catalogue, which the poll only fetches while
  // that view is showing. Forcing one refresh fetches it and redraws, rather
  // than showing an empty tree until the next 5s cycle.
  else if (v === 'storage') refresh(true);
  else renderTree();
}

// The next view in the strip, wrapping. Prism gives its view switch a key, and
// corral's four views of the same fleet were reachable by mouse alone.
export function nextTreeView() {
  const at = TREE_VIEWS.indexOf(treeView);
  const next = TREE_VIEWS[(at + 1) % TREE_VIEWS.length];
  setTreeView(next);
  return next;
}

export function treeRow({ lvl, icon, label, sub, sel, onclick, dot, key, sig }) {
  const div = document.createElement('div');
  div.className = `tree-item lvl-${lvl}${sel ? ' selected' : ''}`;
  div.setAttribute('tabindex', '0');
  // The label is its own element so it can be ellipsized: a bare text node is
  // an anonymous flex item and refuses to shrink, which is how a long VM name
  // used to push the row past the sidebar edge (#290).
  div.innerHTML = `${dot ? `<span class="dot ${dot}"></span>` : ''}${icon}` +
    ` <span class="tree-label">${esc(label)}</span>` +
    (sub ? ` <span class="muted">${esc(sub)}</span>` : '');
  if (key !== undefined) keyed(div, key, sig);
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
    <button type="button" class="btn sm${treeView === 'pool' ? ' active' : ''}" data-view="pool" title="User-defined pools; drag to regroup or to move between backends">Pool View</button>
    <button type="button" class="btn sm${treeView === 'storage' ? ' active' : ''}" data-view="storage" title="Image sources and imported disks, grouped by where they come from">Storage View</button>`;
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

// Rows are collected before they are placed, so the whole tree can be diffed
// against what is already on screen instead of replacing it. The sink stands in
// for the container and only has to answer appendChild, which is why the pool
// renderer in pools.js needs no changes to take part.
function rowSink() {
  const rows = [];
  return { rows, appendChild: (el) => { rows.push(el); return el; } };
}

export function renderTree() {
  const tree = $('#tree');
  const filter = treeFilterBox();
  if (!filter.isConnected) tree.insertBefore(filter, tree.firstChild);
  const sink = rowSink();
  sink.appendChild(keyed(treeViewToggle(), 'view-toggle', treeView));

  const dcRow = treeRow({
    lvl: 0, icon: icon('datacenter'), label: 'Datacenter',
    key: 'dc', sig: state.selected.type === 'dc',
    sel: state.selected.type === 'dc',
    onclick: () => select({ type: 'dc' }),
  });
  attachContextMenu(dcRow, () => [{ icon: 'datacenter', label: 'Open Datacenter', action: () => select({ type: 'dc' }) }]);
  sink.appendChild(dcRow);

  // The fleet drawn as the machines it runs on. Beside the Datacenter because
  // it is the same scope seen another way, not a grouping of the tree.
  const topoRow = treeRow({
    lvl: 0, icon: icon('server'), label: 'Topology',
    key: 'topology', sig: state.selected.type === 'topology',
    sel: state.selected.type === 'topology',
    onclick: () => select({ type: 'topology' }),
  });
  attachContextMenu(topoRow, () => [{ icon: 'server', label: 'Open Topology', action: () => select({ type: 'topology' }) }]);
  sink.appendChild(topoRow);

  const docRow = treeRow({
    lvl: 0, icon: icon('health'), label: 'Cluster health',
    key: 'doctor', sig: state.selected.type === 'doctor',
    sel: state.selected.type === 'doctor',
    onclick: () => select({ type: 'doctor' }),
  });
  attachContextMenu(docRow, () => [{ icon: 'health', label: 'Open Cluster health', action: () => select({ type: 'doctor' }) }]);
  sink.appendChild(docRow);

  const extRow = treeRow({
    lvl: 0, icon: icon('extension'), label: 'Extensions',
    key: 'extensions', sig: state.selected.type === 'extensions',
    sel: state.selected.type === 'extensions',
    onclick: () => select({ type: 'extensions' }),
  });
  attachContextMenu(extRow, () => [{ icon: 'extension', label: 'Open Extensions', action: () => select({ type: 'extensions' }) }]);
  sink.appendChild(extRow);

  const mvRow = treeRow({
    lvl: 0, icon: icon('cube'), label: 'Multiview',
    sub: 'live consoles',
    key: 'multiview', sig: state.selected.type === 'multiview',
    sel: state.selected.type === 'multiview',
    onclick: () => select({ type: 'multiview' }),
  });
  attachContextMenu(mvRow, () => [{ icon: 'cube', label: 'Open Multiview', action: () => select({ type: 'multiview' }) }]);
  sink.appendChild(mvRow);

  const setRow = treeRow({
    lvl: 0, icon: icon('cog'), label: 'Settings',
    sub: 'theme & branding',
    key: 'settings', sig: state.selected.type === 'settings',
    sel: state.selected.type === 'settings',
    onclick: () => select({ type: 'settings' }),
  });
  attachContextMenu(setRow, () => [{ icon: 'cog', label: 'Open Settings', action: () => select({ type: 'settings' }) }]);
  sink.appendChild(setRow);

  // Rows that a capability contributes, such as the machines a host-power
  // plugin can switch on and off. Each capability builds its own rows and
  // attaches its own menus; this file names none of them.
  capabilityTreeRows(sink);

  if (treeView === 'pool') renderTreePools(sink);
  else if (treeView === 'storage') renderTreeStorage(sink);
  else if (treeView === 'namespace') renderTreeNamespaces(sink);
  else renderTreeServer(sink);

  // The filter box is the one child that is not a keyed row: it is built once
  // and kept, so it is handed over as something to leave alone rather than
  // something to match.
  reconcile(tree, sink.rows, { keep: [filter] });
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
    key: `ct:${ctKey(c)}`,
    sig: [c, lvl, state.selected.type === 'ct' && state.selected.key === ctKey(c)],
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
export function dropTargetNode(row, node) {
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

function renderTreeServer(sink) {
  const byNode = (nodeName) => state.vms.filter((v) => v.node === nodeName);
  const ctsByNode = (nodeName) => state.cts.filter((c) => c.node === nodeName);
  const placed = new Set();
  const ctPlaced = new Set();

  for (const n of state.nodes) {
    const row = treeRow({
      lvl: 1, icon: icon('server'), label: n.name, sub: n.roles,
      dot: n.ready ? 'on' : 'off',
      key: `node:${n.name}`,
      sig: [n, state.selected.type === 'node' && state.selected.name === n.name],
      sel: state.selected.type === 'node' && state.selected.name === n.name,
      onclick: () => select({ type: 'node', name: n.name }),
    });
    attachContextMenu(row, () => nodeMenuItems(n.name));
    dropTargetNode(row, n);
    sink.appendChild(row);
    for (const vm of byNode(n.name)) {
      placed.add(vmKey(vm));
      sink.appendChild(vmRow(vm, 2));
    }
    for (const c of ctsByNode(n.name)) {
      ctPlaced.add(ctKey(c));
      sink.appendChild(ctRow(c, 2));
    }
  }

  const orphans = state.vms.filter((v) => !placed.has(vmKey(v)));
  for (const vm of orphans) sink.appendChild(vmRow(vm, 1));
  const ctOrphans = state.cts.filter((c) => !ctPlaced.has(ctKey(c)));
  for (const c of ctOrphans) sink.appendChild(ctRow(c, 1));
}

// Namespace View: Datacenter → Namespace → VMs/CTs (templates included —
// they're still VMs in their namespace, just labeled differently by vmRow).
// Namespace is stable across live migration, unlike node.
//
// This groups by an axis the *backend* defines. Pool View groups by one the
// operator defines (ADR-0008); the names have to differ or nobody can tell
// which tree they are looking at.
function renderTreeNamespaces(sink) {
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
      key: `ns:${ns}`,
      sig: [ns, parts, state.selected.type === 'namespace' && state.selected.name === ns],
      sel: state.selected.type === 'namespace' && state.selected.name === ns,
      onclick: () => select({ type: 'namespace', name: ns }),
    });
    attachContextMenu(row, () => namespaceMenuItems(ns));
    sink.appendChild(row);
    for (const vm of nsVMs) sink.appendChild(vmRow(vm, 2));
    for (const c of nsCTs) sink.appendChild(ctRow(c, 2));
  }
}

// Storage View: Datacenter -> source -> images, plus imported disks grouped by
// namespace. This is the fourth of Proxmox's tree views (Server, Storage, Pool,
// Folder) and the one corral was missing: the same objects, grouped by where
// their bits come from rather than by what is running them.
//
// The rows are deliberately not drop targets. An image is not a guest, and
// nothing in the API moves an image between sources, so a drag here would
// promise something that cannot happen.
function renderTreeStorage(sink) {
  const bySource = new Map();
  for (const img of state.images || []) {
    const src = img.source || '(built in)';
    if (!bySource.has(src)) bySource.set(src, []);
    bySource.get(src).push(img);
  }

  for (const src of [...bySource.keys()].sort()) {
    const images = bySource.get(src);
    const custom = images.some((i) => i.custom);
    const row = treeRow({
      lvl: 1, icon: icon('disk'), label: src,
      sub: `${images.length} image${images.length === 1 ? '' : 's'}${custom ? ' · custom' : ''}`,
      key: `src:${src}`,
      sig: [src, images, state.selected.type === 'storage' && state.selected.name === src],
      sel: state.selected.type === 'storage' && state.selected.name === src,
      onclick: () => select({ type: 'storage', name: src }),
    });
    sink.appendChild(row);
    for (const img of images) {
      sink.appendChild(treeRow({
        lvl: 2, icon: icon(img.custom ? 'template' : 'disk'), label: img.name,
        sub: img.variant || '',
        key: `img:${src}/${img.name}`,
        sig: [img, state.selected.type === 'storage' && state.selected.name === src],
        sel: false,
        onclick: () => select({ type: 'storage', name: src }),
      }));
    }
  }

  // Imported disks are storage too, and the only part of this view that an
  // operator creates rather than consumes.
  const dvs = state.dataVolumes || [];
  if (dvs.length) {
    const byNS = new Map();
    for (const dv of dvs) {
      const ns = dv.namespace || '(none)';
      if (!byNS.has(ns)) byNS.set(ns, []);
      byNS.get(ns).push(dv);
    }
    for (const ns of [...byNS.keys()].sort()) {
      const group = byNS.get(ns);
      sink.appendChild(treeRow({
        lvl: 1, icon: icon('folder'), label: `${ns} disks`,
        sub: `${group.length} imported`,
        key: `dvns:${ns}`,
        sig: [ns, group, state.selected.type === 'storage' && state.selected.name === `dv:${ns}`],
        sel: state.selected.type === 'storage' && state.selected.name === `dv:${ns}`,
        onclick: () => select({ type: 'storage', name: `dv:${ns}` }),
      }));
      for (const dv of group) {
        sink.appendChild(treeRow({
          lvl: 2, icon: icon('disk'), label: dv.name,
          sub: dv.phase || dv.size || '',
          key: `dv:${ns}/${dv.name}`,
          sig: [dv],
          sel: false,
          onclick: () => select({ type: 'storage', name: `dv:${ns}` }),
        }));
      }
    }
  }

  if (!bySource.size && !dvs.length) {
    sink.appendChild(treeRow({
      lvl: 1, icon: icon('disk'), label: 'No images',
      sub: 'nothing to show yet',
      key: 'storage:empty', sig: 'empty', sel: false,
      onclick: () => select({ type: 'storage', name: '' }),
    }));
  }
}

export function vmRow(vm, lvl) {
  const row = treeRow({
    lvl, icon: icon(vm.isTemplate ? 'template' : 'cube'), label: vm.name,
    sub: vm.isTemplate ? 'template' : vm.namespace,
    dot: vm.ready ? 'on' : (vm.running ? 'mid' : 'off'),
    key: `vm:${vmKey(vm)}`,
    // The whole VM goes into the signature, not just what the row shows: the
    // context menu and the drag payload close over this object, so a row
    // reused after an invisible field changed would act on stale data.
    sig: [vm, lvl, state.selected.type === 'vm' && state.selected.key === vmKey(vm),
      state.selectedVMKeys.has(vmKey(vm))],
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