// The VDI broker adapter (#227–#229): its routes, and the one decision the
// client makes on its own: which screen opens the console that the broker
// chose. The broker decides the protocol and checks that the caller holds
// the desktop. Unit tests: pkg/web/jstest/vdi.test.mjs.

import { ctKey, vmKey } from './api.js';

// A pool or desktop ID is kind/namespace/name, and each part is a DNS
// label, so it is a valid path as it stands. Each part is escaped all the
// same, so a malformed ID cannot reach another route.
const idPath = (id) => id.split('/').map(encodeURIComponent).join('/');

export const vdiPoolsURL = '/api/vdi/pools';
export const vdiDesktopsURL = '/api/vdi/desktops';
export const vdiClaimURL = (poolId) => `/api/vdi/pools/${idPath(poolId)}/claim`;
export const vdiDesktopURL = (id, action = '') => `/api/vdi/desktops/${idPath(id)}${action ? `/${action}` : ''}`;

// The guest tab that runs each of the broker's protocols.
const TAB_FOR = { rdp: 'rdp', vnc: 'console', terminal: 'terminal' };

// consoleSelection is where the client opens a connect target: the tab of
// the guest that runs the chosen protocol. A CT has only its terminal. The
// broker's pools are on this Corral's own cluster, so a guest from a peer
// never matches, and a guest of the default context wins over a same-named
// one elsewhere. Null when the inventory does not list the guest yet.
export function consoleSelection(target, vms, cts) {
  const d = target.desktop;
  if (d.kind === 'ct') {
    const c = cts.find((x) => x.namespace === d.namespace && x.name === d.member);
    return c ? { selected: { type: 'ct', key: ctKey(c) }, ctTab: 'terminal' } : null;
  }
  const local = vms.filter((v) => !v.peer && v.namespace === d.namespace && v.name === d.member);
  const vm = local.find((v) => !v.context) || local[0];
  return vm ? { selected: { type: 'vm', key: vmKey(vm) }, tab: TAB_FOR[target.protocol] || 'console' } : null;
}

function minutesUntil(at, now) {
  const mins = Math.round((Date.parse(at) - now) / 60000);
  return mins < 1 ? 'now' : `in ${mins} min`;
}

// sessionText says in one line whether a desktop has a console open and
// when the broker reclaims it. An open console is presence, not activity,
// and the text does not claim more.
export function sessionText(d, now = Date.now()) {
  const ends = d.reclaimReason === 'max-duration' && d.reclaimAt ? ` · ends ${minutesUntil(d.reclaimAt, now)}` : '';
  if (d.connected) return `connected${ends}`;
  if (d.reclaimReason === 'disconnect-grace' && d.reclaimAt) return `disconnected · released ${minutesUntil(d.reclaimAt, now)} unless you reconnect`;
  return `not connected${ends}`;
}
