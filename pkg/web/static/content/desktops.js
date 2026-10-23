// Desktops: VDI self-service (#227–#229). A user claims a desktop from a
// pool, connects to it, and releases it. Connect asks the broker which
// console to open (RDP, VNC, or the terminal) and opens that tab of the
// guest; the broker checks that the caller holds the desktop.
//
// The screen exists only when corral web runs the broker
// (CORRAL_VDI_BROKER). It is a capability: it holds its own data and
// registers its screen, tree row and palette entry with ui/capabilities.js.

import { api } from '../api.js';
import { refresh, select } from '../app.js';
import { icon } from '../icons.js';
import { desktopMenuItems, desktopPoolMenuItems } from '../menus.js';
import { state } from '../state.js';
import { treeRow } from '../tree.js';
import { registerCapability } from '../ui/capabilities.js';
import { $, esc, toast } from '../ui/dom.js';
import { attachContextMenu } from '../ui/menu.js';
import { consoleSelection, sessionText, vdiClaimURL, vdiDesktopURL, vdiDesktopsURL, vdiPoolsURL } from '../vdi.js';

// The caller's desktops, from the last poll. Null while the broker is off.
let desktops = null;
// The desktop a link asked to connect to, once it has been tried, so a
// redraw does not open its console again.
let linkTried = '';

async function connect(id) {
  let target;
  try { target = await api(vdiDesktopURL(id, 'connect')); }
  catch (e) { toast(e.message); return; }
  const where = consoleSelection(target, state.vms, state.cts);
  if (!where) { toast(`${id} is not in the inventory yet. Try again in a few seconds.`); return; }
  if (where.ctTab) state.ctTab = where.ctTab;
  select(where.selected, where.tab);
  if (target.skipped?.length) toast(`Opened ${target.protocol}. Skipped ${target.skipped.join('; ')}`);
}

async function post(url, done) {
  try {
    const result = await api(url, { method: 'POST' });
    done(result);
  } catch (e) {
    toast(e.message);
  }
  refresh(true);
}

function desktopRows(list) {
  if (!list.length) return '<p class="muted">You have no desktop. Get one from a pool below.</p>';
  return `<table><thead><tr><th>Desktop</th><th>Pool</th><th>State</th><th>Session</th><th></th></tr></thead><tbody>
    ${list.map((d) => `<tr>
      <td><strong>${esc(d.member)}</strong>${state.me.login && d.owner !== state.me.login ? ` <span class="muted">${esc(d.owner)}</span>` : ''}</td>
      <td>${esc(d.pool)} <span class="muted">${esc(d.kind)}</span></td>
      <td>${esc(d.phase)}${d.startError ? ` <span class="doc-broken" title="${esc(d.startError)}">start failed</span>` : ''}</td>
      <td class="muted">${esc(sessionText(d))}</td>
      <td style="white-space:nowrap">
        ${d.running ? `<button class="btn sm primary" data-vdi-connect="${esc(d.id)}">${icon('desktop')} Connect</button>`
    : `<button class="btn sm" data-vdi-start="${esc(d.id)}">${icon('play')} Start</button>`}
        <button class="btn sm" data-vdi-release="${esc(d.id)}">Release</button>
      </td>
    </tr>`).join('')}
  </tbody></table>`;
}

function poolRows(pools) {
  if (!pools.length) return '<p class="muted">The broker serves no pools.</p>';
  return `<table><thead><tr><th>Pool</th><th>Kind</th><th>Mode</th><th>Free</th><th></th></tr></thead><tbody>
    ${pools.map((p) => `<tr>
      <td><strong>${esc(p.name)}</strong> <span class="muted">${esc(p.namespace)}</span></td>
      <td>${esc(p.kind)}</td>
      <td>${esc(p.mode)}</td>
      <td>${p.free} of ${p.members}</td>
      <td><button class="btn sm" data-vdi-claim="${esc(p.id)}" ${p.free ? '' : 'disabled title="No free desktop"'}>${icon('plus')} Get a desktop</button></td>
    </tr>`).join('')}
  </tbody></table>`;
}

function bind(main, list = [], pools = []) {
  main.querySelectorAll('[data-vdi-connect]').forEach((b) => { b.onclick = () => connect(b.dataset.vdiConnect); });
  const startDesktop = (id) => post(vdiDesktopURL(id, 'start'), (d) => toast(d.startError || `Starting ${d.member}`));
  const releaseDesktop = (id) => {
    if (!confirm(`Release ${id}? The desktop stops, and an ephemeral desktop loses its changes.`)) return;
    post(vdiDesktopURL(id, 'release'), () => toast(`Releasing ${id}`));
  };
  const claimPool = (id) => post(vdiClaimURL(id), (d) => toast(d.startError || `${d.member} is yours. Select Connect when it runs.`));

  main.querySelectorAll('[data-vdi-start]').forEach((b) => {
    b.onclick = () => startDesktop(b.dataset.vdiStart);
  });
  main.querySelectorAll('[data-vdi-release]').forEach((b) => {
    b.onclick = () => releaseDesktop(b.dataset.vdiRelease);
  });
  main.querySelectorAll('[data-vdi-claim]').forEach((b) => {
    b.onclick = () => claimPool(b.dataset.vdiClaim);
  });

  const mineTable = main.querySelector('#vdi-mine table');
  if (mineTable) {
    mineTable.querySelectorAll('tbody tr').forEach((tr, idx) => {
      const d = list[idx];
      if (d) {
        attachContextMenu(tr, () => desktopMenuItems(d, {
          onConnect: connect,
          onStart: startDesktop,
          onRelease: releaseDesktop,
        }));
      }
    });
  }

  const poolsTable = main.querySelector('#vdi-pools table');
  if (poolsTable) {
    poolsTable.querySelectorAll('tbody tr').forEach((tr, idx) => {
      const p = pools[idx];
      if (p) {
        attachContextMenu(tr, () => desktopPoolMenuItems(p, { onClaim: claimPool }));
      }
    });
  }
}

async function renderDesktops(main, linked) {
  main.innerHTML = `<div class="page-head"><h1>${icon('desktop')} Desktops</h1></div>
    <p class="muted" style="margin-bottom:14px">Get a desktop from a pool and connect to it.
      Release it when you are done. A desktop with no console open is released after a grace period.</p>
    <h2 class="section">Your desktops</h2><div id="vdi-mine"><p class="muted">loading…</p></div>
    <h2 class="section">Pools</h2><div id="vdi-pools"><p class="muted">loading…</p></div>`;
  let pools;
  let mine;
  try { [pools, mine] = await Promise.all([api(vdiPoolsURL), api(vdiDesktopsURL)]); }
  catch (e) {
    $('#vdi-mine').innerHTML = `<p class="console-msg">${esc(e.message)}</p>`;
    $('#vdi-pools').innerHTML = '';
    return;
  }
  $('#vdi-mine').innerHTML = desktopRows(mine);
  $('#vdi-pools').innerHTML = poolRows(pools);
  bind(main, mine, pools);
  // A link from `corral vdi connect --broker` names the desktop to open.
  if (linked && linkTried !== linked) {
    linkTried = linked;
    connect(linked);
  }
}

registerCapability({
  name: 'vdi',

  async load() {
    try { desktops = await api(vdiDesktopsURL); }
    catch (e) {
      // 404: the broker is off. 401: there is no identity to hold a desktop.
      if (e.status === 404 || e.status === 401) { desktops = null; return; }
      throw e;
    }
  },
  clear() { desktops = null; },
  fingerprint() { return desktops; },

  screens: { desktops: renderDesktops },

  treeRows(sink) {
    if (!desktops) return;
    const selected = state.selected.type === 'desktops';
    sink.appendChild(treeRow({
      lvl: 0, icon: icon('desktop'), label: 'Desktops',
      sub: desktops.length ? `${desktops.length} yours` : 'self-service',
      key: 'vdi', sig: [desktops.length, selected], sel: selected,
      onclick: () => select({ type: 'desktops' }),
    }));
  },

  paletteEntries() {
    if (!desktops) return [];
    return [{ id: 'view:desktops', icon: 'desktop', label: 'Desktops', sub: 'go to', keywords: 'vdi desktop claim pool connect', run: () => select({ type: 'desktops' }) }];
  },
});
