// Corral web UI — Proxmox-style dashboard for KubeVirt.
// Vanilla JS; noVNC + xterm.js loaded from CDN for the consoles.

import { icon } from './icons.js';

const $ = (sel) => document.querySelector(sel);

let vms = [];
let nodes = [];
let caps = { storageClass: '', canExpand: false, canSnapshot: false };
// Authenticated tailnet identity + privilege (see /api/whoami). Defaults to
// admin so the UI is fully enabled until told otherwise (single-user mode).
let me = { login: '', name: '', admin: true, enforced: false };
let availableNADs = [];
let selected = { type: 'dc' }; // {type:'dc'} | {type:'node',name} | {type:'vm',key}
let tab = 'summary';
let rfb = null;        // noVNC connection
let term = null;       // xterm instance
let ttyWS = null;      // serial console websocket

// ── API ───────────────────────────────────────────────────────────

async function api(path, opts = {}) {
  const r = await fetch(path, opts);
  if (!r.ok) {
    let msg = r.statusText;
    try { msg = (await r.json()).error || msg; } catch { /* not json */ }
    throw new Error(msg);
  }
  return r.headers.get('content-type')?.includes('json') ? r.json() : r.text();
}

function toast(msg) {
  document.querySelectorAll('.toast').forEach((t) => t.remove());
  const el = document.createElement('div');
  el.className = 'toast';
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 6000);
}

const esc = (s) => String(s ?? '').replace(/[&<>"']/g,
  (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const vmKey = (vm) => `${vm.namespace}/${vm.name}`;
const findVM = (key) => vms.find((v) => vmKey(v) === key);

// Fingerprint of the last-rendered state. The 5s poll only re-renders when
// the data (or what's selected) actually changed — otherwise innerHTML
// replacement would reset scroll position and text selection on every tick.
let lastRenderFp = '';

async function refresh(force = false) {
  try {
    [vms, nodes] = await Promise.all([api('/api/vms'), api('/api/nodes')]);
  } catch (e) {
    toast(`Refresh failed: ${e.message}`);
    return;
  }
  const fp = JSON.stringify([vms, nodes, selected, tab]);
  if (!force && fp === lastRenderFp) return; // nothing changed — keep the DOM
  lastRenderFp = fp;

  // Re-render, preserving scroll positions across the DOM swap.
  const treeEl = $('#tree');
  const contentEl = $('#content');
  const treeScroll = treeEl ? treeEl.scrollTop : 0;
  const contentScroll = contentEl ? contentEl.scrollTop : 0;
  renderTree();
  // Don't clobber live consoles (or the multiview grid) on poll.
  if (tab !== 'console' && tab !== 'terminal' && selected.type !== 'multiview') renderContent();
  if (treeEl) treeEl.scrollTop = treeScroll;
  if (contentEl) contentEl.scrollTop = contentScroll;
}

async function loadCaps() {
  try { caps = await api('/api/capabilities'); } catch { /* keep defaults */ }
  // bootc/windows are optional plugins — hide their source options when absent.
  if (!caps.bootc) {
    document.querySelector('[name=sourceType] option[value=bootc]')?.remove();
  }
  // The Windows create flow is compiled into the web server (manifest gen in
  // pkg/kubevirt), so it's always available — no plugin install needed.
  try { availableNADs = await api('/api/nads'); } catch { availableNADs = []; }
}

// loadWhoami fetches the caller identity and flips the UI into read-only mode
// for non-admins (the server still enforces this; the UI just hides controls).
async function loadWhoami() {
  try { me = await api('/api/whoami'); } catch { return; }
  const el = $('#whoami');
  if (el) {
    const badge = me.admin ? '' : '<span class="ro-badge">read-only</span>';
    if (me.login) el.innerHTML = `<span class="who-name">${esc(me.name || me.login)}</span>${badge}`;
    else if (me.enforced) el.innerHTML = badge; // behind the gate but unidentified
    else el.textContent = '';
  }
  document.body.classList.toggle('read-only', !me.admin);
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

// ── Tree (sidebar) ────────────────────────────────────────────────

function treeRow({ lvl, icon, label, sub, sel, onclick, dot }) {
  const div = document.createElement('div');
  div.className = `tree-item lvl-${lvl}${sel ? ' selected' : ''}`;
  div.innerHTML = `${dot ? `<span class="dot ${dot}"></span>` : ''}${icon} ${esc(label)}` +
    (sub ? ` <span class="muted">${esc(sub)}</span>` : '');
  div.onclick = () => { onclick(); closeDrawer(); };
  return div;
}

function renderTree() {
  const tree = $('#tree');
  tree.replaceChildren();

  tree.appendChild(treeRow({
    lvl: 0, icon: icon('datacenter'), label: 'Datacenter',
    sel: selected.type === 'dc',
    onclick: () => select({ type: 'dc' }),
  }));

  tree.appendChild(treeRow({
    lvl: 0, icon: icon('health'), label: 'Cluster health',
    sel: selected.type === 'doctor',
    onclick: () => select({ type: 'doctor' }),
  }));

  tree.appendChild(treeRow({
    lvl: 0, icon: icon('extension'), label: 'Extensions',
    sel: selected.type === 'extensions',
    onclick: () => select({ type: 'extensions' }),
  }));

  tree.appendChild(treeRow({
    lvl: 0, icon: icon('cube'), label: 'Multiview',
    sub: 'live consoles',
    sel: selected.type === 'multiview',
    onclick: () => select({ type: 'multiview' }),
  }));

  const byNode = (nodeName) => vms.filter((v) => v.node === nodeName);
  const placed = new Set();

  for (const n of nodes) {
    tree.appendChild(treeRow({
      lvl: 1, icon: icon('server'), label: n.name, sub: n.roles,
      dot: n.ready ? 'on' : 'off',
      sel: selected.type === 'node' && selected.name === n.name,
      onclick: () => select({ type: 'node', name: n.name }),
    }));
    for (const vm of byNode(n.name)) {
      placed.add(vmKey(vm));
      tree.appendChild(vmRow(vm, 2));
    }
  }

  const orphans = vms.filter((v) => !placed.has(vmKey(v)));
  for (const vm of orphans) tree.appendChild(vmRow(vm, 1));
}

function vmRow(vm, lvl) {
  return treeRow({
    lvl, icon: icon(vm.isTemplate ? 'template' : 'cube'), label: vm.name,
    sub: vm.isTemplate ? 'template' : vm.namespace,
    dot: vm.ready ? 'on' : (vm.running ? 'mid' : 'off'),
    sel: selected.type === 'vm' && selected.key === vmKey(vm),
    onclick: () => select({ type: 'vm', key: vmKey(vm) }),
  });
}

// markRendered records the just-rendered state so the next poll tick doesn't
// re-render (and reset scroll) for a change the user already saw.
function markRendered() {
  lastRenderFp = JSON.stringify([vms, nodes, selected, tab]);
}

function select(sel) {
  disconnectConsoles();
  if (selected.type === 'multiview' && sel.type !== 'multiview') disconnectMultiview();
  selected = sel;
  tab = 'summary';
  renderTree();
  renderContent();
  markRendered();
}

// ── Content panel ─────────────────────────────────────────────────

function renderContent() {
  const main = $('#content');
  if (selected.type === 'vm') {
    const vm = findVM(selected.key);
    if (!vm) { selected = { type: 'dc' }; }
    else return renderVM(main, vm);
  }
  if (selected.type === 'node') return renderNode(main, selected.name);
  if (selected.type === 'extensions') return renderExtensions(main);
  if (selected.type === 'doctor') return renderDoctor(main);
  if (selected.type === 'multiview') return renderMultiview(main);
  return renderDatacenter(main);
}

// ── Multiview: a grid of live view-only consoles ──────────────────
// Watch 4–6 VMs at once — built for monitoring automated GUI testing.

let multiviewRFBs = [];

function disconnectMultiview() {
  for (const r of multiviewRFBs) { try { r.disconnect(); } catch { /* gone */ } }
  multiviewRFBs = [];
}

async function renderMultiview(main) {
  disconnectMultiview();
  const running = vms.filter((v) => v.running);
  const shown = running.slice(0, 6);
  main.innerHTML = `
    <div class="page-head"><h1>${icon('cube')} Multiview</h1>
      <span class="muted">${running.length} running VM${running.length === 1 ? '' : 's'}${running.length > 6 ? ' — showing first 6' : ''}</span>
    </div>
    ${shown.length ? `<div id="mv-grid" class="mv-grid"></div>`
      : `<p class="console-msg">No running VMs. Start some and they appear here, live.</p>`}`;
  if (!shown.length) return;

  let RFB;
  try {
    ({ default: RFB } = await import(
      'https://cdn.jsdelivr.net/npm/@novnc/novnc@1.4.0/core/rfb.js'));
  } catch (e) {
    $('#mv-grid').innerHTML = `<p class="console-msg">noVNC failed to load: ${esc(e.message)}</p>`;
    return;
  }
  const grid = $('#mv-grid');
  for (const vm of shown) {
    const tile = document.createElement('div');
    tile.className = 'mv-tile';
    tile.innerHTML = `<div class="mv-title">${esc(vm.name)} <span class="muted">${esc(vm.namespace)}</span></div>
      <div class="mv-screen"></div>`;
    tile.querySelector('.mv-title').onclick = () => {
      disconnectMultiview();
      select({ type: 'vm', key: vmKey(vm) });
      tab = 'console';
      renderContent();
    };
    grid.appendChild(tile);
    try {
      const rfb = new RFB(tile.querySelector('.mv-screen'), wsURL('vnc', vm));
      rfb.viewOnly = true; // watch, don't type — click the title to take over
      rfb.scaleViewport = true;
      rfb.addEventListener('disconnect', () => {
        tile.querySelector('.mv-screen').innerHTML = `<p class="console-msg">disconnected</p>`;
      });
      multiviewRFBs.push(rfb);
    } catch {
      tile.querySelector('.mv-screen').innerHTML = `<p class="console-msg">connect failed</p>`;
    }
  }
}

async function renderDoctor(main) {
  main.innerHTML = `<div class="page-head"><h1>${icon('health')} Cluster health</h1>
    <button class="btn primary" id="doc-fix" hidden>Reconcile fixable</button></div>
    <p class="muted" style="margin-bottom:14px">What Corral's features need from the cluster.
      Fixable items are safe, config-only changes Corral can apply.</p>
    <div id="doc-list"><p class="muted">checking…</p></div>`;
  let checks;
  try { checks = await api('/api/doctor'); }
  catch (e) { $('#doc-list').innerHTML = `<p class="console-msg">${esc(e.message)}</p>`; return; }
  const fixBtn = $('#doc-fix');
  fixBtn.hidden = !checks.some((c) => !c.ok && c.fixable);
  fixBtn.onclick = async () => {
    fixBtn.disabled = true; fixBtn.textContent = 'Reconciling…';
    try { await api('/api/doctor/fix', { method: 'POST' }); toast('Reconciled'); }
    catch (e) { toast(e.message); }
    renderDoctor(main);
  };
  $('#doc-list').innerHTML = `<table><tbody>
    ${checks.map((c) => `<tr>
      <td style="width:1.5rem">${c.ok ? '<span class="dot on"></span>' : '<span class="dot off"></span>'}</td>
      <td><strong ${c.ok ? '' : 'class="doc-broken"'}>${esc(c.name)}</strong></td>
      <td class="${c.ok ? 'muted' : 'doc-broken'}">${esc(c.detail)}</td>
      <td>${!c.ok && c.fixable ? `<button class="btn sm" data-fix="${esc(c.name)}">Fix</button>` : ''}</td>
    </tr>`).join('')}
  </tbody></table>`;
  // Per-check fix buttons — scoped reconcile of just that item.
  $('#doc-list').querySelectorAll('[data-fix]').forEach((b) => {
    b.onclick = async () => {
      b.disabled = true; b.textContent = 'Fixing…';
      try {
        await api('/api/doctor/fix', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ check: b.dataset.fix }),
        });
        toast(`Fixed: ${b.dataset.fix}`);
      } catch (e) { toast(e.message); }
      renderDoctor(main);
    };
  });
}

async function renderExtensions(main) {
  main.innerHTML = `<div class="page-head"><h1>${icon('extension')} Extensions</h1></div>
    <p class="muted" style="margin-bottom:14px">Optional plugins from the Corral marketplace.
      Installed plugins add <code>corral &lt;name&gt;</code> commands.</p>
    <div id="ext-list"><p class="muted">loading…</p></div>`;
  let list;
  try { list = await api('/api/plugins'); }
  catch (e) { $('#ext-list').innerHTML = `<p class="console-msg">${esc(e.message)}</p>`; return; }
  if (!list.length) { $('#ext-list').innerHTML = `<p class="muted">No extensions available.</p>`; return; }
  $('#ext-list').innerHTML = `<div class="ext-grid">${list.map((p) => `
    <div class="ext-card">
      <div class="ext-head">${icon('extension')} <strong>${esc(p.name)}</strong>
        <span class="muted">${esc(p.version || '')}</span>
        ${p.installed ? '<span class="pill on">installed</span>' : ''}</div>
      <div class="ext-desc">${esc(p.description || '')}</div>
      <div class="ext-actions">
        ${p.installed
          ? `<button class="btn sm danger" data-ext-rm="${esc(p.name)}">Remove</button>`
          : (p.inStore ? `<button class="btn sm primary" data-ext-add="${esc(p.name)}">Install</button>` : '')}
        ${p.homepage ? `<a class="btn sm" href="${esc(p.homepage)}" target="_blank" rel="noopener">Homepage</a>` : ''}
      </div>
    </div>`).join('')}</div>`;
  main.querySelectorAll('[data-ext-add]').forEach((b) => {
    b.onclick = async () => {
      b.disabled = true; b.textContent = 'Installing…';
      try { await api(`/api/plugins/${b.dataset.extAdd}/install`, { method: 'POST' }); toast('Installed'); }
      catch (e) { toast(e.message); }
      renderExtensions(main);
    };
  });
  main.querySelectorAll('[data-ext-rm]').forEach((b) => {
    b.onclick = async () => {
      try { await api(`/api/plugins/${b.dataset.extRm}`, { method: 'DELETE' }); toast('Removed'); }
      catch (e) { toast(e.message); }
      renderExtensions(main);
    };
  });
}

function renderDatacenter(main) {
  const running = vms.filter((v) => v.ready).length;
  const ready = nodes.filter((n) => n.ready).length;
  main.innerHTML = `
    <div class="page-head"><h1>Datacenter</h1></div>
    <div class="cards">
      <div class="card"><div class="num">${vms.length}</div><div class="label">virtual machines</div></div>
      <div class="card"><div class="num">${running}</div><div class="label">running</div></div>
      <div class="card"><div class="num">${ready}/${nodes.length}</div><div class="label">nodes ready</div></div>
    </div>
    ${vmTable(vms)}
    <h2 class="section">${icon('disk')} Image library
      <button class="btn" id="dc-import">${icon('download')} Import image</button>
    </h2>
    <div id="dc-images"><p class="muted">loading…</p></div>`;
  bindVMTable(main);
  $('#dc-import').onclick = importImage;
  loadImages();
}

async function loadImages() {
  const el = $('#dc-images');
  if (!el) return;
  let dvs;
  try { dvs = await api('/api/datavolumes'); }
  catch (e) { el.innerHTML = `<p class="muted">${esc(e.message)}</p>`; return; }
  if (!dvs.length) { el.innerHTML = `<p class="muted">No imported images. Use “Import image” for an ISO/qcow2 URL.</p>`; return; }
  el.innerHTML = `<table><thead><tr>
      <th>Name</th><th>Namespace</th><th>Size</th><th>Status</th><th>Source</th><th></th>
    </tr></thead><tbody>
    ${dvs.map((d) => `<tr>
      <td>${esc(d.name)}</td><td>${esc(d.namespace)}</td><td>${esc(d.size || '—')}</td>
      <td>${esc(d.phase || '—')}${d.progress && d.phase !== 'Succeeded' ? ` (${esc(d.progress)})` : ''}</td>
      <td class="muted" style="max-width:280px;overflow:hidden;text-overflow:ellipsis">${esc(d.source || '')}</td>
      <td><button class="btn sm danger" data-deldv="${esc(d.namespace)}/${esc(d.name)}">${icon('trash')}</button></td>
    </tr>`).join('')}
    </tbody></table>`;
  el.querySelectorAll('[data-deldv]').forEach((b) => {
    b.onclick = async () => {
      const [ns, name] = b.dataset.deldv.split('/');
      if (!confirm(`Delete image ${name}?`)) return;
      try { await api(`/api/datavolumes/${ns}/${name}`, { method: 'DELETE' }); toast('Deleted'); }
      catch (e) { toast(e.message); }
      setTimeout(loadImages, 500);
    };
  });
}

async function importImage() {
  const url = prompt('Image URL (ISO / qcow2 / raw, http[s]):', '');
  if (!url) return;
  const guess = (url.split('/').pop() || 'image').replace(/[^a-z0-9-]/gi, '-').toLowerCase().slice(0, 40);
  const name = prompt('Name for this image:', guess);
  if (!name) return;
  const size = prompt('Disk size for the import (e.g. 10Gi):', '10Gi') || '10Gi';
  try {
    await api('/api/datavolumes', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, namespace: '', url, size }),
    });
    toast('Import started');
    setTimeout(loadImages, 800);
  } catch (e) { toast(e.message); }
}

function renderNode(main, name) {
  const n = nodes.find((x) => x.name === name);
  const nodeVMs = vms.filter((v) => v.node === name);
  main.innerHTML = `
    <div class="page-head">
      <h1>${icon('server')} ${esc(name)}</h1>
      <span class="pill ${n?.ready ? 'on' : 'off'}">${n?.ready ? 'ready' : 'not ready'}</span>
    </div>
    <dl class="props">
      <dt>Roles</dt><dd>${esc(n?.roles || '—')}</dd>
      <dt>Kubelet</dt><dd>${esc(n?.kubelet || '—')}</dd>
      <dt>Architecture</dt><dd>${esc(n?.arch || '—')}</dd>
      <dt>VMs</dt><dd>${nodeVMs.length}</dd>
    </dl>
    <h2 style="font-size:1rem;margin:18px 0 8px">Virtual machines</h2>
    ${vmTable(nodeVMs)}`;
  bindVMTable(main);
}

function vmTable(list) {
  if (!list.length) return `<p class="console-msg">No virtual machines.</p>`;
  return `<table><thead><tr>
      <th>Name</th><th>Status</th><th>Node</th><th>Namespace</th><th>CPU</th><th>Mem</th><th>IP</th>
    </tr></thead><tbody>
    ${list.map((v) => `<tr data-key="${esc(vmKey(v))}">
      <td>${esc(v.name)}</td>
      <td><span class="dot ${v.ready ? 'on' : v.running ? 'mid' : 'off'}"></span> ${esc(v.status)}</td>
      <td>${esc(v.node || '—')}</td><td>${esc(v.namespace)}</td>
      <td>${v.cpu}</td><td>${esc(v.mem)}</td><td>${esc(v.ip || '—')}</td>
    </tr>`).join('')}
    </tbody></table>`;
}

function bindVMTable(root) {
  root.querySelectorAll('tr[data-key]').forEach((tr) => {
    tr.onclick = () => select({ type: 'vm', key: tr.dataset.key });
  });
}

// ── VM view ───────────────────────────────────────────────────────

const TABS = [
  ['summary', 'Summary'],
  ['console', 'Console'],
  ['terminal', 'Terminal'],
  ['hardware', 'Hardware'],
  ['snapshots', 'Snapshots'],
  ['events', 'Events'],
  ['yaml', 'YAML'],
];

function renderVM(main, vm) {
  main.innerHTML = `
    <div class="page-head">
      <h1>${icon('cube')} ${esc(vm.name)}</h1>
      <span class="pill ${vm.ready ? 'on' : 'off'}">${esc(vm.status)}</span>
      <div class="toolbar">
        <button class="btn" data-act="start" ${vm.running ? 'disabled' : ''}>${icon('play')} Start</button>
        <button class="btn" data-act="stop" ${vm.running ? '' : 'disabled'}>${icon('stop')} Stop</button>
        <button class="btn" data-act="restart" ${vm.running ? '' : 'disabled'}>${icon('restart')} Restart</button>
        <button class="btn" data-act="pause" ${vm.ready ? '' : 'disabled'}>${icon('pause')} Pause</button>
        <button class="btn" data-act="unpause">${icon('play')} Resume</button>
        <button class="btn" data-act="migrate" ${vm.ready && vm.liveMigratable ? '' : 'disabled'}
          title="${vm.liveMigratable ? 'Live-migrate to another node' : 'Not live-migratable (persistent RWO disk)'}">${icon('migrate')} Migrate</button>
        <button class="btn" data-act="clone">${icon('clone')} Clone</button>
        <button class="btn" data-act="template" title="${vm.isTemplate ? 'Remove template mark' : 'Mark as a golden template to clone from'}">
          ${icon('template')} ${vm.isTemplate ? 'Unmark template' : 'Make template'}</button>
        <button class="btn" data-act="export" ${vm.running ? 'disabled' : ''}
          title="${vm.running ? 'Stop the VM to export its disk' : 'Download a disk backup'}">${icon('download')} Export</button>
        ${vm.bootc && caps.bootc ? `<button class="btn" data-act="upgrade"
          title="Rebuild this bootc VM's disk from the latest image and restart">${icon('restart')} Upgrade</button>` : ''}
        <button class="btn danger" data-act="delete">${icon('trash')} Delete</button>
      </div>
    </div>
    <div class="tabs">
      ${TABS.map(([id, label]) =>
        `<div class="tab ${tab === id ? 'active' : ''}" data-tab="${id}">${label}</div>`).join('')}
    </div>
    <div id="tab-body"></div>`;

  main.querySelectorAll('[data-act]').forEach((b) => {
    b.onclick = () => vmAction(vm, b.dataset.act);
  });
  main.querySelectorAll('[data-tab]').forEach((t) => {
    t.onclick = () => { disconnectConsoles(); tab = t.dataset.tab; renderVM(main, vm); markRendered(); };
  });

  renderTab(vm);
}

function renderTab(vm) {
  const body = $('#tab-body');
  switch (tab) {
    case 'summary':
      body.innerHTML = `<dl class="props">
        <dt>Status</dt><dd>${esc(vm.status)}</dd>
        <dt>Namespace</dt><dd>${esc(vm.namespace)}</dd>
        <dt>Node</dt><dd>${esc(vm.node || '—')}</dd>
        <dt>vCPUs</dt><dd>${vm.cpu}</dd>
        <dt>Memory</dt><dd>${esc(vm.mem)}</dd>
        <dt>Live usage</dt><dd id="vm-usage">${vm.running ? '…' : '—'}</dd>
        <dt>Pod IP</dt><dd>${esc(vm.ip || '—')}</dd>
        <dt>Live-migratable</dt><dd>${vm.liveMigratable ? 'yes' : 'no'}</dd>
        <dt>Guest agent</dt><dd>${vm.agentConnected ? 'connected' : 'not connected'}</dd>
        <dt>Tailnet proxy</dt><dd>${esc(vm.vnc || 'off')}</dd>
        <dt>SSH</dt><dd><code>corral ssh ${esc(vm.name)}</code></dd>
        <dt>RDP</dt><dd id="vm-rdp">${vm.running ? 'checking…' : '—'}</dd>
      </dl>
      <div id="guest-info"></div>
      <div id="powersched-box" class="panel-section"><p class="muted">loading schedule…</p></div>`;
      if (vm.running) loadMetrics(vm);
      if (vm.running) checkRDP(vm);
      if (vm.agentConnected) loadGuestInfo(vm);
      renderPowerSchedule(vm);
      break;
    case 'console': connectVNC(vm, body); break;
    case 'terminal': connectTTY(vm, body); break;
    case 'hardware': renderHardware(vm, body); break;
    case 'snapshots': renderSnapshots(vm, body); break;
    case 'events': renderEvents(vm, body); break;
    case 'yaml':
      body.innerHTML = `<pre class="yaml">loading…</pre>`;
      api(`/api/vms/${vm.namespace}/${vm.name}`)
        .then((j) => { body.querySelector('pre').textContent = JSON.stringify(j, null, 2); })
        .catch((e) => { body.querySelector('pre').textContent = e.message; });
      break;
  }
}

async function vmAction(vm, act) {
  if (act === 'delete') {
    if (!confirm(`Delete ${vm.name} and its disks?`)) return;
    try {
      await api(`/api/vms/${vm.namespace}/${vm.name}`, { method: 'DELETE' });
      select({ type: 'dc' });
    } catch (e) { toast(e.message); }
    return refresh();
  }
  if (act === 'migrate') return migrateVM(vm);
  if (act === 'clone') return cloneVM(vm);
  if (act === 'template') {
    try { await post(vm, '/template', { on: !vm.isTemplate }); toast(vm.isTemplate ? 'Template mark removed' : 'Marked as template'); }
    catch (e) { toast(e.message); }
    return setTimeout(refresh, 600);
  }
  if (act === 'export') {
    toast('Preparing backup… the download will start when ready.');
    window.location.href = `/api/vms/${vm.namespace}/${vm.name}/export`;
    return;
  }
  if (act === 'upgrade') {
    const ref = prompt('Rebuild from which bootc image?\nLeave blank to pull the latest of the current image, or enter a new image to switch.', '');
    if (ref === null) return; // cancelled
    try {
      const res = await api(`/api/vms/${vm.namespace}/${vm.name}/bootc/rebuild`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ image: ref.trim() }),
      });
      if (res.task) watchBuild(res.task, vm.name);
    } catch (e) { toast(e.message); }
    return;
  }
  try {
    await api(`/api/vms/${vm.namespace}/${vm.name}/${act}`, { method: 'POST' });
  } catch (e) { toast(e.message); }
  setTimeout(refresh, 800);
}

async function post(vm, path, body) {
  return api(`/api/vms/${vm.namespace}/${vm.name}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body || {}),
  });
}

async function migrateVM(vm) {
  const others = nodes.filter((n) => n.ready && n.name !== vm.node).map((n) => n.name);
  const target = others.length
    ? prompt(`Migrate ${vm.name} to which node?\nAvailable: ${others.join(', ')}\n(leave blank to let the scheduler choose)`, others[0] ?? '')
    : '';
  if (target === null) return; // cancelled
  try {
    await post(vm, '/migrate', { targetNode: target.trim() });
    toast(`Migrating ${vm.name}…`);
  } catch (e) { toast(e.message); }
  setTimeout(refresh, 800);
}

async function cloneVM(vm) {
  const target = prompt(`Clone ${vm.name} to a new VM named:`, `${vm.name}-clone`);
  if (!target) return;
  try {
    await post(vm, '/clone', { target: target.trim() });
    toast(`Cloning ${vm.name} → ${target}…`);
  } catch (e) { toast(e.message); }
  setTimeout(refresh, 1500);
}

async function loadGuestInfo(vm) {
  let info;
  try { info = await api(`/api/vms/${vm.namespace}/${vm.name}/guestinfo`); }
  catch { return; /* agent dropped */ }
  const os = info.os || {};
  const fss = Array.isArray(info.filesystems) ? info.filesystems : (info.filesystems?.items || []);
  const el = $('#guest-info');
  if (!el) return;
  el.innerHTML = `<h2 class="section">Guest agent</h2>
    <dl class="props">
      <dt>OS</dt><dd>${esc(os.prettyName || os.name || '—')}</dd>
      <dt>Kernel</dt><dd>${esc(os.kernelRelease || '—')}</dd>
      <dt>Hostname</dt><dd>${esc(os.hostname || '—')}</dd>
      ${fss.map((f) => `<dt>FS ${esc(f.mountPoint || f.name || '?')}</dt>
        <dd>${esc(f.fileSystemType || '')} ${f.usedBytes != null ? `· ${gib(f.usedBytes)}/${gib(f.totalBytes)} GiB` : ''}</dd>`).join('')}
    </dl>`;
}

const gib = (b) => (Number(b || 0) / 1073741824).toFixed(1);

// Probe the VM for an open RDP port (Windows native, or Linux via
// gnome-remote-desktop/xrdp) and surface how to connect.
async function checkRDP(vm) {
  let r;
  try { r = await api(`/api/vms/${vm.namespace}/${vm.name}/rdp`); } catch { r = { open: false }; }
  const el = $('#vm-rdp');
  if (!el) return; // user navigated away
  el.innerHTML = r.open
    ? `available — <code>virtctl port-forward vm/${esc(vm.name)} 3389:3389 -n ${esc(vm.namespace)}</code> then point your RDP client at localhost:3389`
    : '—';
}

async function loadMetrics(vm) {
  try {
    const m = await api(`/api/vms/${vm.namespace}/${vm.name}/metrics`);
    const el = $('#vm-usage');
    if (el) el.textContent = (m.cpu || m.mem) ? `${m.cpu || '?'} CPU · ${m.mem || '?'} mem` : 'no metrics yet';
  } catch { /* metrics-server may be absent */ }
}

// Autostart/shutdown windows (schedule plugin): two cron boundaries that flip
// the VM on/off. Built into the web server (pkg/cronops) — no plugin needed.
async function renderPowerSchedule(vm) {
  const box = $('#powersched-box');
  if (!box) return;
  let s = {};
  try { s = await api(`/api/vms/${vm.namespace}/${vm.name}/powerschedule`); } catch { /* form */ }
  const has = s && (s.start || s.stop);
  box.innerHTML = `
    <h2 class="section">${icon('play')} Autostart / shutdown windows</h2>
    <div class="hw-edit">
      <label>Start (cron) <input id="pwr-start" placeholder="0 9 * * 1-5" value="${esc((s && s.start) || '')}" style="width:9rem"></label>
      <label>Stop (cron) <input id="pwr-stop" placeholder="0 18 * * 1-5" value="${esc((s && s.stop) || '')}" style="width:9rem"></label>
      <button class="btn primary" id="pwr-save">Save</button>
      ${has ? '<button class="btn sm danger" id="pwr-clear">Clear</button>' : ''}
    </div>
    <p class="muted">5-field cron in the cluster's timezone. e.g. start <code>0 9 * * 1-5</code>, stop <code>0 18 * * 1-5</code> = weekdays 9–6. Leave a field blank to skip that boundary.</p>`;
  $('#pwr-save').onclick = async () => {
    try {
      await api(`/api/vms/${vm.namespace}/${vm.name}/powerschedule`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ start: $('#pwr-start').value.trim(), stop: $('#pwr-stop').value.trim() }),
      });
      toast('Schedule saved');
    } catch (e) { toast(e.message); }
    renderPowerSchedule(vm);
  };
  const clr = $('#pwr-clear');
  if (clr) clr.onclick = async () => {
    try { await api(`/api/vms/${vm.namespace}/${vm.name}/powerschedule`, { method: 'DELETE' }); toast('Schedule cleared'); }
    catch (e) { toast(e.message); }
    renderPowerSchedule(vm);
  };
}

async function renderEvents(vm, body) {
  body.innerHTML = `<p class="muted">loading…</p>`;
  let evs;
  try { evs = await api(`/api/vms/${vm.namespace}/${vm.name}/events`); }
  catch (e) { body.innerHTML = `<p class="console-msg">${esc(e.message)}</p>`; return; }
  if (!evs.length) { body.innerHTML = `<p class="muted">No recent events.</p>`; return; }
  body.innerHTML = `<table><thead><tr>
      <th>Time</th><th>Type</th><th>Reason</th><th>Object</th><th>Message</th>
    </tr></thead><tbody>
    ${evs.map((e) => `<tr class="${e.type === 'Warning' ? 'ev-warn' : ''}">
      <td>${esc(e.time || '')}</td><td>${esc(e.type)}</td><td>${esc(e.reason)}</td>
      <td>${esc(e.object)}</td><td>${esc(e.message)}</td></tr>`).join('')}
    </tbody></table>`;
}

async function renderHardware(vm, body) {
  body.innerHTML = `<pre class="yaml">loading…</pre>`;
  let j;
  try { j = await api(`/api/vms/${vm.namespace}/${vm.name}`); }
  catch (e) { body.innerHTML = `<p class="console-msg">${esc(e.message)}</p>`; return; }

  const spec = j.spec?.template?.spec ?? {};
  const cpu = spec.domain?.cpu ?? {};
  const vcpus = (cpu.sockets || 1) * (cpu.cores || 1) * (cpu.threads || 1);
  const mem = spec.domain?.memory?.guest ?? '';
  const disks = spec.domain?.devices?.disks ?? [];
  const volumes = Object.fromEntries((spec.volumes ?? []).map((v) => [v.name, v]));
  const pvcOf = (name) => volumes[name]?.persistentVolumeClaim?.claimName;
  const volDesc = (name) => {
    const v = volumes[name] || {};
    if (v.persistentVolumeClaim) return `PVC ${v.persistentVolumeClaim.claimName}`;
    if (v.containerDisk) return `containerDisk ${v.containerDisk.image}`;
    if (v.cloudInitNoCloud) return 'cloud-init';
    return Object.keys(v).filter((k) => k !== 'name').join(',') || '?';
  };
  const liveNote = vm.liveMigratable
    ? 'applies live (hotplug)'
    : 'VM will restart to apply';

  body.innerHTML = `
    <h2 class="section">${icon('cpu')} Processor &amp; memory</h2>
    <div class="hw-edit">
      <label>vCPUs <input id="hw-cpu" type="number" min="1" max="64" value="${vcpus}"></label>
      <label>Memory <input id="hw-mem" value="${esc(mem)}"></label>
      <button class="btn primary" id="hw-apply">Apply</button>
      <span class="muted">${esc(liveNote)}</span>
    </div>

    <h2 class="section">${icon('disk')} Storage
      <button class="btn" id="hw-adddisk">${icon('plus')} Add disk</button>
    </h2>
    <table><thead><tr><th>Disk</th><th>Type</th><th>Backing</th><th></th></tr></thead><tbody>
      ${disks.map((d) => {
        const pvc = pvcOf(d.name);
        return `<tr>
          <td>${esc(d.name)}</td>
          <td>${esc(d.cdrom ? 'cdrom' : 'disk')} (${esc(d.disk?.bus || d.cdrom?.bus || '—')})</td>
          <td>${esc(volDesc(d.name))}</td>
          <td>${pvc ? `<button class="btn sm" data-expand="${esc(pvc)}" ${caps.canExpand ? '' : 'disabled'}
              title="${caps.canExpand ? 'Grow this disk' : 'Storage class does not support expansion'}">${icon('expand')} Expand</button>` : ''}
            ${pvc && d.name.includes('-hp-') ? `<button class="btn sm danger" data-rmvol="${esc(d.name)}">Detach</button>` : ''}
          </td>
        </tr>`;
      }).join('')}
    </tbody></table>

    <h2 class="section">${icon('server')} Network
      ${availableNADs.length ? `<button class="btn" id="hw-addnic">${icon('plus')} Add NIC</button>` : ''}
    </h2>
    ${networkTable(spec, vm)}

    <h2 class="section">${icon('cpu')} GPU / PCI passthrough</h2>
    <div id="hw-gpus"><p class="muted">loading…</p></div>

    <h2 class="section">${icon('info')} Firmware</h2>
    <dl class="props">
      <dt>Boot</dt><dd>${spec.domain?.firmware?.kernelBoot ? 'kernel boot (bootc)' : 'BIOS'}</dd>
      <dt>Node selector</dt><dd><code>${esc(JSON.stringify(spec.nodeSelector ?? {}))}</code></dd>
    </dl>`;

  renderGPUs(vm);

  $('#hw-apply').onclick = async () => {
    const newCpu = parseInt($('#hw-cpu').value, 10);
    const newMem = $('#hw-mem').value.trim();
    const payload = {};
    if (newCpu && newCpu !== vcpus) payload.cpu = newCpu;
    if (newMem && newMem !== mem) payload.mem = newMem;
    if (!payload.cpu && !payload.mem) { toast('No changes'); return; }
    $('#hw-apply').disabled = true;
    try { await post(vm, '/scale', payload); toast('Applied'); }
    catch (e) { toast(e.message); }
    setTimeout(refresh, 800);
  };
  $('#hw-adddisk').onclick = async () => {
    const size = prompt('New disk size (e.g. 10Gi):', '10Gi');
    if (!size) return;
    try { await post(vm, '/volumes', { size: size.trim() }); toast('Disk added'); }
    catch (e) { toast(e.message); }
    setTimeout(() => renderHardware(vm, body), 800);
  };
  body.querySelectorAll('[data-expand]').forEach((b) => {
    b.onclick = async () => {
      const size = prompt(`Grow ${b.dataset.expand} to:`, '');
      if (!size) return;
      try { await post(vm, '/expand', { pvc: b.dataset.expand, size: size.trim() }); toast('Expanding…'); }
      catch (e) { toast(e.message); }
    };
  });
  body.querySelectorAll('[data-rmvol]').forEach((b) => {
    b.onclick = async () => {
      if (!confirm(`Detach ${b.dataset.rmvol}?`)) return;
      try {
        await api(`/api/vms/${vm.namespace}/${vm.name}/volumes/${b.dataset.rmvol}`, { method: 'DELETE' });
        toast('Detached');
      } catch (e) { toast(e.message); }
      setTimeout(() => renderHardware(vm, body), 800);
    };
  });
  const addNic = $('#hw-addnic');
  if (addNic) addNic.onclick = async () => {
    const nad = prompt(`Attach a NIC on which network?\nAvailable: ${availableNADs.join(', ')}`, availableNADs[0] || '');
    if (!nad) return;
    try { await post(vm, '/nics', { nad: nad.trim() }); toast('NIC added'); }
    catch (e) { toast(e.message); }
    setTimeout(() => renderHardware(vm, body), 800);
  };
}

function networkTable(spec, vm) {
  const ifaces = spec.domain?.devices?.interfaces ?? [];
  const nets = Object.fromEntries((spec.networks ?? []).map((n) => [n.name, n]));
  const binding = (i) => ['masquerade', 'bridge', 'sriov', 'macvtap', 'slirp'].find((b) => i[b]) || '?';
  const netOf = (name) => {
    const n = nets[name] || {};
    if (n.pod) return 'pod network';
    if (n.multus) return `multus: ${n.multus.networkName}`;
    return Object.keys(n).filter((k) => k !== 'name')[0] || '—';
  };
  if (!ifaces.length) return `<p class="muted">No interfaces.</p>`;
  return `<table><thead><tr><th>Name</th><th>Binding</th><th>Network</th><th>IP</th></tr></thead><tbody>
    ${ifaces.map((i) => `<tr>
      <td>${esc(i.name)}</td><td>${esc(binding(i))}</td><td>${esc(netOf(i.name))}</td>
      <td>${esc(i.name === 'default' ? (vm.ip || '—') : '—')}</td></tr>`).join('')}
    </tbody></table>
    <p class="muted" style="font-size:.78rem;margin-top:6px">Secondary NIC hotplug needs Multus (not installed on this cluster).</p>`;
}

// GPU/PCI passthrough section of the Hardware tab (gpu plugin): list attached
// devices with detach, and attach from the cluster's permitted devices.
async function renderGPUs(vm) {
  const box = $('#hw-gpus');
  if (!box) return;
  let permitted = [], attached = [];
  try { permitted = await api('/api/gpus'); } catch { /* none */ }
  try { attached = await api(`/api/vms/${vm.namespace}/${vm.name}/gpus`); } catch { /* none */ }

  const rows = attached.length
    ? `<table><thead><tr><th>Name</th><th>Device</th><th></th></tr></thead><tbody>
        ${attached.map((g) => `<tr><td>${esc(g.name)}</td><td><code>${esc(g.deviceName)}</code></td>
          <td><button class="btn sm danger" data-rmgpu="${esc(g.name)}">Detach</button></td></tr>`).join('')}
       </tbody></table>`
    : `<p class="muted">No GPUs attached.</p>`;

  const picker = permitted.length
    ? `<div class="hw-edit" style="margin-top:8px">
        <label>Device <select id="gpu-dev">${permitted.map((d) =>
          `<option value="${esc(d.resourceName)}">${esc(d.resourceName)} (${esc(d.type)})</option>`).join('')}</select></label>
        <button class="btn" id="gpu-attach">${icon('plus')} Attach</button>
        <span class="muted">applies on next boot</span>
      </div>`
    : `<p class="muted" style="font-size:.78rem">No passthrough devices permitted yet. An admin enables them once with
       <code>corral gpu enable --vendor &lt;vid:did&gt; --resource &lt;vendor/name&gt;</code>.</p>`;
  box.innerHTML = rows + picker;

  const attach = $('#gpu-attach');
  if (attach) attach.onclick = async () => {
    try {
      await post(vm, '/gpus', { device: $('#gpu-dev').value });
      toast('GPU attached (next boot)');
    } catch (e) { toast(e.message); }
    renderGPUs(vm);
  };
  box.querySelectorAll('[data-rmgpu]').forEach((b) => {
    b.onclick = async () => {
      try {
        await api(`/api/vms/${vm.namespace}/${vm.name}/gpus/${b.dataset.rmgpu}`, { method: 'DELETE' });
        toast('GPU detached');
      } catch (e) { toast(e.message); }
      renderGPUs(vm);
    };
  });
}

async function renderSnapshots(vm, body) {
  if (!caps.canSnapshot) {
    body.innerHTML = `<p class="console-msg">Snapshots need a snapshot-capable StorageClass
      (no VolumeSnapshotClass found in this cluster).</p>`;
    return;
  }
  // Scheduling is built into the web server (pkg/cronops); it needs the same
  // snapshot-capable storage as one-off snapshots, which canSnapshot gates.
  const hasSnapsched = caps.canSnapshot;
  body.innerHTML = `
    <div class="toolbar" style="margin-bottom:12px">
      <button class="btn primary" id="snap-new">${icon('camera')} Take snapshot</button>
    </div>
    ${hasSnapsched ? `<div id="snapsched-box" class="panel-section"><p class="muted">loading schedule…</p></div>` : ''}
    <table><thead><tr><th>Name</th><th>Ready</th><th>Created</th><th></th></tr></thead>
    <tbody id="snap-rows"><tr><td colspan="4" class="muted">loading…</td></tr></tbody></table>`;

  $('#snap-new').onclick = async () => {
    try { await post(vm, '/snapshots', {}); toast('Snapshot started'); }
    catch (e) { toast(e.message); }
    setTimeout(() => renderSnapshots(vm, body), 800);
  };

  if (hasSnapsched) renderSnapSchedule(vm);

  let snaps = [];
  try { snaps = await api(`/api/vms/${vm.namespace}/${vm.name}/snapshots`); }
  catch (e) { $('#snap-rows').innerHTML = `<tr><td colspan="4">${esc(e.message)}</td></tr>`; return; }

  $('#snap-rows').innerHTML = snaps.length
    ? snaps.map((s) => `<tr>
        <td>${esc(s.name)}</td>
        <td>${s.ready ? '✓' : '…'}</td>
        <td>${esc(s.created || '')}</td>
        <td>
          <button class="btn sm" data-restore="${esc(s.name)}" title="VM must be stopped">Restore</button>
          <button class="btn sm danger" data-delsnap="${esc(s.name)}">Delete</button>
        </td></tr>`).join('')
    : `<tr><td colspan="4" class="muted">No snapshots yet.</td></tr>`;

  body.querySelectorAll('[data-restore]').forEach((b) => {
    b.onclick = async () => {
      if (!confirm(`Restore ${vm.name} from ${b.dataset.restore}? The VM must be stopped first.`)) return;
      try { await post(vm, `/snapshots/${b.dataset.restore}/restore`, {}); toast('Restoring…'); }
      catch (e) { toast(e.message); }
    };
  });
  body.querySelectorAll('[data-delsnap]').forEach((b) => {
    b.onclick = async () => {
      try {
        await api(`/api/vms/${vm.namespace}/${vm.name}/snapshots/${b.dataset.delsnap}`, { method: 'DELETE' });
        toast('Deleted');
      } catch (e) { toast(e.message); }
      setTimeout(() => renderSnapshots(vm, body), 500);
    };
  });
}

// Scheduled snapshots (snapsched plugin) — list/add/remove the CronJob.
async function renderSnapSchedule(vm) {
  const box = $('#snapsched-box');
  if (!box) return;
  let sched = {};
  try { sched = await api(`/api/vms/${vm.namespace}/${vm.name}/snapschedule`); }
  catch { /* show the add form */ }

  if (sched && sched.schedule) {
    box.innerHTML = `
      <h2 class="section">${icon('camera')} Snapshot schedule</h2>
      <dl class="props">
        <dt>Cron</dt><dd><code>${esc(sched.schedule)}</code></dd>
        <dt>Last run</dt><dd>${esc(sched.lastRun || '— (not yet)')}</dd>
      </dl>
      <button class="btn sm danger" id="snapsched-rm">Remove schedule</button>
      <span class="muted">Existing snapshots are kept.</span>`;
    $('#snapsched-rm').onclick = async () => {
      try { await api(`/api/vms/${vm.namespace}/${vm.name}/snapschedule`, { method: 'DELETE' }); toast('Schedule removed'); }
      catch (e) { toast(e.message); }
      renderSnapSchedule(vm);
    };
    return;
  }

  box.innerHTML = `
    <h2 class="section">${icon('camera')} Snapshot schedule</h2>
    <div class="hw-edit">
      <label>Every
        <select id="snapsched-every">
          <option value="6h">6 hours</option>
          <option value="30m">30 minutes</option>
          <option value="1h">1 hour</option>
          <option value="12h">12 hours</option>
          <option value="24h">daily</option>
        </select>
      </label>
      <label>Keep <input id="snapsched-keep" type="number" min="1" value="12" style="width:5rem"></label>
      <button class="btn primary" id="snapsched-add">Schedule</button>
    </div>
    <p class="muted">A CronJob snapshots the VM each tick and prunes beyond “keep”.</p>`;
  $('#snapsched-add').onclick = async () => {
    const every = $('#snapsched-every').value;
    const keep = parseInt($('#snapsched-keep').value, 10) || 12;
    try {
      await api(`/api/vms/${vm.namespace}/${vm.name}/snapschedule`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ every, keep }),
      });
      toast('Schedule created');
    } catch (e) { toast(e.message); }
    renderSnapSchedule(vm);
  };
}

// ── Consoles ──────────────────────────────────────────────────────

const wsURL = (kind, vm) =>
  `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/${kind}/${vm.namespace}/${vm.name}`;

// toggleFullscreen puts el into (or out of) browser fullscreen.
function toggleFullscreen(el) {
  if (document.fullscreenElement) document.exitFullscreen();
  else el.requestFullscreen?.();
}
// Element fullscreen fires no window resize, but noVNC's scaling and xterm's
// fit addon both key off it — give them one.
document.addEventListener('fullscreenchange', () => window.dispatchEvent(new Event('resize')));

async function connectVNC(vm, body) {
  if (!vm.running) {
    body.innerHTML = `<p class="console-msg">VM is not running — start it to open the console.</p>`;
    return;
  }
  body.innerHTML = `
    <div class="toolbar console-bar">
      <button class="btn sm" id="vnc-fullscreen" title="Fullscreen (Esc to leave)">${icon('expand')} Fullscreen</button>
      <label class="console-opt"><input type="checkbox" id="vnc-scale" checked> Scale to fit (local)</label>
      <label class="console-opt" title="Ask the guest to change its resolution to match the window (needs guest support)">
        <input type="checkbox" id="vnc-resize"> Remote resize</label>
    </div>
    <div id="vnc-screen"><p class="console-msg">Connecting…</p></div>`;
  try {
    const { default: RFB } = await import(
      'https://cdn.jsdelivr.net/npm/@novnc/novnc@1.4.0/core/rfb.js');
    const screen = $('#vnc-screen');
    screen.replaceChildren();
    rfb = new RFB(screen, wsURL('vnc', vm));
    rfb.scaleViewport = true;  // noVNC local scaling — fits any window size
    rfb.resizeSession = false; // remote resize is opt-in (guest must support it)

    $('#vnc-fullscreen').onclick = () => toggleFullscreen(screen);
    $('#vnc-scale').onchange = (e) => { if (rfb) rfb.scaleViewport = e.target.checked; };
    $('#vnc-resize').onchange = (e) => {
      if (!rfb) return;
      rfb.resizeSession = e.target.checked;
      if (e.target.checked) {
        // The two modes fight each other; remote resize wins when enabled.
        $('#vnc-scale').checked = false;
        rfb.scaleViewport = false;
      }
    };
    rfb.addEventListener('disconnect', () => {
      if (tab === 'console') {
        screen.innerHTML = `<p class="console-msg">Disconnected. Switch tabs to reconnect.</p>`;
      }
    });
  } catch (e) {
    body.innerHTML = `<p class="console-msg">VNC failed: ${esc(e.message)}</p>`;
  }
}

function connectTTY(vm, body) {
  if (!vm.running) {
    body.innerHTML = `<p class="console-msg">VM is not running — start it to open the serial console.</p>`;
    return;
  }
  body.innerHTML = `
    <div class="toolbar console-bar">
      <button class="btn sm" id="tty-fullscreen" title="Fullscreen (Esc to leave)">${icon('expand')} Fullscreen</button>
    </div>
    <div id="tty-screen"></div>
    <p class="hint" style="color:var(--muted);font-size:.78rem">Serial console via virtctl — hit Enter if blank.</p>`;
  $('#tty-fullscreen').onclick = () => toggleFullscreen($('#tty-screen'));

  term = new Terminal({ fontSize: 14, theme: { background: '#000000' }, cursorBlink: true });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($('#tty-screen'));
  fit.fit();
  const onResize = () => fit.fit();
  window.addEventListener('resize', onResize);

  ttyWS = new WebSocket(wsURL('tty', vm));
  ttyWS.binaryType = 'arraybuffer';
  const enc = new TextEncoder();
  ttyWS.onmessage = (e) => term.write(new Uint8Array(e.data));
  ttyWS.onclose = () => term?.write('\r\n\x1b[33m[disconnected]\x1b[0m\r\n');
  term.onData((d) => {
    if (ttyWS?.readyState === WebSocket.OPEN) ttyWS.send(enc.encode(d));
  });
  term.focus();
}

function disconnectConsoles() {
  try { rfb?.disconnect(); } catch { /* already gone */ }
  rfb = null;
  try { ttyWS?.close(); } catch { /* already gone */ }
  ttyWS = null;
  try { term?.dispose(); } catch { /* already gone */ }
  term = null;
}

// ── Create dialog ─────────────────────────────────────────────────

$('#btn-create').onclick = () => {
  loadCatalog();
  // Reset to the catalog (the simple path) and update field visibility
  const srcType = document.querySelector('[name=sourceType]');
  if (srcType) srcType.value = 'catalog';
  const nsInput = document.querySelector('#create-form [name=namespace]');
  if (nsInput && !nsInput.value) nsInput.value = caps.defaultNamespace || '';
  updateSourceFields();
  wizardReset();
  $('#create-dialog').showModal();
};
$('#btn-cancel').onclick = () => $('#create-dialog').close();

// ── Simple wizard: OS cards → name + size → create ────────────────

const WIZ_SIZES = {
  s: { cpu: 1, mem: '2G', disk: '15G' },
  m: { cpu: 2, mem: '4G', disk: '20G' },
  l: { cpu: 4, mem: '8G', disk: '40G' },
};
let wizImages = [];   // catalog entries + bootc entries (kind: 'bootc')
let wizSelected = null;
let wizFilter = 'all';

// A distro logo from simple-icons, falling back to a letter badge.
// (The badge swap is wired up post-render — see wizBindLogoFallbacks.)
function wizLogo(entry) {
  const letter = esc((entry.name || '?')[0].toUpperCase());
  if (!entry.logo) return `<span class="wiz-badge">${letter}</span>`;
  return `<img class="wiz-logo" data-letter="${letter}" src="https://cdn.simpleicons.org/${esc(entry.logo)}" alt="">`;
}

function wizBindLogoFallbacks(root) {
  root.querySelectorAll('img.wiz-logo').forEach((img) => {
    img.onerror = () => {
      const span = document.createElement('span');
      span.className = 'wiz-badge';
      span.textContent = img.dataset.letter || '?';
      img.replaceWith(span);
    };
  });
}

async function wizardLoad() {
  let imgs = [];
  try { imgs = await api('/api/images'); } catch { /* cards stay empty */ }
  wizImages = imgs.map((i) => ({ ...i, kind: i.url ? 'import' : i.iso ? 'iso' : 'server' }));
  if (caps.bootc) {
    $('#wiz-filters [data-filter="bootc"]').hidden = false;
    let bimgs = [];
    try { bimgs = await api('/api/images?type=bootc'); } catch { /* none */ }
    wizImages = wizImages.concat(bimgs.map((b) => ({ ...b, variant: 'bootc', kind: 'bootc' })));
  }
  wizardRenderCards();
}

function wizardRenderCards() {
  const grid = $('#wiz-cards');
  const shown = wizImages.filter((i) =>
    wizFilter === 'all' || i.variant === wizFilter);
  grid.innerHTML = shown.map((i, n) => `
    <div class="wiz-card" data-wiz="${n}">
      ${wizLogo(i)}
      <strong>${esc(i.name)}</strong>
      <span class="muted">${esc(i.description)}</span>
      ${i.variant === 'bootc' ? '<span class="pill mid">bootc</span>'
        : i.iso ? '<span class="pill">installer</span>' : ''}
    </div>`).join('') || '<p class="muted">No images for this filter.</p>';
  wizBindLogoFallbacks(grid);
  grid.querySelectorAll('[data-wiz]').forEach((card) => {
    card.onclick = () => {
      wizSelected = shown[parseInt(card.dataset.wiz, 10)];
      $('#wiz-selected').innerHTML = `${wizLogo(wizSelected)} <strong>${esc(wizSelected.name)}</strong>
        <span class="muted">${esc(wizSelected.description)}</span>`;
      wizBindLogoFallbacks($('#wiz-selected'));
      $('#wiz-step-1').hidden = true;
      $('#wiz-step-2').hidden = false;
      $('#wiz-back').hidden = false;
      $('#wiz-create').hidden = false;
      $('#wiz-name').focus();
    };
  });
}

function wizardReset() {
  wizSelected = null;
  $('#wizard-simple').hidden = false;
  $('#create-form').hidden = true;
  $('#wiz-step-1').hidden = false;
  $('#wiz-step-2').hidden = true;
  $('#wiz-back').hidden = true;
  $('#wiz-create').hidden = true;
  wizardLoad();
}

$('#wiz-filters').querySelectorAll('.wiz-chip').forEach((chip) => {
  chip.onclick = () => {
    wizFilter = chip.dataset.filter;
    $('#wiz-filters').querySelectorAll('.wiz-chip').forEach((c) => c.classList.toggle('active', c === chip));
    wizardRenderCards();
  };
});
$('#wiz-sizes').querySelectorAll('.wiz-size').forEach((sz) => {
  sz.onclick = () => $('#wiz-sizes').querySelectorAll('.wiz-size')
    .forEach((c) => c.classList.toggle('selected', c === sz));
});
$('#wiz-back').onclick = () => {
  $('#wiz-step-1').hidden = false;
  $('#wiz-step-2').hidden = true;
  $('#wiz-back').hidden = true;
  $('#wiz-create').hidden = true;
};
$('#wiz-cancel').onclick = () => $('#create-dialog').close();
$('#wiz-advanced').onclick = () => {
  $('#wizard-simple').hidden = true;
  $('#create-form').hidden = false;
};
$('#btn-simple').onclick = () => {
  $('#create-form').hidden = true;
  $('#wizard-simple').hidden = false;
};

$('#wiz-create').onclick = async () => {
  const name = $('#wiz-name').value.trim();
  if (!name || !wizSelected) { toast('Pick a name'); return; }
  const size = WIZ_SIZES[$('#wiz-sizes .selected')?.dataset.size || 'm'];
  const body = {
    name,
    namespace: caps.defaultNamespace || '',
    cpu: size.cpu, mem: size.mem, disk: size.disk,
    sshKey: $('#wiz-sshkey').value.trim(),
  };
  if (wizSelected.kind === 'bootc') body.bootc = wizSelected.image;
  else body.image = wizSelected.name;
  $('#wiz-create').disabled = true;
  try {
    const res = await api('/api/vms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    $('#create-dialog').close();
    $('#wiz-name').value = '';
    if (res.task) watchBuild(res.task, name);
    refresh();
  } catch (err) {
    toast(`Create failed: ${err.message}`);
  }
  $('#wiz-create').disabled = false;
};
$('#btn-build-close').onclick = () => $('#build-dialog').close();

const SOURCE_HINTS = {
  containerDisk: 'quay.io/containerdisks/fedora:42',
  import: 'https://cloud-images.example/jammy.qcow2',
  iso: 'https://example.com/installer.iso',
  bootc: 'quay.io/centos-bootc/centos-bootc:stream9',
  windows: 'https://example.com/Win11_x64.iso',
  pvc: 'existing-pvc-name',
};

async function loadCatalog() {
  let imgs;
  try { imgs = await api('/api/images'); } catch { imgs = []; }
  const sel = document.querySelector('[name=catalogImage]');
  // Catalog entries boot three ways; flag the slower ones so there are no surprises.
  const kindTag = (i) => (i.url ? ' [imports via CDI]' : i.iso ? ' [installer ISO]' : '');
  if (sel) sel.innerHTML = imgs.map((i) => `<option value="${esc(i.name)}">${esc(i.name)} — ${esc(i.description)}${kindTag(i)}</option>`).join('');

  // Bootc image suggestions (datalist on the source field) when the plugin is on.
  if (caps.bootc) {
    let bimgs;
    try { bimgs = await api('/api/images?type=bootc'); } catch { bimgs = []; }
    const dl = $('#bootc-catalog');
    if (dl) dl.innerHTML = bimgs.map((b) => `<option value="${esc(b.image)}">${esc(b.name)} — ${esc(b.description)}</option>`).join('');
  }
}

const CREATE_HINTS = {
  iso: 'The installer ISO boots with a blank disk — finish the install in the Console tab.',
  bootc: 'The SSH key is baked into the built disk. Bootc builds take a few minutes — a live build log opens after submit.',
  windows: 'UEFI + TPM + Hyper-V tuned, with the virtio-win driver CD-ROM attached. After boot, in Setup click “Load driver” → the virtio CD-ROM. Provide a Windows installer ISO URL.',
  pvc: 'Boots an existing disk as-is; cloud-init does not run again.',
};
const DEFAULT_HINT = 'Cloud-init VMs get your SSH key and Tailscale auth key (if configured) automatically.';

function updateSourceFields() {
  const type = document.querySelector('[name=sourceType]').value;
  $('#catalog-field').hidden = type !== 'catalog';
  $('#source-field').hidden = type === 'catalog';
  $('#create-hint').textContent = CREATE_HINTS[type] || DEFAULT_HINT;
  const rdp = $('#rdp-field');
  if (rdp) rdp.hidden = type !== 'windows'; // RDP toggle is Windows-only here
  const src = document.querySelector('[name=source]');
  if (src) {
    src.placeholder = SOURCE_HINTS[type] || '';
    // Bootc gets catalog suggestions; other types are free-form.
    if (type === 'bootc') src.setAttribute('list', 'bootc-catalog');
    else src.removeAttribute('list');
  }
}

document.querySelector('[name=sourceType]').onchange = updateSourceFields;

$('#create-form').onsubmit = async (e) => {
  e.preventDefault();
  const f = new FormData(e.target);
  const body = {
    name: f.get('name'),
    namespace: f.get('namespace'),
    node: f.get('node'),
    cpu: parseInt(f.get('cpu'), 10) || 2,
    mem: f.get('mem'),
    disk: f.get('disk'),
    cloudInit: f.get('cloudInit') || '',
    instancetype: f.get('instancetype') || '',
    preference: f.get('preference') || '',
  };
  const src = f.get('source');
  const type = f.get('sourceType');
  body.sshKey = f.get('sshKey') || ''; // key or GitHub username, any source type
  if (type === 'catalog') body.image = f.get('catalogImage');
  else if (type === 'containerDisk') body.containerDisk = src;
  else if (type === 'import') body.import = src;
  else if (type === 'iso') body.iso = src;
  else if (type === 'bootc') body.bootc = src;
  else if (type === 'windows') { body.windows = true; body.iso = src; body.rdp = !!f.get('rdp'); }
  else body.pvc = src;

  try {
    const res = await api('/api/vms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    $('#create-dialog').close();
    e.target.reset();
    if (res.task) watchBuild(res.task, body.name);
    refresh();
  } catch (err) {
    toast(`Create failed: ${err.message}`);
  }
};

// Poll a bootc build task, streaming its log into the build dialog.
function watchBuild(taskID, vmName) {
  const dlg = $('#build-dialog');
  const log = $('#build-log');
  const title = $('#build-title');
  title.textContent = `Building bootc disk for ${vmName}…`;
  log.textContent = '';
  dlg.showModal();

  const timer = setInterval(async () => {
    let t;
    try { t = await api(`/api/tasks/${taskID}`); } catch { return; }
    log.textContent = t.log;
    log.scrollTop = log.scrollHeight;
    if (t.status === 'done') {
      clearInterval(timer);
      title.textContent = `✅ ${vmName} ready`;
      refresh();
    } else if (t.status === 'error') {
      clearInterval(timer);
      title.textContent = `❌ Build failed`;
      log.textContent += `\n${t.error}`;
    }
  }, 2000);
}

// ── Task panel (Proxmox-style activity log) ───────────────────────

let taskLog = [];
let lastTaskLogFp = '';

async function refreshTaskLog() {
  try { taskLog = await api('/api/tasklog'); } catch { return; }
  const fp = JSON.stringify(taskLog);
  if (fp === lastTaskLogFp) return; // unchanged — don't reset panel scroll
  lastTaskLogFp = fp;
  const rows = $('#task-rows');
  const summary = $('#task-panel-summary');
  if (!rows) return;
  const running = taskLog.filter((t) => t.status === 'running').length;
  const errors = taskLog.filter((t) => t.status === 'error').length;
  summary.textContent = taskLog.length
    ? `${running ? `${running} running · ` : ''}${errors ? `${errors} failed · ` : ''}${taskLog.length} total`
    : '';
  const statusPill = (t) => t.status === 'running' ? '<span class="pill mid">running</span>'
    : t.status === 'error' ? `<span class="pill off" title="${esc(t.error || '')}">error</span>`
    : '<span class="pill on">OK</span>';
  rows.innerHTML = taskLog.length
    ? taskLog.slice(0, 50).map((t) => `<tr${t.status === 'error' ? ` title="${esc(t.error || '')}"` : ''}>
        <td>${esc(new Date(t.started).toLocaleTimeString())}</td>
        <td>${esc(t.action)}</td>
        <td>${esc(t.target)}</td>
        <td>${esc(t.duration || '…')}</td>
        <td>${statusPill(t)}</td>
      </tr>`).join('')
    : `<tr><td colspan="5" class="muted">No tasks yet.</td></tr>`;
}

$('#task-panel-head').onclick = () => {
  const panel = $('#task-panel');
  panel.classList.toggle('collapsed');
  $('#task-panel-chevron').textContent = panel.classList.contains('collapsed') ? '▴' : '▾';
};

// ── Mobile drawer ─────────────────────────────────────────────────

$('#btn-menu').onclick = () => $('#tree').classList.toggle('open');
function closeDrawer() { $('#tree').classList.remove('open'); }

// ── Boot ──────────────────────────────────────────────────────────

$('#btn-menu').innerHTML = icon('menu');
$('#btn-create').innerHTML = `${icon('plus')} Create VM`;

loadWhoami();
loadCaps();
loadInstanceTypes();
refresh();
refreshTaskLog();
setInterval(refresh, 5000);
setInterval(refreshTaskLog, 5000);
