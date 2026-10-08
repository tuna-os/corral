// VM screen: the tab strip, every VM tab, and the VM actions.

import { api, vmKey, vmURL } from '../api.js';
import { markRendered, refresh, select } from '../app.js';
import { connectRDP, connectTTY, connectVNC, consoleGeneration, disconnectConsoles } from '../console.js';
import { watchBuild } from '../create.js';
import { timeChart } from '../dashboard.js';
import { icon } from '../icons.js';
import { state } from '../state.js';
import { bindBreadcrumb, breadcrumb } from '../ui/breadcrumb.js';
import { openMenuFrom } from '../ui/menu.js';
import { bindTags, tagChips } from '../tags.js';
import { $, esc, toast } from '../ui/dom.js';
import { NO_SAMPLES } from './datacenter.js';
import { confirmDestroy } from '../ui/confirm.js';

// ── VM view ───────────────────────────────────────────────────────

const TABS = [
  ['summary', 'Summary'],
  ['console', 'Console'],
  ['terminal', 'Terminal'],
  ['rdp', 'RDP'],
  ['hardware', 'Hardware'],
  ['options', 'Options'],
  ['snapshots', 'Snapshots'],
  ['events', 'Events'],
  ['yaml', 'YAML'],
];

export function renderVM(main, vm) {
  // Local QEMU VMs (#91): lifecycle, VNC console (the ws bridge dials the
  // local VNC port), and info — cluster concepts (migrate, snapshots, pause,
  // templates…) don't exist on this backend.
  const isLocal = vm.backend === 'qemu';
  const isKubeVirt = vm.backend === 'kubevirt';
  const capability = vm.capabilities || {};
  const tabs = TABS.filter(([id]) => {
    if (id === 'summary' || id === 'yaml') return true;
    if (id === 'console') return !!capability.vnc;
    if (id === 'terminal') return !!capability.tty;
    if (id === 'rdp') return !!capability.rdp;
    if (id === 'snapshots') return !!capability.snapshots;
    return isKubeVirt;
  });
  if (!tabs.some(([id]) => id === state.tab)) state.tab = 'summary';
  // One list of actions, two presentations.
  //
  // The toolbar had eleven buttons written out as markup. At phone width they
  // wrapped onto three rows and took most of the first screen before any
  // detail appeared. The lifecycle verbs stay on the bar and give up their
  // labels there; the rest move behind "More".
  //
  // Both read this list, so the bar and the menu cannot drift apart — which is
  // exactly what two hand-written copies of eleven actions would do.
  const paused = (vm.status || '').includes('Paused');
  const TOOLBAR = [
    { act: 'start', label: 'Start', ic: 'play', show: capability.start, off: vm.running, primary: true },
    { act: 'stop', label: 'Stop', ic: 'stop', show: capability.stop, off: !vm.running, primary: true },
    { act: 'restart', label: 'Restart', ic: 'restart', show: capability.start && capability.stop, off: !vm.running, primary: true },
    // Pause and Resume are the same button in two states, not two buttons.
    // Both carry the play/pause pair of icons, so with the labels hidden on a
    // phone the bar showed two identical triangles — Start and Resume. Only
    // the one that applies is offered, which also stops Resume being live on a
    // VM that is merely running.
    { act: 'pause', label: 'Pause', ic: 'pause', show: isKubeVirt && !paused, off: !vm.ready, primary: true },
    { act: 'unpause', label: 'Resume', ic: 'play', show: isKubeVirt && paused, primary: true },
    {
      act: 'migrate', label: 'Migrate', ic: 'migrate', show: isKubeVirt,
      off: !(vm.ready && vm.liveMigratable),
      title: vm.liveMigratable ? 'Live-migrate to another node' : 'Not live-migratable (persistent RWO disk)',
    },
    { act: 'clone', label: 'Clone', ic: 'clone', show: isKubeVirt },
    {
      act: 'template', label: vm.isTemplate ? 'Unmark template' : 'Make template', ic: 'template',
      show: isKubeVirt,
      title: vm.isTemplate ? 'Remove template mark' : 'Mark as a golden template to clone from',
    },
    {
      act: 'export', label: 'Export', ic: 'download', show: isKubeVirt, off: vm.running,
      title: vm.running ? 'Stop the VM to export its disk' : 'Download a disk backup',
    },
    {
      act: 'upgrade', label: 'Upgrade', ic: 'restart', show: isKubeVirt && vm.bootc && state.caps.bootc,
      title: "Rebuild this bootc VM's disk from the latest image and restart",
    },
    // Destructive, and the hardest to undo after a mis-tap, so it is never one
    // of the bare icons on the bar.
    { act: 'delete', label: 'Delete', ic: 'trash', show: capability.delete, danger: true },
  ].filter((a) => a.show);
  const overflow = TOOLBAR.filter((a) => !a.primary);
  const btn = (a) => `<button class="btn${a.danger ? ' danger' : ''}${a.primary ? '' : ' act-more'}"
    data-act="${a.act}" aria-label="${esc(a.label)}" ${a.off ? 'disabled' : ''}
    title="${esc(a.title || a.label)}">${icon(a.ic)}<span class="btn-label">${esc(a.label)}</span></button>`;

  // Where this guest sits. A detail screen reached from the palette or a
  // pop-out carries no tree context otherwise.
  const trail = [
    { label: 'Datacenter', go: () => select({ type: 'dc' }) },
    vm.node
      ? { label: vm.node, go: () => select({ type: 'node', name: vm.node }) }
      : (vm.namespace ? { label: vm.namespace, go: () => select({ type: 'namespace', name: vm.namespace }) } : null),
    { label: vm.name },
  ];
  main.innerHTML = `
    ${breadcrumb(trail)}
    <div class="page-head">
      <h1>${icon('cube')} ${esc(vm.name)}</h1>
      <span class="pill ${vm.ready ? 'on' : (vm.running || (vm.status && (vm.status.includes('Starting') || vm.status.includes('Creating')))) ? 'mid' : 'off'}">${esc(vm.status)}</span>
      ${isLocal ? '<span class="pill">local · qemu</span>' : ''}
      <div class="toolbar">
        ${TOOLBAR.filter((a) => a.primary).map(btn).join('')}
        ${overflow.length ? `<button class="btn btn-overflow" data-overflow aria-haspopup="menu" aria-expanded="false"
          aria-label="More actions" title="More actions">${icon('menu')}<span class="btn-label">More</span></button>` : ''}
        ${overflow.map(btn).join('')}
      </div>
    </div>
    <div class="tabs">
      ${tabs.map(([id, label]) =>
        `<div class="tab ${state.tab === id ? 'active' : ''}" data-tab="${id}">${label}</div>`).join('')}
    </div>
    <div id="tab-body"></div>`;

  bindBreadcrumb(main, trail);
  main.querySelectorAll('[data-act]').forEach((b) => {
    b.onclick = () => vmAction(vm, b.dataset.act);
  });
  // The overflow menu runs the same actions as the buttons it stands in for.
  const more = main.querySelector('[data-overflow]');
  if (more) {
    more.onclick = (e) => {
      e.stopPropagation();
      openMenuFrom(more, overflow.map((a) => ({
        icon: a.ic,
        label: a.label,
        title: a.title,
        danger: a.danger,
        disabled: a.off,
        mutate: a.act !== 'export',
        action: () => vmAction(vm, a.act),
      })));
    };
  }
  main.querySelectorAll('[data-tab]').forEach((t) => {
    t.onclick = () => { disconnectConsoles(); state.tab = t.dataset.tab; renderVM(main, vm); markRendered(); };
  });

  // A live console is a WebSocket attached to a canvas, and rebuilding
  // #tab-body would drop both and reconnect — every five seconds. app.js used
  // to avoid that by not rendering the content pane at all while a console tab
  // was open, which froze the whole VM page: status, usage, everything stopped
  // updating until you left the tab.
  //
  // Instead the element the console is mounted in is kept and moved into the
  // freshly rendered page, so the connection is never touched and the rest of
  // the page around it stays live. The cache is keyed by VM and tab, and
  // checked against the console generation: every deliberate switch calls
  // disconnectConsoles() first, which bumps it, so a stale body can never be
  // mistaken for a connected one.
  const want = `${vmKey(vm)}:${state.tab}`;
  const fresh = $('#tab-body');
  if (liveBody && liveFor === want && liveGen === consoleGeneration() && fresh !== liveBody) {
    fresh.replaceWith(liveBody);
    return;
  }
  liveBody = null;
  renderTab(vm);
  if (LIVE_TABS.has(state.tab)) {
    liveBody = $('#tab-body');
    liveFor = want;
    liveGen = consoleGeneration();
  }
}

// The tabs whose body holds a live connection rather than markup.
const LIVE_TABS = new Set(['console', 'terminal', 'rdp']);
let liveBody = null;
let liveFor = '';
let liveGen = -1;

function renderTab(vm) {
  const body = $('#tab-body');
  const capability = vm.capabilities || {};
  switch (state.tab) {
    case 'summary':
      if (vm.backend !== 'kubevirt') {
        // Non-KubeVirt summary: do not issue cluster-only API calls.
        body.innerHTML = `<dl class="props">
          <dt>Status</dt><dd>${esc(vm.status)}</dd>
          <dt>Backend</dt><dd>${esc(vm.backend)}</dd>
          <dt>Context</dt><dd>${esc(vm.context || 'local')}</dd>
          <dt>vCPUs</dt><dd>${vm.cpu}</dd>
          <dt>Memory</dt><dd>${esc(vm.mem)}</dd>
          <dt>Disk</dt><dd>${esc(vm.disk || '—')}</dd>
          <dt>Tailscale IP</dt><dd>${esc(vm.ip || '—')}</dd>
          <dt>VNC</dt><dd>${vm.vnc ? `<code>vnc://${esc(vm.ip || 'host')}:${esc(vm.vnc)}</code>` : '—'}</dd>
          <dt>SSH</dt><dd><code>corral ssh ${esc(vm.name)}</code></dd>
        </dl>`;
        break;
      }
      body.innerHTML = `<dl class="props">
        <dt>Status</dt><dd>${esc(vm.status)}</dd>
        <dt>Tags</dt><dd id="vm-tags">${tagChips(vm)}</dd>
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
      <div id="usage-charts" class="panel-section">
        <h2 class="section">${icon('cpu')} Usage</h2>
        ${vm.running ? `<div class="charts-row">
          <div><strong>CPU</strong> <span class="muted">(${vm.cpu} vCPU = ${vm.cpu * 1000}m)</span><div id="vm-cpu-chart"></div></div>
          <div><strong>Memory</strong> <span class="muted">(${esc(vm.mem)} allocated)</span><div id="vm-mem-chart"></div></div>
        </div>` : '<p class="muted">VM is stopped</p>'}
      </div>
      <div id="guest-info"></div>
      <div id="powersched-box" class="panel-section"><p class="muted">loading schedule…</p></div>`;
      if (vm.running && capability.metrics) loadMetrics(vm);
      if (vm.running) loadUsageCharts(vm);
      if (vm.running && capability.rdp) checkRDP(vm);
      if (vm.agentConnected) loadGuestInfo(vm);
      renderPowerSchedule(vm);
      bindTags(vm);
      break;
    case 'console': connectVNC(vm, body); break;
    case 'terminal': connectTTY(vm, body); break;
    case 'rdp': connectRDP(vm, body); break;
    case 'hardware': renderHardware(vm, body); break;
    case 'options': renderOptions(vm, body); break;
    case 'snapshots': renderSnapshots(vm, body); break;
    case 'events': renderEvents(vm, body); break;
    case 'yaml':
      body.innerHTML = `<pre class="yaml">loading…</pre>`;
      api(vmURL(vm))
        .then((j) => { body.querySelector('pre').textContent = JSON.stringify(j, null, 2); })
        .catch((e) => { body.querySelector('pre').textContent = e.message; });
      break;
  }
}

export async function vmAction(vm, act) {
  if (act === 'delete') {
    if (!await confirmDestroy({
      title: `Delete ${vm.name}?`,
      identifier: vm.name,
      label: `Type ${vm.name} to confirm`,
      note: 'This destroys the guest and its disks. There is no undo.',
    })) return;
    try {
      let target = vmURL(vm);
      if (vm.backend === 'libvirt') target += `${target.includes('?') ? '&' : '?'}destroyStorage=true`;
      await api(target, { method: 'DELETE' });
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
  if (act === 'export') return exportVM(vm);
  if (act === 'upgrade') {
    const ref = prompt('Rebuild from which bootc image?\nLeave blank to pull the latest of the current image, or enter a new image to switch.', '');
    if (ref === null) return; // cancelled
    try {
      const res = await api(vmURL(vm, '/bootc/rebuild'), {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ image: ref.trim() }),
      });
      if (res.task) watchBuild(res.task, vm.name);
    } catch (e) { toast(e.message); }
    return;
  }
  try {
    await api(vmURL(vm, `/${act}`), { method: 'POST' });
  } catch (e) { toast(e.message); }
  setTimeout(refresh, 800);
}

export async function post(vm, path, body) {
  return api(vmURL(vm, path), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body || {}),
  });
}

// migrateVM asks for (or, after a drag onto a node, confirms) the target node.
export async function migrateVM(vm, targetNode = null) {
  if (document.body.classList.contains('read-only') || !state.me.admin) return;
  const others = state.nodes.filter((n) => n.ready && n.name !== vm.node).map((n) => n.name);
  if (!others.length) { toast('No other ready node to migrate to.'); return; }
  const target = await pickNode(vm, others, targetNode);
  if (target === null) return; // cancelled
  let res;
  try {
    res = await post(vm, '/migrate', { targetNode: target });
  } catch (e) { toast(e.message); return; }
  if (res && res.task) {
    watchBuild(res.task, vm.name, {
      titleRun: `Migrating ${vm.name}${target ? ` → ${target}` : ''}…`,
      titleDone: `✅ ${vm.name} migrated`,
      titleFail: `❌ Migration failed`,
    });
  }
}

// exportVM lets the user pick a disk-backup format, then downloads it. qcow2
// (compact, compressed, portable) is the default; raw.gz is the no-qemu-img
// fallback the server always supports.
export async function exportVM(vm) {
  const fmt = await pickExportFormat(vm);
  if (!fmt) return; // cancelled
  toast('Preparing backup… the download will start when ready.');
  const q = fmt === 'qcow2' ? '?format=qcow2' : '';
  const base = vmURL(vm, '/export');
  window.location.href = base + (base.includes('?') ? `&${q.slice(1)}` : q);
}

function pickExportFormat(vm) {
  return new Promise((resolve) => {
    const dlg = document.createElement('dialog');
    dlg.className = 'pick-dialog';
    dlg.innerHTML = `
      <h3>Export ${esc(vm.name)}</h3>
      <p class="muted">Download a backup of the VM's disk.</p>
      <label>Format
        <select id="pick-fmt">
          <option value="qcow2">qcow2 — compact, compressed, portable (recommended)</option>
          <option value="raw">raw.gz — gzipped raw image (always available)</option>
        </select>
      </label>
      <p class="muted" style="font-size:.78rem">qcow2 needs qemu-img on the server; if it's unavailable the download falls back with a clear error and raw.gz still works.</p>
      <div class="pick-actions">
        <button class="btn" value="cancel">Cancel</button>
        <button class="btn primary" id="pick-go">${icon('download')} Download</button>
      </div>`;
    document.body.appendChild(dlg);
    const finish = (val) => { dlg.close(); dlg.remove(); resolve(val); };
    dlg.querySelector('[value="cancel"]').onclick = () => finish(null);
    dlg.querySelector('#pick-go').onclick = () => finish(dlg.querySelector('#pick-fmt').value);
    dlg.addEventListener('cancel', () => finish(null));
    dlg.showModal();
  });
}

// pickNode shows a small modal with a target-node dropdown (eligible nodes
// only) and what kind of migration this will be. preselect is the node a VM
// was dropped on. Resolves to the chosen node name, '' for "let the scheduler
// choose", or null if cancelled.
function pickNode(vm, eligible, preselect = null) {
  return new Promise((resolve) => {
    const dlg = document.createElement('dialog');
    dlg.className = 'pick-dialog migrate-dialog';
    const mode = !vm.running
      ? `<li><span class="dot off"></span> <b>Offline:</b> the VM is stopped; it starts on the new node next time.</li>`
      : vm.liveMigratable
        ? `<li><span class="dot on"></span> <b>Live:</b> memory and CPU state move with no downtime.</li>`
        : `<li><span class="dot mid"></span> <b>Not live-migratable:</b> the migration may fail; stop the VM to move it offline.</li>`;
    dlg.innerHTML = `
      <h3>Migrate ${esc(vm.name)}</h3>
      <p class="muted">Currently on <strong>${esc(vm.node || '—')}</strong>. ${preselect ? `Move to <strong>${esc(preselect)}</strong>?` : 'Pick a target node.'}</p>
      <label>Target node
        <select id="pick-node">
          <option value="">Auto — let the scheduler choose</option>
          ${eligible.map((n) => `<option value="${esc(n)}"${n === preselect ? ' selected' : ''}>${esc(n)}</option>`).join('')}
        </select>
      </label>
      <ul class="migrate-checks">${mode}</ul>
      <div class="pick-actions">
        <button class="btn" value="cancel">Cancel</button>
        <button class="btn primary" id="pick-go">${icon('migrate')} Migrate</button>
      </div>`;
    document.body.appendChild(dlg);
    const finish = (val) => { dlg.close(); dlg.remove(); resolve(val); };
    dlg.querySelector('[value="cancel"]').onclick = () => finish(null);
    dlg.querySelector('#pick-go').onclick = () => finish(dlg.querySelector('#pick-node').value);
    dlg.addEventListener('cancel', () => finish(null)); // Esc key
    dlg.showModal();
  });
}

export async function cloneVM(vm) {
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
  try { info = await api(vmURL(vm, '/guestinfo')); }
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
  try { r = await api(vmURL(vm, '/rdp')); } catch { r = { open: false }; }
  const el = $('#vm-rdp');
  if (!el) return; // user navigated away
  el.innerHTML = r.open
    ? `available — <code>virtctl port-forward vm/${esc(vm.name)} 3389:3389 -n ${esc(vm.namespace)}</code> then point your RDP client at localhost:3389`
    : '—';
}

async function loadMetrics(vm) {
  try {
    const m = await api(vmURL(vm, '/metrics'));
    const el = $('#vm-usage');
    if (el) el.textContent = (m.cpu || m.mem) ? `${m.cpu || '?'} CPU · ${m.mem || '?'} mem` : 'no metrics yet';
  } catch { /* metrics-server may be absent */ }
}

// ── Usage charts (RRD-style history) ──────────────────────────────
// The server samples per-VM CPU and memory into a bounded ring buffer; the
// charts poll the retained window and stop once they leave the DOM.
function loadUsageCharts(vm) {
  const load = () => api(vmURL(vm, '/metrics/history'));
  timeChart($('#vm-cpu-chart'), { load, metric: 'cpu', label: `${vm.name} CPU usage`, empty: NO_SAMPLES, height: 140 });
  timeChart($('#vm-mem-chart'), { load, metric: 'mem', label: `${vm.name} memory usage`, empty: NO_SAMPLES, height: 140 });
}

// Autostart/shutdown windows (schedule plugin): two cron boundaries that flip
// the VM on/off. Built into the web server (pkg/cronops) — no plugin needed.
async function renderPowerSchedule(vm) {
  const box = $('#powersched-box');
  if (!box) return;
  let s = {};
  try { s = await api(vmURL(vm, '/powerschedule')); } catch { /* form */ }
  if (!box.isConnected) return; // the tab changed while this loaded
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
      await api(vmURL(vm, '/powerschedule'), {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ start: $('#pwr-start').value.trim(), stop: $('#pwr-stop').value.trim() }),
      });
      toast('Schedule saved');
    } catch (e) { toast(e.message); }
    renderPowerSchedule(vm);
  };
  const clr = $('#pwr-clear');
  if (clr) clr.onclick = async () => {
    try { await api(vmURL(vm, '/powerschedule'), { method: 'DELETE' }); toast('Schedule cleared'); }
    catch (e) { toast(e.message); }
    renderPowerSchedule(vm);
  };
}

async function renderEvents(vm, body) {
  body.innerHTML = `<p class="muted">loading…</p>`;
  let evs;
  try { evs = await api(vmURL(vm, '/events')); }
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
  try { j = await api(vmURL(vm)); }
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
          <td>${pvc ? `<button class="btn sm" data-expand="${esc(pvc)}" ${state.caps.canExpand ? '' : 'disabled'}
              title="${state.caps.canExpand ? 'Grow this disk' : 'Storage class does not support expansion'}">${icon('expand')} Expand</button>` : ''}
            ${pvc && d.name.includes('-hp-') ? `<button class="btn sm danger" data-rmvol="${esc(d.name)}">Detach</button>` : ''}
          </td>
        </tr>`;
      }).join('')}
    </tbody></table>

    <h2 class="section">${icon('server')} Network
      ${state.availableNADs.length ? `<button class="btn" id="hw-addnic">${icon('plus')} Add NIC</button>` : ''}
    </h2>
    ${networkTable(spec, vm)}

    <h2 class="section">${icon('cpu')} GPU / PCI passthrough</h2>
    <div id="hw-gpus"><p class="muted">loading…</p></div>

    <h2 class="section">${icon('info')} Firmware</h2>
    <dl class="props">
      <dt>Boot</dt><dd>${spec.domain?.firmware?.kernelBoot ? 'kernel boot (bootc)' : 'BIOS'}</dd>
      <dt>Node selector</dt><dd>${Object.keys(spec.nodeSelector ?? {}).length
        ? `<code>${esc(JSON.stringify(spec.nodeSelector))}</code>`
        : '<span class="muted">(any node)</span>'}</dd>
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
        await api(vmURL(vm, `/volumes/${b.dataset.rmvol}`), { method: 'DELETE' });
        toast('Detached');
      } catch (e) { toast(e.message); }
      setTimeout(() => renderHardware(vm, body), 800);
    };
  });
  const addNic = $('#hw-addnic');
  if (addNic) addNic.onclick = async () => {
    const nad = prompt(`Attach a NIC on which network?\nAvailable: ${state.availableNADs.join(', ')}`, state.availableNADs[0] || '');
    if (!nad) return;
    try { await post(vm, '/nics', { nad: nad.trim() }); toast('NIC added'); }
    catch (e) { toast(e.message); }
    setTimeout(() => renderHardware(vm, body), 800);
  };
}

// Options tab (#48): fields that map honestly onto the KubeVirt VM spec —
// start-on-boot (applies immediately, no VM restart), and boot
// order/firmware/machine type (all read once at VMI startup, so they show
// an "applies on next boot" badge instead of pretending to hot-apply).
// Fields with no honest KubeVirt equivalent (PVE's "swap", BIOS-only knobs
// KubeVirt doesn't expose) are simply absent, not stubbed.
async function renderOptions(vm, body) {
  body.innerHTML = `<pre class="yaml">loading…</pre>`;
  let j;
  try { j = await api(vmURL(vm)); }
  catch (e) { body.innerHTML = `<p class="console-msg">${esc(e.message)}</p>`; return; }

  const spec = j.spec ?? {};
  const tspec = spec.template?.spec ?? {};
  const domain = tspec.domain ?? {};
  const runStrategy = spec.runStrategy || (spec.running ? 'Always' : 'Manual');
  const isBootc = !!domain.firmware?.kernelBoot;
  const firmware = domain.firmware?.bootloader?.efi ? 'uefi'
    : domain.firmware?.bootloader?.bios ? 'bios' : '';
  const machineType = domain.machine?.type || '';

  const disks = domain.devices?.disks ?? [];
  const ifaces = domain.devices?.interfaces ?? [];
  const bootDevices = [
    ...disks.map((d) => ({ name: d.name, kind: 'disk', order: d.bootOrder })),
    ...ifaces.map((i) => ({ name: i.name, kind: 'nic', order: i.bootOrder })),
  ];

  const restartBadge = `<span class="pill mid" title="Read once at VM startup — takes effect the next time the VM (re)starts">applies on next boot</span>`;
  const liveBadge = `<span class="pill on" title="A controller-level setting, not part of the VM's boot template — takes effect immediately, no restart needed">applies immediately</span>`;

  body.innerHTML = `
    <h2 class="section">${icon('play')} Start on boot ${liveBadge}</h2>
    <div class="hw-edit">
      <label>Behavior
        <select id="opt-runstrategy">
          <option value="Always" ${runStrategy === 'Always' ? 'selected' : ''}>Always — auto-starts, stays running</option>
          <option value="Manual" ${runStrategy === 'Manual' ? 'selected' : ''}>Manual — start/stop is up to you</option>
        </select>
      </label>
      <button class="btn primary" id="opt-apply-runstrategy">Apply</button>
    </div>

    <h2 class="section">${icon('info')} Firmware &amp; machine type ${restartBadge}</h2>
    ${isBootc
      ? `<p class="muted">This VM boots a bootc-built disk via UEFI firmware set at build time — not editable here.</p>`
      : `<div class="hw-edit">
          <label>Firmware
            <select id="opt-firmware">
              <option value="bios" ${firmware === 'bios' || firmware === '' ? 'selected' : ''}>BIOS</option>
              <option value="uefi" ${firmware === 'uefi' ? 'selected' : ''}>UEFI (OVMF)</option>
            </select>
          </label>
          <label>Machine type <input id="opt-machine" value="${esc(machineType)}" placeholder="q35"></label>
          <button class="btn primary" id="opt-apply-firmware">Apply</button>
        </div>`}

    <h2 class="section">${icon('expand')} Boot order ${restartBadge}</h2>
    ${bootDevices.length
      ? `<table><thead><tr><th>Device</th><th>Type</th><th>Order</th></tr></thead><tbody>
          ${bootDevices.map((d) => `<tr>
            <td>${esc(d.name)}</td><td>${d.kind}</td>
            <td><input type="number" min="1" class="opt-bootorder" data-device="${esc(d.name)}"
              value="${d.order ?? ''}" style="width:4em"></td>
          </tr>`).join('')}
        </tbody></table>
        <div class="hw-edit"><button class="btn primary" id="opt-apply-bootorder">Apply</button>
          <span class="muted">Leave blank for devices with no explicit order.</span></div>`
      : `<p class="muted">No disks or interfaces to order.</p>`}

    <h2 class="section">${icon('terminal')} Guest agent</h2>
    <dl class="props">
      <dt>Status</dt><dd>${vm.agentConnected ? 'connected' : 'not connected'}</dd>
    </dl>
    ${vm.agentConnected ? '' : `<p class="muted">No server-side toggle — qemu-guest-agent runs inside the guest.
      Add it via cloud-init (<code>packages: [qemu-guest-agent]</code>,
      <code>runcmd: [systemctl enable --now qemu-guest-agent]</code>) or bake it into a bootc image.</p>`}`;

  $('#opt-apply-runstrategy').onclick = async () => {
    const v = $('#opt-runstrategy').value;
    if (v === runStrategy) { toast('No changes'); return; }
    try { await post(vm, '/options', { runStrategy: v }); toast('Applied'); }
    catch (e) { toast(e.message); }
    setTimeout(() => renderOptions(vm, body), 800);
  };

  const applyFirmware = $('#opt-apply-firmware');
  if (applyFirmware) applyFirmware.onclick = async () => {
    const newFirmware = $('#opt-firmware').value;
    const newMachine = $('#opt-machine').value.trim();
    const payload = {};
    if (newFirmware !== firmware) payload.firmware = newFirmware;
    if (newMachine !== machineType) payload.machineType = newMachine;
    if (!payload.firmware && !payload.machineType) { toast('No changes'); return; }
    try { await post(vm, '/options', payload); toast('Applied — restart the VM for it to take effect'); }
    catch (e) { toast(e.message); }
    setTimeout(() => renderOptions(vm, body), 800);
  };

  const applyBootOrder = $('#opt-apply-bootorder');
  if (applyBootOrder) applyBootOrder.onclick = async () => {
    const bootOrder = {};
    body.querySelectorAll('.opt-bootorder').forEach((inp) => {
      const n = parseInt(inp.value, 10);
      if (n) bootOrder[inp.dataset.device] = n;
    });
    if (!Object.keys(bootOrder).length) { toast('No boot order set'); return; }
    try { await post(vm, '/options', { bootOrder }); toast('Applied — restart the VM for it to take effect'); }
    catch (e) { toast(e.message); }
    setTimeout(() => renderOptions(vm, body), 800);
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
  try { attached = await api(vmURL(vm, '/gpus')); } catch { /* none */ }

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
        await api(vmURL(vm, `/gpus/${b.dataset.rmgpu}`), { method: 'DELETE' });
        toast('GPU detached');
      } catch (e) { toast(e.message); }
      renderGPUs(vm);
    };
  });
}

async function renderSnapshots(vm, body) {
  if (!state.caps.canSnapshot) {
    body.innerHTML = `<p class="console-msg">Snapshots need a snapshot-capable StorageClass
      (no VolumeSnapshotClass found in this cluster).</p>`;
    return;
  }
  // Scheduling is built into the web server (pkg/cronops); it needs the same
  // snapshot-capable storage as one-off snapshots, which canSnapshot gates.
  const hasSnapsched = state.caps.canSnapshot;
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
  try { snaps = await api(vmURL(vm, '/snapshots')); }
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
        await api(vmURL(vm, `/snapshots/${b.dataset.delsnap}`), { method: 'DELETE' });
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
  try { sched = await api(vmURL(vm, '/snapschedule')); }
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
      try { await api(vmURL(vm, '/snapschedule'), { method: 'DELETE' }); toast('Schedule removed'); }
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
      await api(vmURL(vm, '/snapschedule'), {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ every, keep }),
      });
      toast('Schedule created');
    } catch (e) { toast(e.message); }
    renderSnapSchedule(vm);
  };
}