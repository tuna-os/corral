// Unit tests for the VDI broker adapter (pkg/web/static/vdi.js): its routes,
// where a connect target opens, and the session line.
//
//   just test-js        (node --test pkg/web/jstest/*.test.mjs)

import assert from 'node:assert/strict';
import { describe, test } from 'node:test';

import {
  consoleSelection, sessionText, vdiClaimURL, vdiDesktopsURL, vdiDesktopURL, vdiPoolsURL,
} from '../static/vdi.js';

const vm = (over = {}) => ({ namespace: 'vdi', name: 'desk-1', ...over });
const target = (protocol, desktop = {}) => ({
  protocol,
  desktop: { kind: 'vm', namespace: 'vdi', member: 'desk-1', ...desktop },
});

describe('routes', () => {
  test('name the broker routes by ID', () => {
    assert.equal(vdiPoolsURL, '/api/vdi/pools');
    assert.equal(vdiDesktopsURL, '/api/vdi/desktops');
    assert.equal(vdiClaimURL('vm/vdi/desk'), '/api/vdi/pools/vm/vdi/desk/claim');
    assert.equal(vdiDesktopURL('vm/vdi/desk-1'), '/api/vdi/desktops/vm/vdi/desk-1');
    assert.equal(vdiDesktopURL('ct/vdi/lab-1', 'connect'), '/api/vdi/desktops/ct/vdi/lab-1/connect');
  });

  test('escape each part, so an ID cannot reach another route', () => {
    assert.equal(vdiDesktopURL('vm/vdi/..%2f..', 'release'), '/api/vdi/desktops/vm/vdi/..%252f../release');
    assert.equal(vdiDesktopURL('vm/a b/x?y'), '/api/vdi/desktops/vm/a%20b/x%3Fy');
  });
});

describe('consoleSelection', () => {
  test('opens the tab of the protocol the broker chose', () => {
    for (const [protocol, tab] of [['rdp', 'rdp'], ['vnc', 'console'], ['terminal', 'terminal']]) {
      assert.deepEqual(consoleSelection(target(protocol), [vm()], []), {
        selected: { type: 'vm', key: '//vdi/desk-1' }, tab,
      });
    }
  });

  test('opens the terminal of a CT', () => {
    const ct = { namespace: 'vdi', name: 'lab-1' };
    assert.deepEqual(consoleSelection(target('terminal', { kind: 'ct', member: 'lab-1' }), [], [ct]), {
      selected: { type: 'ct', key: 'vdi/lab-1' }, ctTab: 'terminal',
    });
  });

  test('never picks a peer guest, and prefers the default context', () => {
    const vms = [vm({ peer: 'other' }), vm({ context: 'lab' }), vm()];
    assert.equal(consoleSelection(target('vnc'), vms, []).selected.key, '//vdi/desk-1');
    assert.equal(consoleSelection(target('vnc'), [vm({ context: 'lab' })], []).selected.key, '/lab/vdi/desk-1');
    assert.equal(consoleSelection(target('vnc'), [vm({ peer: 'other' })], []), null);
  });

  test('is null while the inventory does not list the guest', () => {
    assert.equal(consoleSelection(target('rdp'), [vm({ name: 'desk-2' })], []), null);
    assert.equal(consoleSelection(target('terminal', { kind: 'ct', member: 'lab-1' }), [], []), null);
  });
});

describe('sessionText', () => {
  const now = Date.parse('2026-10-15T09:00:00Z');
  test('says connected, and the end of a maximum duration', () => {
    assert.equal(sessionText({ connected: true }, now), 'connected');
    assert.equal(sessionText({ connected: true, reclaimReason: 'max-duration', reclaimAt: '2026-10-15T09:30:00Z' }, now),
      'connected · ends in 30 min');
  });

  test('says when a disconnected desktop is released', () => {
    assert.equal(sessionText({ connected: false, reclaimReason: 'disconnect-grace', reclaimAt: '2026-10-15T09:15:00Z' }, now),
      'disconnected · released in 15 min unless you reconnect');
    assert.equal(sessionText({ connected: false, reclaimReason: 'disconnect-grace', reclaimAt: '2026-10-15T08:59:00Z' }, now),
      'disconnected · released now unless you reconnect');
  });

  test('says not connected when no console ever opened', () => {
    assert.equal(sessionText({ connected: false }, now), 'not connected');
  });
});
