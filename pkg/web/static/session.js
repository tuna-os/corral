// Who the caller is and what this server offers, asked once at boot (#288).
// Not to be confused with ui/capabilities.js, the registry of screens and
// data that plugins contribute. Unit tests: pkg/web/jstest/session.test.mjs.

import { api } from './api.js';
import { state } from './state.js';
import { $, esc } from './ui/dom.js';

// loadWhoami fetches the caller identity and flips the UI into read-only mode
// for non-admins (the server still enforces this; the UI just hides controls).
export async function loadWhoami() {
  try { state.me = await api('/api/whoami'); } catch { return; }
  const el = $('#whoami');
  if (el) el.innerHTML = whoamiMarkup(state.me);
  document.body.classList.toggle('read-only', !state.me.admin);
}

// The header's identity badge: the caller's name, and "read-only" for a
// non-admin. Behind the gate but unidentified shows the badge alone; with no
// gate at all there is nobody to name and nothing to show.
export function whoamiMarkup(me) {
  const badge = me.admin ? '' : '<span class="ro-badge">read-only</span>';
  if (me.login) return `<span class="who-name">${esc(me.name || me.login)}</span>${badge}`;
  if (me.enforced) return badge;
  return '';
}

// loadCapabilities fetches what this server can do into state.caps, then has
// the create form fill itself (`fillCreateForm`, from create.js, which knows
// its own fields), then the networks a VM can attach to.
export async function loadCapabilities(fillCreateForm) {
  try { state.caps = await api('/api/capabilities'); } catch { /* keep defaults */ }
  // bootc/windows are optional plugins — hide their source options when absent.
  if (!state.caps.bootc) {
    document.querySelector('[name=sourceType] option[value=bootc]')?.remove();
  }
  await fillCreateForm();
  // The Windows create flow is compiled into the web server (manifest gen in
  // pkg/kubevirt), so it's always available — no plugin install needed.
  try { state.availableNADs = await api('/api/nads'); } catch { state.availableNADs = []; }
}
