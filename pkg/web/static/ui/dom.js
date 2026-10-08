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