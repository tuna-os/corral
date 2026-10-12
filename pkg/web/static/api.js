// API transport and inventory keys: the fetch wrapper and the helpers that
// turn a VM or container into its API URL and its canonical selection key.

import { state } from './state.js';

// ── API ───────────────────────────────────────────────────────────

// Every request goes through here, so every error message an operator reads
// is shaped here. Two things this has to get right, both taken from how
// Proxmox's getResponseErrorMessage() reads a failure:
//
// A request that never reached the server is not the same as one the server
// refused, and the operator does something different about each. `fetch`
// rejects only in the first case, and its own message is "Failed to fetch",
// which says nothing about corral being down.
//
// And the message must never come out empty. `statusText` is the reason
// phrase, and HTTP/2 has no reason phrase at all: it is always '' behind any
// proxy speaking h2, which is most real deployments. A thrown Error with an
// empty message surfaces as a blank toast or a blank offline screen.
export async function api(path, opts = {}) {
  let r;
  try {
    r = await fetch(path, opts);
  } catch {
    throw new Error('Cannot reach the server — check that corral is still running');
  }
  if (!r.ok) {
    let msg = '';
    // The server's own words first: it knows why it said no.
    try {
      const body = await r.json();
      msg = body.error || body.message || '';
    } catch { /* not json — fall back to the status */ }
    if (!msg) msg = r.statusText;
    // The code always, because 403 and 500 need different things done, and
    // because it is the only thing left when there is no reason phrase.
    throw new Error(msg ? `${msg} (${r.status})` : `Request failed with status ${r.status}`);
  }
  return r.headers.get('content-type')?.includes('json') ? r.json() : r.text();
}

export const vmKey = (vm) => `${vm.peer || ''}/${vm.context || ''}/${vm.namespace}/${vm.name}`;
export const vmURL = (vm, suffix = '') => `/api/vms/${vm.namespace}/${vm.name}${suffix}${vm.context ? `?context=${encodeURIComponent(vm.context)}` : ''}`;
export const findVM = (key) => state.vms.find((v) => vmKey(v) === key);
export const ctKey = (c) => `${c.namespace}/${c.name}`;
export const findCT = (key) => state.cts.find((c) => ctKey(c) === key);