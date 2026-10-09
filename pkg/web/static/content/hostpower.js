// Host power: machines that a host-power plugin can switch on and off, for
// example an on-demand cloud node kept stopped while it is idle.
//
// This module is the whole of that capability in the UI. It holds its own
// data, draws its own tree rows, widget, screen and menu entries, and
// registers them with ui/capabilities.js. Core names none of it. Adding a
// second capability means another module like this one and no edit to core.

import { api } from '../api.js';
import { refresh, select } from '../app.js';
import { icon } from '../icons.js';
import { state } from '../state.js';
import { registerCapability } from '../ui/capabilities.js';
import { esc, toast } from '../ui/dom.js';
import { attachContextMenu } from '../ui/menu.js';
import { treeRow } from '../tree.js';

// ── Data ─────────────────────────────────────────────────────────

// The capability's own state. It used to live on the shared state object,
// where every file could reach it and six of them did.
let hosts = [];

function hostList() { return hosts; }

export function hostPowerKey(h) { return `${h.plugin}/${h.id}`; }

export function hostPowerDot(hostState) {
  if (hostState === 'running') return 'on';
  if (hostState === 'stopped') return 'off';
  return 'mid'; // starting / stopping / unknown
}

// The VMs that a machine carries, which decides both the warning before a
// power-off and the alert when a machine is off with guests on it.
function guestsOn(h) {
  return h.node ? state.vms.filter((v) => v.node === h.node) : [];
}

// Every power button carries .hp-action, which the stylesheet hides in
// read-only mode. A new button here needs that class or it stays live for an
// operator who may not use it.
async function act(h, action) {
  await api(
    `/api/hostpower/${encodeURIComponent(h.plugin)}/${action}?id=${encodeURIComponent(h.id)}`,
    { method: 'POST' },
  );
  toast(`${action === 'start' ? 'Powering on' : 'Powering off'} ${h.name}`);
}

// A power-off takes the guests with it, so it asks first. Returns false when
// the operator declines.
function confirmStop(h) {
  const on = guestsOn(h);
  return !on.length || confirm(`Power off ${h.name}? ${on.length} VM(s) on it will stop.`);
}

// ── The screen ───────────────────────────────────────────────────

function renderHostPower(main, key) {
  const h = hostList().find((x) => hostPowerKey(x) === key);
  if (!h) { main.innerHTML = '<p class="muted">Host no longer reported by its plugin.</p>'; return; }
  const onNode = guestsOn(h);
  const btn = (action, label, cls) => ((h.actions || []).includes(action)
    ? `<button class="btn ${cls} hp-action" data-hp="${action}">${icon(action === 'start' ? 'play' : 'stop')} ${label}</button>` : '');
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
      if (action === 'stop' && !confirmStop(h)) return;
      b.disabled = true;
      try { await act(h, action); } catch (e) { toast(e.message); }
      refresh(true);
    };
  });
}

// ── The dashboard widget ─────────────────────────────────────────

function powerWidget(node) {
  return {
    title: 'Host power', w: 4, h: 3,
    render(body) {
      const shown = hostList().filter((h) => !node || h.node === node);
      if (!shown.length) {
        body.innerHTML = `<p class="muted">${node ? 'No host-power plugin manages this node.'
          : 'No host-power plugin is installed. Hosts show here when one is (see Extensions).'}</p>`;
        return;
      }
      body.innerHTML = `<ul class="plain">${shown.map((h) => {
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
          const h = shown.find((x) => hostPowerKey(x) === b.dataset.hpkey);
          const action = b.dataset.hp;
          if (action === 'stop' && !confirmStop(h)) return;
          b.disabled = true;
          try { await act(h, action); } catch (e) { toast(e.message); }
          refresh(true);
        };
      });
    },
  };
}

// ── Menu entries ─────────────────────────────────────────────────

// The actions for one machine, used by its own tree row and by the node whose
// machine it is.
function actionItems(h) {
  const items = [];
  if ((h.actions || []).includes('start')) {
    items.push({
      icon: 'play',
      label: 'Power on',
      mutate: true,
      disabled: h.state === 'running',
      action: async () => {
        try { await act(h, 'start'); } catch (e) { toast(e.message); }
        refresh(true);
      },
    });
  }
  if ((h.actions || []).includes('stop')) {
    items.push({
      icon: 'stop',
      label: 'Power off',
      mutate: true,
      danger: true,
      disabled: h.state === 'stopped',
      action: async () => {
        if (!confirmStop(h)) return;
        try { await act(h, 'stop'); } catch (e) { toast(e.message); }
        refresh(true);
      },
    });
  }
  return items;
}

function hostPowerMenuItems(h) {
  return [
    {
      icon: 'server',
      label: 'View host',
      action: () => select({ type: 'hostpower', key: hostPowerKey(h) }),
    },
    { separator: true },
    ...actionItems(h),
  ];
}

// ── Registration ─────────────────────────────────────────────────

registerCapability({
  name: 'host-power',

  async load() { hosts = (await api('/api/hostpower')).hosts || []; },
  clear() { hosts = []; },
  fingerprint() { return hosts; },

  screens: { hostpower: renderHostPower },

  widgets(scope) { return { power: powerWidget(scope) }; },
  // The same corner on both the Datacenter and the node screen. A saved
  // layout wins over this, so it only decides a first visit.
  layout() { return [{ id: 'power', x: 8, y: 3, w: 4, h: 3 }]; },

  treeRows(sink) {
    for (const h of hostList()) {
      const selected = state.selected.type === 'hostpower' && state.selected.key === hostPowerKey(h);
      const row = treeRow({
        lvl: 0,
        icon: icon('server'),
        label: h.name,
        sub: h.state,
        dot: hostPowerDot(h.state),
        key: `hp:${hostPowerKey(h)}`,
        sig: [h, selected],
        sel: selected,
        onclick: () => select({ type: 'hostpower', key: hostPowerKey(h) }),
      });
      attachContextMenu(row, () => hostPowerMenuItems(h));
      sink.appendChild(row);
    }
  },

  // A node's menu gains the actions of the machine that carries it, matched
  // either way round because a provider may name the machine after the node.
  menuItems(kind, subject) {
    if (kind !== 'node') return [];
    const h = hostList().find((x) => x.node === subject || x.name === subject);
    return h ? actionItems(h) : [];
  },

  alerts() {
    return hostList()
      .filter((h) => h.state === 'stopped' && guestsOn(h).length)
      .map((h) => `Host <strong>${esc(h.name)}</strong> is off with ${guestsOn(h).length} VM(s) on it`);
  },
});
