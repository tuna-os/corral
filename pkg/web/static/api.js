// API transport and inventory keys: the fetch wrapper and the helpers that
// turn a VM or container into its API URL and its canonical selection key.

import { state } from './state.js';

// ── API ───────────────────────────────────────────────────────────

export async function api(path, opts = {}) {
  const r = await fetch(path, opts);
  if (!r.ok) {
    let msg = r.statusText;
    try { msg = (await r.json()).error || msg; } catch { /* not json */ }
    throw new Error(msg);
  }
  return r.headers.get('content-type')?.includes('json') ? r.json() : r.text();
}

export const vmKey = (vm) => `${vm.peer || ''}/${vm.context || ''}/${vm.namespace}/${vm.name}`;
export const vmURL = (vm, suffix = '') => `/api/vms/${vm.namespace}/${vm.name}${suffix}${vm.context ? `?context=${encodeURIComponent(vm.context)}` : ''}`;
export const findVM = (key) => state.vms.find((v) => vmKey(v) === key);
export const ctKey = (c) => `${c.namespace}/${c.name}`;
export const findCT = (key) => state.cts.find((c) => ctKey(c) === key);