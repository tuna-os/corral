// Corral web UI — Proxmox-style dashboard for KubeVirt.
// Vanilla JS; noVNC + xterm.js loaded from CDN for the consoles.

const $ = (sel) => document.querySelector(sel);

let vms = [];
let nodes = [];
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
    lvl: 0, icon: '🏠', label: 'Datacenter',
    sel: selected.type === 'dc',
    onclick: () => select({ type: 'dc' }),
  }));

  const byNode = (nodeName) => vms.filter((v) => v.node === nodeName);
  const placed = new Set();

  for (const n of nodes) {
    tree.appendChild(treeRow({
      lvl: 1, icon: '🖥', label: n.name, sub: n.roles,
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
    lvl, icon: '⬛', label: vm.name, sub: vm.namespace,
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
      <h1>🖥 ${esc(name)}</h1>
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
  ['yaml', 'YAML'],
];

function renderVM(main, vm) {
  main.innerHTML = `
    <div class="page-head">
      <h1>⬛ ${esc(vm.name)}</h1>
      <span class="pill ${vm.ready ? 'on' : 'off'}">${esc(vm.status)}</span>
      <div class="toolbar">
        <button class="btn" data-act="start" ${vm.running ? 'disabled' : ''}>▶ Start</button>
        <button class="btn" data-act="stop" ${vm.running ? '' : 'disabled'}>■ Stop</button>
        <button class="btn" data-act="restart" ${vm.running ? '' : 'disabled'}>↻ Restart</button>
        <button class="btn" data-act="pause" ${vm.ready ? '' : 'disabled'}>⏸ Pause</button>
        <button class="btn" data-act="unpause">⏵ Resume</button>
        <button class="btn" data-act="migrate" ${vm.ready ? '' : 'disabled'}>⇄ Migrate</button>
        <button class="btn danger" data-act="delete">✕ Delete</button>
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
        <dt>CPU</dt><dd>${vm.cpu} cores</dd>
        <dt>Memory</dt><dd>${esc(vm.mem)}</dd>
        <dt>Pod IP</dt><dd>${esc(vm.ip || '—')}</dd>
        <dt>Tailnet proxy</dt><dd>${esc(vm.vnc || 'off')}</dd>
        <dt>SSH</dt><dd><code>corral ssh ${esc(vm.name)}</code></dd>
      </dl>`;
      break;
    case 'console': connectVNC(vm, body); break;
    case 'terminal': connectTTY(vm, body); break;
    case 'hardware': renderHardware(vm, body); break;
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
  try {
    await api(`/api/vms/${vm.namespace}/${vm.name}/${act}`, { method: 'POST' });
  } catch (e) { toast(e.message); }
  setTimeout(refresh, 800);
}

async function renderHardware(vm, body) {
  body.innerHTML = `<pre class="yaml">loading…</pre>`;
  try {
    const j = await api(`/api/vms/${vm.namespace}/${vm.name}`);
    const spec = j.spec?.template?.spec ?? {};
    const disks = spec.domain?.devices?.disks ?? [];
    const volumes = Object.fromEntries((spec.volumes ?? []).map((v) => [v.name, v]));
    const volDesc = (name) => {
      const v = volumes[name] || {};
      if (v.persistentVolumeClaim) return `PVC ${v.persistentVolumeClaim.claimName}`;
      if (v.containerDisk) return `containerDisk ${v.containerDisk.image}`;
      if (v.cloudInitNoCloud) return 'cloud-init';
      return Object.keys(v).filter((k) => k !== 'name').join(',') || '?';
    };
    body.innerHTML = `<dl class="props">
      <dt>CPU</dt><dd>${spec.domain?.cpu?.cores ?? '—'} cores</dd>
      <dt>Memory</dt><dd>${esc(spec.domain?.memory?.guest ?? '—')}</dd>
      <dt>Firmware</dt><dd>${spec.domain?.firmware?.kernelBoot ? 'kernel boot (bootc)' : 'BIOS'}</dd>
      ${disks.map((d) => `<dt>Disk: ${esc(d.name)}</dt>
        <dd>${esc(d.cdrom ? 'cdrom' : 'disk')} (${esc(d.disk?.bus || d.cdrom?.bus || '—')}) — ${esc(volDesc(d.name))}</dd>`).join('')}
      <dt>Node selector</dt><dd>${esc(JSON.stringify(spec.nodeSelector ?? {}) )}</dd>
    </dl>`;
  } catch (e) {
    body.innerHTML = `<p class="console-msg">${esc(e.message)}</p>`;
  }
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

refresh();
setInterval(refresh, 5000);
