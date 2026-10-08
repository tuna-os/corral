// Container (CT) screen and actions.

import { api, ctKey, findCT } from '../api.js';
import { markRendered, refresh, select } from '../app.js';
import { connectTTY, disconnectConsoles } from '../console.js';
import { icon } from '../icons.js';
import { ctMenuItems } from '../menus.js';
import { state } from '../state.js';
import { $, esc, toast } from '../ui/dom.js';
import { attachContextMenu } from '../ui/menu.js';

export function ctTable(list) {
  if (!list.length) return '';
  return `<table><thead><tr>
      <th>Name</th><th>Status</th><th>Node</th><th>Namespace</th><th>CPU</th><th>Mem</th><th>Privileged</th>
    </tr></thead><tbody>
    ${list.map((c) => `<tr data-ctkey="${esc(ctKey(c))}">
      <td>${esc(c.name)}</td>
      <td><span class="dot ${c.ready ? 'on' : c.phase === 'Stopped' ? 'off' : 'mid'}"></span> ${esc(c.phase)}</td>
      <td>${esc(c.node || '—')}</td><td>${esc(c.namespace)}</td>
      <td>${c.cpu || '—'}</td><td>${esc(c.mem || '—')}</td><td>${c.privileged ? 'yes' : 'no'}</td>
    </tr>`).join('')}
    </tbody></table>`;
}

export function bindCTTable(root) {
  root.querySelectorAll('tr[data-ctkey]').forEach((tr) => {
    const c = findCT(tr.dataset.ctkey);
    if (c) attachContextMenu(tr, () => ctMenuItems(c));
    tr.onclick = () => select({ type: 'ct', key: tr.dataset.ctkey });
    tr.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        if (!e.target.closest('button')) {
          e.preventDefault();
          select({ type: 'ct', key: tr.dataset.ctkey });
        }
      }
    });
  });
}

// Container (CT) detail view (#50) — simpler than a VM's: no VNC/hardware/
// snapshots, just Summary + Terminal (exec, not a serial console — see
// ttyBridge's VM-vs-CT dispatch) and start/stop/delete.
const CT_TABS = [['summary', 'Summary'], ['hardware', 'Hardware'], ['terminal', 'Terminal']];

export function renderCT(main, c) {
  const running = c.phase === 'Running';
  main.innerHTML = `
    <div class="page-head">
      <h1>${icon('cube')} ${esc(c.name)}</h1>
      <span class="pill ${c.ready ? 'on' : 'off'}">${esc(c.phase)}</span>
      <div class="toolbar">
        <button class="btn" data-ctact="start" ${running ? 'disabled' : ''}>${icon('play')} Start</button>
        <button class="btn" data-ctact="stop" ${running ? '' : 'disabled'}>${icon('stop')} Stop</button>
        <button class="btn danger" data-ctact="delete">${icon('trash')} Delete</button>
      </div>
    </div>
    <div class="tabs">
      ${CT_TABS.map(([id, label]) =>
        `<div class="tab ${state.ctTab === id ? 'active' : ''}" data-cttab="${id}">${label}</div>`).join('')}
    </div>
    <div id="ct-tab-body"></div>`;

  main.querySelectorAll('[data-ctact]').forEach((b) => {
    b.onclick = () => ctAction(c, b.dataset.ctact);
  });
  main.querySelectorAll('[data-cttab]').forEach((t) => {
    t.onclick = () => { disconnectConsoles(); state.ctTab = t.dataset.cttab; renderCT(main, c); markRendered(); };
  });

  const body = $('#ct-tab-body');
  if (state.ctTab === 'summary') {
    body.innerHTML = `<dl class="props">
      <dt>Status</dt><dd>${esc(c.phase)}</dd>
      <dt>Namespace</dt><dd>${esc(c.namespace)}</dd>
      <dt>Image</dt><dd><code>${esc(c.image || '—')}</code></dd>
      <dt>vCPUs</dt><dd>${c.cpu || '—'}</dd>
      <dt>Memory</dt><dd>${esc(c.mem || '—')}</dd>
      <dt>Privileged</dt><dd>${c.privileged ? 'yes' : 'no'}</dd>
      <dt>Storage</dt><dd>${c.privileged
        ? `<code>/</code> — persistent full rootfs (${esc(c.name)}-data PVC, distrobox-style: package installs and dotfiles survive Stop/Start)`
        : `<code>/data</code> only (${esc(c.name)}-data PVC) — everything else resets on Stop/Start`}</dd>
    </dl>`;
  } else if (state.ctTab === 'terminal') {
    connectTTY({ namespace: c.namespace, name: c.name, running }, body);
  } else if (state.ctTab === 'hardware') {
    body.innerHTML = `<div class="hw-edit">
      <label>vCPUs <input id="ct-hw-cpu" type="number" value="${c.cpu || 1}" min="1"></label>
      <label>Memory <input id="ct-hw-mem" value="${esc(c.mem || '512Mi')}"></label>
      <button class="btn primary" id="ct-hw-apply">Apply</button>
    </div>`;
    $('#ct-hw-apply').onclick = async () => {
      const cpu = parseInt($('#ct-hw-cpu').value, 10) || 0;
      const mem = $('#ct-hw-mem').value.trim();
      try {
        await api(`/api/cts/${c.namespace}/${c.name}/scale`, {
          method: 'PUT', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ cpu, mem }),
        });
        toast('Scaled — stop/start to apply if the pod rejects in-place resize');
        refresh(true);
      } catch (e) { toast(e.message); }
    };
  }
}

export async function ctAction(c, act) {
  if (act === 'delete') {
    if (!confirm(`Delete container ${c.name}? This removes its data volume too.`)) return;
    try {
      await api(`/api/cts/${c.namespace}/${c.name}`, { method: 'DELETE' });
      toast('Deleted');
      select({ type: 'dc' });
    } catch (e) { toast(e.message); }
    return refresh(true);
  }
  try {
    await api(`/api/cts/${c.namespace}/${c.name}/${act}`, { method: 'POST' });
    toast(act === 'start' ? 'Starting…' : 'Stopped');
  } catch (e) { toast(e.message); }
  setTimeout(() => refresh(true), 600);
}