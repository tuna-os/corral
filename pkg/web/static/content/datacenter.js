// Datacenter screen: dashboard widgets, the fleet grid with tag filter, and
// the image library.

import { api, vmKey } from '../api.js';
import { markRendered, refresh, select } from '../app.js';
import { watchBuild } from '../create.js';
import { fmtBytes, fmtCPU, mountDashboard, timeChart } from '../dashboard.js';
import { icon } from '../icons.js';
import { state } from '../state.js';
import { $, esc, toast } from '../ui/dom.js';
import { hostPowerDot, hostPowerKey } from './hostpower.js';
import { bindTemplateTable, bindVMTable, templateTable, vmTable } from './vm-table.js';

// Tag the tree/list is filtered to, or null for "show all".
let tagFilter = null;

// ── Dashboard widgets (#348) ──────────────────────────────────────
// The Datacenter and node pages open with a widget grid (dashboard.js). The
// widgets read this module's state when they render, so a poll only has to
// call the dashboard's refresh().

// "4Gi", "8G", "2048Mi" → bytes (0 when unreadable).
function memBytes(s) {
  const m = /^([\d.]+)\s*([KMGT]?)i?B?$/i.exec(String(s || '').trim());
  if (!m) return 0;
  return Number(m[1]) * (1024 ** ' KMGT'.indexOf(m[2].toUpperCase() || ' '));
}

export const NO_SAMPLES = `No samples yet. Usage comes from <strong>metrics-server</strong>
  (see <em>Cluster health</em>), sampled every 15 seconds.`;

// Run fn now and every ms until body leaves the DOM — for widgets whose data
// is not part of the fleet poll (task log, usage samples).
function pollWhileShown(body, fn, ms) {
  fn();
  const t = setInterval(() => (body.isConnected ? fn() : clearInterval(t)), ms);
}

export function chartWidget(title, url, metric) {
  return {
    title, w: 6, h: 3, minW: 3, live: true,
    render: (body) => timeChart(body, { load: () => api(url), metric, label: title, empty: NO_SAMPLES }),
  };
}

// The busiest running KubeVirt VMs by CPU or memory at the last sample,
// optionally on one node. A name opens that VM.
export function topVMsWidget(title, by, node) {
  return {
    title, w: 4, h: 3, live: true,
    render: (body) => pollWhileShown(body, () => {
      const q = new URLSearchParams({ by, limit: '5' });
      if (node) q.set('node', node);
      api(`/api/metrics/top?${q}`).then((rows) => {
        if (!body.isConnected) return;
        if (!rows.length) { body.innerHTML = `<p class="muted">${NO_SAMPLES}</p>`; return; }
        const val = (r) => (by === 'mem' ? r.mem : r.cpu);
        const top = Math.max(...rows.map(val)) || 1;
        body.innerHTML = `<table><tbody>${rows.map((r) => {
          const vm = state.vms.find((v) => v.backend === 'kubevirt' && v.namespace === r.namespace && v.name === r.name);
          const name = vm ? `<a href="#" data-vmkey="${esc(vmKey(vm))}">${esc(r.name)}</a>` : esc(r.name);
          return `<tr><td>${name}</td>
            <td style="width:55%">${by === 'mem' ? fmtBytes(r.mem) : fmtCPU(r.cpu)}
              <div class="meter"><span style="width:${((val(r) / top) * 100).toFixed(0)}%"></span></div></td></tr>`;
        }).join('')}</tbody></table>`;
        body.querySelectorAll('[data-vmkey]').forEach((a) => {
          a.onclick = (e) => { e.preventDefault(); select({ type: 'vm', key: a.dataset.vmkey }); };
        });
      }).catch((e) => { if (body.isConnected) body.innerHTML = `<p class="muted">${esc(e.message)}</p>`; });
    }, 15000),
  };
}

// Host-power hosts (sdk.CapHostPower plugins), optionally only one node's.
// The power buttons carry .hp-action, which read-only mode hides.
export function powerWidget(node) {
  return {
    title: 'Host power', w: 4, h: 3,
    render(body) {
      const hosts = (state.hostPower.hosts || []).filter((h) => !node || h.node === node);
      if (!hosts.length) {
        body.innerHTML = `<p class="muted">${node ? 'No host-power plugin manages this node.'
          : 'No host-power plugin is installed. Hosts show here when one is (see Extensions).'}</p>`;
        return;
      }
      body.innerHTML = `<ul class="plain">${hosts.map((h) => {
        const btn = (action, label) => ((h.actions || []).includes(action)
          ? `<button class="btn sm hp-action" data-hp="${action}" data-hpkey="${esc(hostPowerKey(h))}">${label}</button>` : '');
        return `<li><span class="dot ${hostPowerDot(h.state)}"></span>
          <a href="#" data-hpopen="${esc(hostPowerKey(h))}">${esc(h.name)}</a>
          <span class="muted">${esc(h.state)}</span> ${btn('start', 'Power on')} ${btn('stop', 'Power off')}</li>`;
      }).join('')}</ul>`;
      body.querySelectorAll('[data-hpopen]').forEach((a) => {
        a.onclick = (e) => { e.preventDefault(); select({ type: 'hostpower', key: a.dataset.hpopen }); };
      });
      body.querySelectorAll('[data-hp]').forEach((b) => {
        b.onclick = async () => {
          const h = hosts.find((x) => hostPowerKey(x) === b.dataset.hpkey);
          const action = b.dataset.hp;
          const onNode = h.node ? state.vms.filter((v) => v.node === h.node) : [];
          if (action === 'stop' && onNode.length && !confirm(`Power off ${h.name}? ${onNode.length} VM(s) on it will stop.`)) return;
          b.disabled = true;
          try {
            await api(`/api/hostpower/${encodeURIComponent(h.plugin)}/${action}?id=${encodeURIComponent(h.id)}`, { method: 'POST' });
            toast(`${action === 'start' ? 'Powering on' : 'Powering off'} ${h.name}`);
          } catch (e) { toast(e.message); }
          refresh(true);
        };
      });
    },
  };
}

function recentTasksWidget() {
  return {
    title: 'Recent tasks', w: 4, h: 3, live: true,
    render: (body) => pollWhileShown(body, () => {
      api('/api/tasklog').then((log) => {
        if (!body.isConnected) return;
        if (!log.length) { body.innerHTML = '<p class="muted">No tasks yet.</p>'; return; }
        const pill = (t) => (t.status === 'running' ? '<span class="pill mid">running</span>'
          : t.status === 'error' ? `<span class="pill off" title="${esc(t.error || '')}">error</span>`
            : '<span class="pill on">OK</span>');
        body.innerHTML = `<table><tbody>${log.slice(0, 8).map((t) => `<tr>
          <td class="muted">${esc(new Date(t.started).toLocaleTimeString())}</td>
          <td>${esc(t.action)}</td><td>${esc(t.target)}</td><td>${pill(t)}</td></tr>`).join('')}</tbody></table>`;
      }).catch((e) => { if (body.isConnected) body.innerHTML = `<p class="muted">${esc(e.message)}</p>`; });
    }, 5000),
  };
}

// Things an operator should look at now: nodes that are not ready, VMs in a
// failure state, stopped hosts with VMs scheduled to them, and failed tasks.
function alertsWidget() {
  return {
    title: 'Alerts', w: 4, h: 2,
    render(body) {
      const alerts = [];
      for (const n of state.nodes) if (!n.ready) alerts.push(`Node <strong>${esc(n.name)}</strong> is not ready`);
      for (const v of state.vms) {
        if (/error|fail|crash|unschedulable/i.test(v.status || '')) alerts.push(`VM <strong>${esc(v.name)}</strong>: ${esc(v.status)}`);
      }
      for (const h of state.hostPower.hosts || []) {
        const onNode = h.node ? state.vms.filter((v) => v.node === h.node).length : 0;
        if (h.state === 'stopped' && onNode) alerts.push(`Host <strong>${esc(h.name)}</strong> is off with ${onNode} VM(s) on it`);
      }
      const draw = (failed) => {
        const all = alerts.concat(failed.map((t) => `Task <strong>${esc(t.action)}</strong> ${esc(t.target)} failed${t.error ? `: ${esc(t.error)}` : ''}`));
        body.innerHTML = all.length
          ? `<ul class="plain">${all.map((a) => `<li><span class="dot off"></span> ${a}</li>`).join('')}</ul>`
          : '<p class="muted"><span class="dot on"></span> Nothing needs attention.</p>';
      };
      draw([]);
      api('/api/tasklog')
        .then((log) => { if (body.isConnected) draw(log.filter((t) => t.status === 'error').slice(0, 5)); })
        .catch(() => { /* the task log is best-effort here */ });
    },
  };
}

function capacityWidget() {
  return {
    title: 'Capacity', w: 4, h: 2,
    render(body) {
      const running = state.vms.filter((v) => v.ready);
      const cpu = running.reduce((a, v) => a + (Number(v.cpu) || 0), 0);
      const mem = running.reduce((a, v) => a + memBytes(v.mem), 0);
      body.innerHTML = `<dl class="kv">
        <dt>Virtual machines</dt><dd><span class="big">${state.vms.length}</span> <span class="muted">${running.length} running</span></dd>
        <dt>Containers</dt><dd>${state.cts.length}</dd>
        <dt>Nodes ready</dt><dd>${state.nodes.filter((n) => n.ready).length}/${state.nodes.length}</dd>
        <dt>Allocated</dt><dd>${cpu} vCPU · ${fmtBytes(mem)} <span class="muted">(running VMs)</span></dd>
      </dl>`;
    },
  };
}

const DC_WIDGETS = {
  capacity: capacityWidget(),
  alerts: alertsWidget(),
  tasks: recentTasksWidget(),
  cpu: chartWidget('CPU usage', '/api/metrics/history', 'cpu'),
  mem: chartWidget('Memory usage', '/api/metrics/history', 'mem'),
  'top-cpu': topVMsWidget('Top VMs by CPU', 'cpu'),
  'top-mem': topVMsWidget('Top VMs by memory', 'mem'),
  power: powerWidget(),
};

const DC_LAYOUT = [
  { id: 'capacity', x: 0, y: 0, w: 4, h: 2 },
  { id: 'alerts', x: 4, y: 0, w: 4, h: 2 },
  { id: 'tasks', x: 8, y: 0, w: 4, h: 3 },
  { id: 'cpu', x: 0, y: 2, w: 4, h: 3 },
  { id: 'mem', x: 4, y: 2, w: 4, h: 3 },
  { id: 'top-cpu', x: 0, y: 5, w: 4, h: 3 },
  { id: 'top-mem', x: 4, y: 5, w: 4, h: 3 },
  { id: 'power', x: 8, y: 3, w: 4, h: 3 },
];

let dcDash = null;

export function renderDatacenter(main) {
  // Mount the widget grid once per visit; later polls refresh it in place so
  // a drag, an open menu or keyboard focus survives the 5s refresh.
  if (!main.querySelector('#dc-dash')) {
    main.innerHTML = `<div class="page-head"><h1>Datacenter</h1></div>
      <div id="dc-dash"></div><div id="dc-rest"></div>`;
    dcDash = mountDashboard($('#dc-dash'), { scope: 'datacenter', widgets: DC_WIDGETS, layout: DC_LAYOUT });
  } else {
    dcDash.refresh();
  }
  const rest = $('#dc-rest');
  const allTags = [...new Set(state.vms.flatMap((v) => v.tags || []))].sort();
  if (tagFilter && !allTags.includes(tagFilter)) tagFilter = null; // tag vanished
  const shown = tagFilter ? state.vms.filter((v) => (v.tags || []).includes(tagFilter)) : state.vms;
  rest.innerHTML = `
    ${allTags.length ? `<div class="tagbar">
      <span class="muted">Filter by tag:</span>
      <button class="chip filter ${tagFilter ? '' : 'active'}" data-tagfilter="">all</button>
      ${allTags.map((t) => `<button class="chip filter ${tagFilter === t ? 'active' : ''}" data-tagfilter="${esc(t)}">${esc(t)}</button>`).join('')}
    </div>` : ''}
    ${vmTable(shown)}
    <h2 class="section">${icon('disk')} Image library
      <button class="btn" id="dc-import">${icon('download')} Import image</button>
      <button class="btn" id="dc-upload">${icon('download')} Upload ISO</button>
      <input type="file" id="dc-upload-file" accept=".iso,.img,.qcow2,.raw" hidden>
    </h2>
    <div id="dc-images"><p class="muted">loading…</p></div>
    <h2 class="section">${icon('template')} Templates</h2>
    ${templateTable(state.vms.filter((v) => v.isTemplate))}`;
  bindVMTable(rest, shown);
  bindTemplateTable(rest);
  rest.querySelectorAll('[data-tagfilter]').forEach((b) => {
    b.onclick = () => { tagFilter = b.dataset.tagfilter || null; renderDatacenter(main); markRendered(); };
  });
  $('#dc-import').onclick = importImage;
  $('#dc-upload').onclick = () => $('#dc-upload-file').click();
  $('#dc-upload-file').onchange = (e) => {
    const file = e.target.files[0];
    e.target.value = ''; // allow re-selecting the same file next time
    if (file) uploadImage(file);
  };
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

// Uploads a local file straight to a new DataVolume via the CDI upload
// proxy (server-side: pkg/kubevirt.UploadDataVolume, which shells out to
// `virtctl image-upload` rather than reimplementing CDI's upload protocol).
async function uploadImage(file) {
  const guess = file.name.replace(/\.[^.]+$/, '').replace(/[^a-z0-9-]/gi, '-').toLowerCase().slice(0, 40) || 'image';
  const name = prompt('Name for this image:', guess);
  if (!name) return;
  const sizeGuess = Math.ceil(file.size / (1024 ** 3)) + 1; // pad a bit over the raw file size
  const size = prompt('DataVolume size (e.g. 10Gi) — must fit the uploaded file:', `${sizeGuess}Gi`) || `${sizeGuess}Gi`;

  const qs = new URLSearchParams({ name, namespace: '', size });
  let res;
  try {
    res = await api(`/api/datavolumes/upload?${qs}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/octet-stream' },
      body: file,
    });
  } catch (e) { toast(`Upload failed: ${e.message}`); return; }

  if (res.task) {
    watchBuild(res.task, name, {
      titleRun: `Uploading ${file.name}…`,
      titleDone: `✅ ${name} uploaded`,
      titleFail: `❌ Upload failed`,
      onDone: loadImages,
    });
  }
}