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
      emit: (ev) => listeners[ev]?.(),
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

  test('readiness POST is sent only on actual RFB connect, once, to the intended same-origin VM', async () => {
    body.dataset.readyNonce = 'listener-nonce';
    body.dataset.context = 'lab & east';
    const requests = [];
    const request = async (path, options) => { requests.push({ path, options }); };
    await runConsolePage({ body, screen, status, open, schedule, request });
    assert.equal(requests.length, 0, 'opening a session is not readiness');
    sessions[0].emit('connect');
    await Promise.resolve();
    assert.equal(requests.length, 1);
    assert.equal(requests[0].path, '/api/console-ready/corral-vms/desk');
    assert.deepEqual(requests[0].options, {
      method: 'POST',
      mode: 'same-origin',
      credentials: 'same-origin',
      redirect: 'error',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ nonce: 'listener-nonce', context: 'lab & east' }),
    });
    sessions[0].emit('disconnect');
    await scheduled[0].fn();
    sessions[1].emit('connect');
    await Promise.resolve();
    assert.equal(requests.length, 1, 'reconnect must not send another readiness event');
  });

  test('without an owned nonce even a successful connection sends no readiness request', async () => {
    const requests = [];
    const request = async (...args) => { requests.push(args); };
    await runConsolePage({ body, screen, status, open, schedule, request });
    sessions[0].emit('connect');
    await Promise.resolve();
    assert.deepEqual(requests, []);
  });

  test('disconnect and a late connect from an ended session cannot report readiness', async () => {
    body.dataset.readyNonce = 'listener-nonce';
    const requests = [];
    const request = async (...args) => { requests.push(args); };
    await runConsolePage({ body, screen, status, open, schedule, request });
    sessions[0].emit('securityfailure');
    await Promise.resolve();
    assert.deepEqual(requests, []);
    sessions[0].emit('disconnect');
    sessions[0].emit('connect');
    await Promise.resolve();
    assert.deepEqual(requests, []);
    assert.equal(body.dataset.connected, 'false');
  });

  test('failed session opening cannot report readiness', async () => {
    body.dataset.readyNonce = 'listener-nonce';
    const requests = [];
    const request = async (...args) => { requests.push(args); };
    const fail = async () => { throw new Error('RFB handshake failed'); };
    await runConsolePage({ body, screen, status, open: fail, schedule, request });
    await Promise.resolve();
    assert.deepEqual(requests, []);
    assert.equal(body.dataset.connected, 'false');
  });

  test('failed readiness delivery neither changes RFB state nor creates a fallback notification', async () => {
    body.dataset.readyNonce = 'listener-nonce';
    let requests = 0;
    const request = async () => { requests++; throw new Error('offline'); };
    await runConsolePage({ body, screen, status, open, schedule, request });
    sessions[0].emit('connect');
    await Promise.resolve();
    await Promise.resolve();
    assert.equal(requests, 1);
    assert.equal(body.dataset.connected, 'true');
    assert.equal(scheduled.length, 0);
  });
});
