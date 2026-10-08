// Extensions screen: installed and available plugins.

import { api } from '../api.js';
import { icon } from '../icons.js';
import { $, esc, toast } from '../ui/dom.js';

export async function renderExtensions(main) {
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
      <div class="muted">${esc(p.source || '')}${p.publisher ? ` · ${esc(p.publisher)}` : ''}${p.license ? ` · ${esc(p.license)}` : ''}</div>
      ${p.supportedBackends?.length ? `<div class="muted">Backends: ${p.supportedBackends.map(esc).join(', ')}</div>` : '<div class="muted">Backends: not declared</div>'}
      ${p.permissions?.length ? `<div class="muted">Permissions: ${p.permissions.map(esc).join(', ')}</div>` : ''}
      <div class="ext-actions">
        ${p.installed
          ? `<button class="btn sm danger" data-ext-rm="${esc(p.name)}">Remove</button>`
          : (p.inStore ? `<button class="btn sm primary" data-ext-add="${esc(p.name)}" data-source="${esc(p.source || '')}" data-permissions="${esc((p.permissions || []).join(', '))}">Install</button>` : '')}
        ${p.homepage ? `<a class="btn sm" href="${esc(p.homepage)}" target="_blank" rel="noopener">Homepage</a>` : ''}
      </div>
    </div>`).join('')}</div>`;
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