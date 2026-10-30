// Unit tests for the standalone console page (pkg/web/static/console/page.js):
// the automation hooks and the reconnect loop, with noVNC replaced by a fake.
// The real handshake against the demo VNC server is ui-smoke's to check.

import assert from 'node:assert/strict';
import { beforeEach, describe, test } from 'node:test';

import { retryDelay, runConsolePage } from '../static/console/page.js';

describe('retryDelay', () => {
  test('backs off to every 5 s', () => {
    assert.deepEqual([0, 1, 2, 3, 9].map(retryDelay), [1000, 2000, 4000, 5000, 5000]);
  });
});

describe('runConsolePage', () => {
  let body;
  let screen;
  let status;
  let sessions;
  let opened;
  let scheduled;
  const canvas = { id: '' };

  beforeEach(() => {
    body = { dataset: { namespace: 'corral-vms', vm: 'desk', context: '', scale: 'one', connected: 'false' } };
    screen = { dataset: {}, querySelector: (sel) => (sel === 'canvas' ? canvas : null) };
    status = { textContent: '' };
    sessions = [];
    opened = [];
    scheduled = [];
  });

  const open = async (target, vm) => {
    opened.push(vm);
    const listeners = {};
    const session = {
      scale: '',
      focused: 0,
      on: (ev, fn) => { listeners[ev] = fn; },
      emit: (ev) => listeners[ev](),
      setScale(mode) { this.scale = mode; },
      focus() { this.focused++; },
    };
    sessions.push(session);
    return session;
  };
  const schedule = (fn, ms) => scheduled.push({ fn, ms });

  test('connect sets the hooks, the 1:1 scale and the focus', async () => {
    await runConsolePage({ body, screen, status, open, schedule });
    assert.deepEqual(opened, [{ namespace: 'corral-vms', name: 'desk', context: '' }]);
    assert.equal(sessions[0].scale, 'one');
    assert.equal(canvas.id, 'vnc-canvas');
    sessions[0].emit('connect');
    assert.equal(body.dataset.connected, 'true');
    assert.equal(screen.dataset.connected, 'true');
    assert.equal(sessions[0].focused, 1);
  });

  test('scale=fit and the context reach the session', async () => {
    body.dataset.scale = 'fit';
    body.dataset.context = 'lab';
    await runConsolePage({ body, screen, status, open, schedule });
    assert.equal(sessions[0].scale, 'fit');
    assert.equal(opened[0].context, 'lab');
  });

  test('a disconnect reconnects with backoff, and a connect resets it', async () => {
    await runConsolePage({ body, screen, status, open, schedule });
    sessions[0].emit('connect');
    sessions[0].emit('disconnect');
    sessions[0].emit('disconnect'); // a second event from the same session is ignored
    assert.equal(body.dataset.connected, 'false');
    assert.equal(screen.dataset.connected, 'false');
    assert.deepEqual(scheduled.map((s) => s.ms), [1000]);

    await scheduled[0].fn();
    sessions[1].emit('disconnect');
    assert.deepEqual(scheduled.map((s) => s.ms), [1000, 2000]);

    await scheduled[1].fn();
    sessions[2].emit('connect');
    sessions[2].emit('disconnect');
    assert.deepEqual(scheduled.map((s) => s.ms), [1000, 2000, 1000]);
    assert.equal(sessions.length, 3);
  });

  test('a failed open is retried', async () => {
    let fail = true;
    const flaky = async (target, vm) => {
      if (fail) { fail = false; throw new Error('boom'); }
      return open(target, vm);
    };
    await runConsolePage({ body, screen, status, open: flaky, schedule });
    assert.match(status.textContent, /VNC failed: boom/);
    assert.equal(scheduled.length, 1);
    await scheduled[0].fn();
    assert.equal(sessions.length, 1);
  });
});
