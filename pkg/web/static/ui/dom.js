// DOM helpers every module shares: query, toast, HTML escaping.

export const $ = (sel) => document.querySelector(sel);

// Toasts are the only report some actions make: "Started web-prod", "Refresh
// failed", "Workspace layout reset". They were a bare div, so a screen reader
// never heard any of them — the action simply appeared to do nothing.
//
// The region is created once and kept. A live region has to exist before its
// text changes for the change to be announced: an element inserted with
// aria-live and its message already in it is usually read as ordinary content,
// which is exactly the mistake this avoids.
// Built at load, not on the first toast. Creating the region and putting the
// message in it in the same tick is the failure this is avoiding: assistive
// technology watches an existing region for changes, and one that arrives with
// its text already present is usually read as ordinary content, if at all.
// This module is imported by app.js, which is a module and therefore deferred,
// so the body is parsed by the time this runs.
const toastRegion = (() => {
  const region = document.createElement('div');
  region.id = 'toast-region';
  // polite, not assertive: a toast reports what happened, and should wait for
  // the reader to finish its sentence rather than cut across it.
  region.setAttribute('role', 'status');
  region.setAttribute('aria-live', 'polite');
  document.body.appendChild(region);
  return region;
})();

// Failures you have to read.
//
// A toast is right for "Stop: 8 ok" and wrong for "2 failed": it disappears,
// it has no room for a reason, and the reason is the whole point. An operator
// whose bulk stop left two guests running needs to know which two and why -
// a lock, a missing disk, a node that went away - because each needs a
// different thing done next.
//
// Proxmox answers this with a task log window per bulk action, since its
// bulk endpoints run server-side and return a task. corral fans out from the
// browser, so the equivalent is this: one dialog, every failure named with
// what the API said about it.
export function reportFailures(title, failures) {
  if (!failures.length) return;
  const dlg = document.createElement('dialog');
  dlg.className = 'failure-report';
  dlg.innerHTML = `
    <h2>${esc(title)}</h2>
    <table><tbody>${failures.map(({ name, error }) =>
      `<tr><td>${esc(name)}</td><td class="muted">${esc(error || 'failed')}</td></tr>`).join('')}</tbody></table>
    <div class="dialog-actions"><button type="button" class="btn">Close</button></div>`;
  document.body.appendChild(dlg);
  const close = () => { if (dlg.open) dlg.close(); dlg.remove(); };
  dlg.querySelector('button').onclick = close;
  dlg.addEventListener('cancel', close);
  dlg.addEventListener('click', (e) => { if (e.target === dlg) close(); });
  dlg.showModal();
}

export function toast(msg) {
  const region = toastRegion;
  region.querySelectorAll('.toast').forEach((t) => t.remove());
  const el = document.createElement('div');
  el.className = 'toast';
  el.textContent = msg;
  region.appendChild(el);
  setTimeout(() => el.remove(), 6000);
}

export const esc = (s) => String(s ?? '').replace(/[&<>"']/g,
  (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));