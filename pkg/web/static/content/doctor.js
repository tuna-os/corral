// Doctor screen: cluster and local readiness checks.

import { api } from '../api.js';
import { icon } from '../icons.js';
import { $, esc, toast } from '../ui/dom.js';

export async function renderDoctor(main) {
  main.innerHTML = `<div class="page-head"><h1>${icon('health')} Cluster health</h1>
    <button class="btn primary" id="doc-fix" hidden>Reconcile fixable</button></div>
    <p class="muted" style="margin-bottom:14px">What Corral's features need from the cluster.
      Fixable items are safe, config-only changes Corral can apply.</p>
    <div id="doc-list"><p class="muted">checking…</p></div>`;
  let checks;
  try { checks = await api('/api/doctor'); }
  catch (e) { $('#doc-list').innerHTML = `<p class="console-msg">${esc(e.message)}</p>`; return; }
  const fixBtn = $('#doc-fix');
  fixBtn.hidden = !checks.some((c) => !c.ok && c.fixable);
  fixBtn.onclick = async () => {
    fixBtn.disabled = true; fixBtn.textContent = 'Reconciling…';
    try { await api('/api/doctor/fix', { method: 'POST' }); toast('Reconciled'); }
    catch (e) { toast(e.message); }
    renderDoctor(main);
  };
  $('#doc-list').innerHTML = `<table><tbody>
    ${checks.map((c) => `<tr>
      <td style="width:1.5rem">${c.ok ? '<span class="dot on"></span>' : '<span class="dot off"></span>'}</td>
      <td><strong ${c.ok ? '' : 'class="doc-broken"'}>${esc(c.name)}</strong></td>
      <td class="${c.ok ? 'muted' : 'doc-broken'}">${esc(c.detail)}</td>
      <td>${!c.ok && c.fixable ? `<button class="btn sm" data-fix="${esc(c.name)}">Fix</button>` : ''}</td>
    </tr>`).join('')}
  </tbody></table>`;
  // Per-check fix buttons — scoped reconcile of just that item.
  $('#doc-list').querySelectorAll('[data-fix]').forEach((b) => {
    b.onclick = async () => {
      b.disabled = true; b.textContent = 'Fixing…';
      try {
        await api('/api/doctor/fix', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ check: b.dataset.fix }),
        });
        toast(`Fixed: ${b.dataset.fix}`);
      } catch (e) { toast(e.message); }
      renderDoctor(main);
    };
  });
}