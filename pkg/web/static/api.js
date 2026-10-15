// The API adapter: the one place the web client talks to the server. The
// fetch wrapper and its error messages, JSON requests, and the helpers that
// turn a VM or container into its API URL, its console WebSocket URL and its
// canonical selection key. Unit tests: pkg/web/jstest/api.test.mjs.

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
    const err = new Error(msg ? `${msg} (${r.status})` : `Request failed with status ${r.status}`);
    // Kept as a field too, so a caller can act on the status rather than
    // parse it back out of the message.
    err.status = r.status;
    throw err;
  }
  return r.headers.get('content-type')?.includes('json') ? r.json() : r.text();
}

// sendJSON sends `body` as a JSON document, POST unless told otherwise. Every
// JSON write the client makes goes through here, so the request shape is
// defined once. (The disk upload streams raw bytes and calls api() itself.)
export const sendJSON = (path, body, method = 'POST') => api(path, {
  method,
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
});

export const vmKey = (vm) => `${vm.peer || ''}/${vm.context || ''}/${vm.namespace}/${vm.name}`;
export const vmURL = (vm, suffix = '') => `/api/vms/${vm.namespace}/${vm.name}${suffix}${vm.context ? `?context=${encodeURIComponent(vm.context)}` : ''}`;
export const findVM = (key) => state.vms.find((v) => vmKey(v) === key);
export const ctKey = (c) => `${c.namespace}/${c.name}`;
export const ctURL = (c, suffix = '') => `/api/cts/${c.namespace}/${c.name}${suffix}`;
export const findCT = (key) => state.cts.find((c) => ctKey(c) === key);

// ── Console WebSockets ────────────────────────────────────────────

// The socket is on the page's own host, wss: when the page came over https.
const wsBase = () => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}`;

// The VNC and serial consoles, scoped to the VM's context like vmURL.
export const wsURL = (kind, vm) => {
  const base = `${wsBase()}/api/${kind}/${vm.namespace}/${vm.name}`;
  return base + (vm.context ? `?context=${encodeURIComponent(vm.context)}` : '');
};

// The RDCleanPath proxy IronRDP connects through (ADR-0002).
export const rdpURL = (vm) => `${wsBase()}/api/rdp/${vm.namespace}/${vm.name}?rdcleanpath=1`;