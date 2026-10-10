// Extensions screen: installed and available plugins.

import { api } from '../api.js';
import { icon } from '../icons.js';
import { $, esc, toast } from '../ui/dom.js';
import { openSection, pluginContributions } from './pluginui.js';

// What a plugin declares it adds to this UI, in words. Used for an installed
// plugin and, from its marketplace entry, for one that is not installed yet,
// so the operator sees it before agreeing to the install.
function addsLine(ui) {
  const parts = [
    ...(ui?.widgets || []).map((w) => `widget “${esc(w.title)}”`),
    ...(ui?.sections || []).map((s) => `screen “${esc(s.title)}”`),
  ];
  return parts.length ? `<div class="muted">Adds to the web UI: ${parts.join(', ')}</div>` : '';
}

// The screens and widgets installed plugins add, with a way to each. Widgets
// are not placed on any dashboard on their own, so this says where they are.
function contributionsHTML() {
  const list = pluginContributions();
  if (!list.length) return '';
  return `<section class="ext-added" aria-labelledby="ext-added-h">
    <h2 class="section" id="ext-added-h">${icon('extension')} Added by plugins</h2>
    <ul class="plain">${list.map((p) => `
      ${(p.sections || []).map((s) => `<li>${icon(s.icon || 'extension')}
        <a href="#" data-open-section="${esc(p.name)}" data-section-id="${esc(s.id)}">${esc(s.title)}</a>
        <span class="muted">screen · corral-${esc(p.name)}</span></li>`).join('')}
      ${(p.widgets || []).map((w) => `<li>${icon(w.icon || 'chart')} ${esc(w.title)}
        <span class="muted">widget · corral-${esc(p.name)} · Datacenter → Add widget</span></li>`).join('')}`).join('')}
    </ul></section>`;
}

export async function renderExtensions(main) {
  main.innerHTML = `<div class="page-head"><h1>${icon('extension')} Extensions</h1></div>
    <p class="muted" style="margin-bottom:14px">Optional plugins from the Corral marketplace.
      Installed plugins add <code>corral &lt;name&gt;</code> commands, and some add screens and dashboard widgets.</p>
    <label class="ext-filter" hidden>
      <span class="sr-only">Filter extensions</span>
      <input type="search" id="ext-filter" placeholder="Filter extensions…"
        aria-label="Filter extensions by name, description or backend" aria-controls="ext-list">
    </label>
    ${contributionsHTML()}
    <div id="ext-list"><p class="muted">loading…</p></div>
    <p class="muted ext-empty" hidden>No extension matches that filter.</p>`;
  main.querySelectorAll('[data-open-section]').forEach((a) => {
    a.onclick = (e) => { e.preventDefault(); openSection(a.dataset.openSection, a.dataset.sectionId); };
  });
  let list;
  try { list = await api('/api/plugins'); }
  catch (e) { if (main.querySelector('#ext-list')) $('#ext-list').innerHTML = `<p class="console-msg">${esc(e.message)}</p>`; return; }
  // The marketplace answers slowly when it is fetched over the network, and
  // the operator may have moved on - for example to a screen opened from the
  // list above. There is then nothing here to fill in.
  if (!main.querySelector('#ext-list')) return;
  if (!list.length) { $('#ext-list').innerHTML = `<p class="muted">No extensions available.</p>`; return; }
  $('#ext-list').innerHTML = `<div class="ext-grid">${list.map((p) => `
    <div class="ext-card" data-search="${esc([p.name, p.description, p.publisher, p.source, ...(p.supportedBackends || [])].filter(Boolean).join(' ').toLowerCase())}">
      <div class="ext-head">${icon('extension')} <strong>${esc(p.name)}</strong>
        <span class="muted">${esc(p.version || '')}</span>
        ${p.installed ? '<span class="pill on">installed</span>' : ''}</div>
      <div class="ext-desc">${esc(p.description || '')}</div>
      <div class="muted">${esc(p.source || '')}${p.publisher ? ` · ${esc(p.publisher)}` : ''}${p.license ? ` · ${esc(p.license)}` : ''}</div>
      ${p.supportedBackends?.length ? `<div class="muted">Backends: ${p.supportedBackends.map(esc).join(', ')}</div>` : '<div class="muted">Backends: not declared</div>'}
      ${p.permissions?.length ? `<div class="muted">Permissions: ${p.permissions.map(esc).join(', ')}</div>` : ''}
      ${addsLine(p.ui)}
      <div class="ext-actions">
        ${p.installed
          ? `<button class="btn sm danger" data-ext-rm="${esc(p.name)}">Remove</button>`
          : (p.inStore ? `<button class="btn sm primary" data-ext-add="${esc(p.name)}" data-source="${esc(p.source || '')}" data-permissions="${esc((p.permissions || []).join(', '))}">Install</button>` : '')}
        ${p.homepage ? `<a class="btn sm" href="${esc(p.homepage)}" target="_blank" rel="noopener">Homepage</a>` : ''}
      </div>
    </div>`).join('')}</div>`;
  // The tree and every data grid in the app filter; a marketplace list that
  // only scrolls is the odd one out, and it is the screen where you arrive
  // already knowing the name of the thing you came for. Hidden when there is
  // nothing to sift through.
  const filterBox = $('#ext-filter');
  const filterWrap = main.querySelector('.ext-filter');
  const emptyNote = main.querySelector('.ext-empty');
  if (filterWrap && list.length > 4) filterWrap.hidden = false;
  if (filterBox) {
    filterBox.oninput = () => {
      const q = filterBox.value.trim().toLowerCase();
      let shown = 0;
      main.querySelectorAll('.ext-card').forEach((card) => {
        const hit = !q || (card.dataset.search || '').includes(q);
        card.hidden = !hit;
        if (hit) shown++;
      });
      if (emptyNote) emptyNote.hidden = shown > 0;
    };
  }

  main.querySelectorAll('[data-ext-add]').forEach((b) => {
    b.onclick = async () => {
      const permissions = b.dataset.permissions;
      if (permissions && !confirm(`Install ${b.dataset.extAdd} and grant its declared permissions?\n\n${permissions}`)) return;
      b.disabled = true; b.textContent = 'Installing…';
      const source = b.dataset.source ? `?source=${encodeURIComponent(b.dataset.source)}` : '';
      try { await api(`/api/plugins/${b.dataset.extAdd}/install${source}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ acceptPermissions: !!permissions }),
      }); toast('Installed'); }
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