// Host power screen: hosts exposed by host-power plugins.

import { api } from '../api.js';
import { refresh } from '../app.js';
import { icon } from '../icons.js';
import { state } from '../state.js';
import { esc, toast } from '../ui/dom.js';

// ── Host power (host-power plugins) ──────────────────────────────
export function hostPowerKey(h) { return `${h.plugin}/${h.id}`; }
export function hostPowerDot(state) {
  if (state === 'running') return 'on';
  if (state === 'stopped') return 'off';
  return 'mid'; // starting / stopping / unknown
}

export function renderHostPower(main, key) {
  const h = (state.hostPower.hosts || []).find((x) => hostPowerKey(x) === key);
  if (!h) { main.innerHTML = '<p class="muted">Host no longer reported by its plugin.</p>'; return; }
  const onNode = h.node ? state.vms.filter((v) => v.node === h.node) : [];
  const btn = (action, label, cls) => (h.actions || []).includes(action)
    ? `<button class="btn ${cls} hp-action" data-hp="${action}">${icon(action === 'start' ? 'play' : 'stop')} ${label}</button>` : '';
  main.innerHTML = `<div class="page-head"><h1>${icon('server')} ${esc(h.name)}</h1>
      <div>${btn('start', 'Power on', 'primary')} ${btn('stop', 'Power off', '')}</div></div>
    <table><tbody>
      <tr><td class="muted">State</td><td><span class="dot ${hostPowerDot(h.state)}"></span> ${esc(h.state)}</td></tr>
      ${h.node ? `<tr><td class="muted">Kubernetes node</td><td>${esc(h.node)}</td></tr>` : ''}
      ${h.detail ? `<tr><td class="muted">Detail</td><td>${esc(h.detail)}</td></tr>` : ''}
      <tr><td class="muted">Provided by</td><td><code>corral-${esc(h.plugin)}</code></td></tr>
      ${h.node ? `<tr><td class="muted">VMs on this host</td><td>${onNode.length ? onNode.map((v) => esc(v.name)).join(', ') : 'none'}</td></tr>` : ''}
    </tbody></table>
    ${h.state === 'stopped' ? '<p class="muted" style="margin-top:14px">VMs scheduled to this host stay pending until it is powered on.</p>' : ''}`;
  main.querySelectorAll('[data-hp]').forEach((b) => {
    b.onclick = async () => {
      const action = b.dataset.hp;
      if (action === 'stop' && onNode.length && !confirm(`Power off ${h.name}? ${onNode.length} VM(s) on it will stop.`)) return;
      b.disabled = true;
      try {
        await api(`/api/hostpower/${encodeURIComponent(h.plugin)}/${action}?id=${encodeURIComponent(h.id)}`, { method: 'POST' });
        toast(`${action === 'start' ? 'Powering on' : 'Powering off'} ${h.name}`);
      } catch (e) { toast(e.message); }
      refresh(true);
    };
  });
}