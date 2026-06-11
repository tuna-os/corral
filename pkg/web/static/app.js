// Corral web UI — Proxmox-style dashboard for KubeVirt.
// Vanilla JS; noVNC + xterm.js loaded from CDN for the consoles.

import { icon } from './icons.js';

const $ = (sel) => document.querySelector(sel);

let vms = [];
let nodes = [];
let caps = { storageClass: '', canExpand: false, canSnapshot: false };
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

async function refresh() {
  try {
    [vms, nodes] = await Promise.all([api('/api/vms'), api('/api/nodes')]);
  } catch (e) {
    toast(`Refresh failed: ${e.message}`);
    return;
  }
  renderTree();
  // Don't clobber live consoles on poll.
  if (tab !== 'console' && tab !== 'terminal') renderContent();
}

async function loadCaps() {
  try { caps = await api('/api/capabilities'); } catch { /* keep defaults */ }
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
    lvl, icon: icon('cube'), label: vm.name, sub: vm.namespace,
    dot: vm.ready ? 'on' : (vm.running ? 'mid' : 'off'),
    sel: selected.type === 'vm' && selected.key === vmKey(vm),
    onclick: () => select({ type: 'vm', key: vmKey(vm) }),
  });
}

function select(sel) {
  disconnectConsoles();
  selected = sel;
  tab = 'summary';
  renderTree();
  renderContent();
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
  return renderDatacenter(main);
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
    ${vmTable(vms)}`;
  bindVMTable(main);
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
        <button class="btn" data-act="export" ${vm.running ? 'disabled' : ''}
          title="${vm.running ? 'Stop the VM to export its disk' : 'Download a disk backup'}">${icon('download')} Export</button>
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
    t.onclick = () => { disconnectConsoles(); tab = t.dataset.tab; renderVM(main, vm); };
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
      </dl>
      <div id="guest-info"></div>`;
      if (vm.running) loadMetrics(vm);
      if (vm.agentConnected) loadGuestInfo(vm);
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
  if (act === 'export') {
    toast('Preparing backup… the download will start when ready.');
    window.location.href = `/api/vms/${vm.namespace}/${vm.name}/export`;
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

async function loadMetrics(vm) {
  try {
    const m = await api(`/api/vms/${vm.namespace}/${vm.name}/metrics`);
    const el = $('#vm-usage');
    if (el) el.textContent = (m.cpu || m.mem) ? `${m.cpu || '?'} CPU · ${m.mem || '?'} mem` : 'no metrics yet';
  } catch { /* metrics-server may be absent */ }
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

    <h2 class="section">${icon('info')} Firmware</h2>
    <dl class="props">
      <dt>Boot</dt><dd>${spec.domain?.firmware?.kernelBoot ? 'kernel boot (bootc)' : 'BIOS'}</dd>
      <dt>Node selector</dt><dd><code>${esc(JSON.stringify(spec.nodeSelector ?? {}))}</code></dd>
    </dl>`;

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
}

async function renderSnapshots(vm, body) {
  if (!caps.canSnapshot) {
    body.innerHTML = `<p class="console-msg">Snapshots need a snapshot-capable StorageClass
      (no VolumeSnapshotClass found in this cluster).</p>`;
    return;
  }
  body.innerHTML = `
    <div class="toolbar" style="margin-bottom:12px">
      <button class="btn primary" id="snap-new">${icon('camera')} Take snapshot</button>
    </div>
    <table><thead><tr><th>Name</th><th>Ready</th><th>Created</th><th></th></tr></thead>
    <tbody id="snap-rows"><tr><td colspan="4" class="muted">loading…</td></tr></tbody></table>`;

  $('#snap-new').onclick = async () => {
    try { await post(vm, '/snapshots', {}); toast('Snapshot started'); }
    catch (e) { toast(e.message); }
    setTimeout(() => renderSnapshots(vm, body), 800);
  };

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

// ── Consoles ──────────────────────────────────────────────────────

const wsURL = (kind, vm) =>
  `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/${kind}/${vm.namespace}/${vm.name}`;

async function connectVNC(vm, body) {
  if (!vm.running) {
    body.innerHTML = `<p class="console-msg">VM is not running — start it to open the console.</p>`;
    return;
  }
  body.innerHTML = `<div id="vnc-screen"><p class="console-msg">Connecting…</p></div>`;
  try {
    const { default: RFB } = await import(
      'https://cdn.jsdelivr.net/npm/@novnc/novnc@1.4.0/core/rfb.js');
    const screen = $('#vnc-screen');
    screen.replaceChildren();
    rfb = new RFB(screen, wsURL('vnc', vm));
    rfb.scaleViewport = true;
    rfb.resizeSession = false;
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
  body.innerHTML = `<div id="tty-screen"></div>
    <p class="hint" style="color:var(--muted);font-size:.78rem">Serial console via virtctl — hit Enter if blank.</p>`;

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

$('#btn-create').onclick = () => $('#create-dialog').showModal();
$('#btn-cancel').onclick = () => $('#create-dialog').close();
$('#btn-build-close').onclick = () => $('#build-dialog').close();

const SOURCE_HINTS = {
  containerDisk: 'quay.io/containerdisks/fedora:42',
  iso: 'https://example.com/installer.iso',
  bootc: 'quay.io/centos-bootc/centos-bootc:stream9',
  pvc: 'existing-pvc-name',
};

document.querySelector('[name=sourceType]').onchange = (e) => {
  $('#sshkey-field').hidden = e.target.value !== 'bootc';
  document.querySelector('[name=source]').placeholder = SOURCE_HINTS[e.target.value];
};

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
  };
  const src = f.get('source');
  const type = f.get('sourceType');
  if (type === 'containerDisk') body.containerDisk = src;
  else if (type === 'iso') body.iso = src;
  else if (type === 'bootc') { body.bootc = src; body.sshKey = f.get('sshKey'); }
  else body.pvc = src;

  try {
    const res = await api('/api/vms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    $('#create-dialog').close();
    e.target.reset();
    $('#sshkey-field').hidden = true;
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

// ── Mobile drawer ─────────────────────────────────────────────────

$('#btn-menu').onclick = () => $('#tree').classList.toggle('open');
function closeDrawer() { $('#tree').classList.remove('open'); }

// ── Boot ──────────────────────────────────────────────────────────

$('#btn-menu').innerHTML = icon('menu');
$('#btn-create').innerHTML = `${icon('plus')} Create VM`;

loadCaps();
refresh();
setInterval(refresh, 5000);
