// Unit tests for identity and capability discovery (pkg/web/static/session.js).

import { elements, makeElement } from './dom-stub.mjs';

import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, test } from 'node:test';

import { loadCapabilities, loadWhoami, whoamiMarkup } from '../static/session.js';
import { state } from '../static/state.js';

const realFetch = globalThis.fetch;
// Answer each path from `routes`; anything else is a 500.
function serve(routes) {
  const asked = [];
  globalThis.fetch = async (path) => {
    asked.push(path);
    if (!(path in routes)) return new Response('', { status: 500 });
    return new Response(JSON.stringify(routes[path]), { headers: { 'Content-Type': 'application/json' } });
  };
  return asked;
}
beforeEach(() => elements.clear());
afterEach(() => { globalThis.fetch = realFetch; });

describe('whoamiMarkup', () => {
  test('an admin is named, with no badge', () => {
    assert.equal(whoamiMarkup({ login: 'ana@example.com', name: 'Ana', admin: true }),
      '<span class="who-name">Ana</span>');
  });

  test('the login stands in for a missing name', () => {
    assert.equal(whoamiMarkup({ login: 'ana@example.com', admin: true }),
      '<span class="who-name">ana@example.com</span>');
  });

  test('a viewer is named and marked read-only', () => {
    assert.equal(whoamiMarkup({ login: 'bob', name: 'Bob', admin: false }),
      '<span class="who-name">Bob</span><span class="ro-badge">read-only</span>');
  });

  test('behind the gate but unidentified shows the badge alone', () => {
    assert.equal(whoamiMarkup({ login: '', admin: false, enforced: true }), '<span class="ro-badge">read-only</span>');
  });

  test('no gate and nobody to name shows nothing', () => {
    assert.equal(whoamiMarkup({ login: '', admin: true, enforced: false }), '');
  });

  test('a name is escaped', () => {
    assert.equal(whoamiMarkup({ login: 'x', name: '<img onerror=1>', admin: true }),
      '<span class="who-name">&lt;img onerror=1&gt;</span>');
  });
});

describe('loadWhoami', () => {
  test('stores the identity, draws the badge and sets read-only mode', async () => {
    const el = makeElement();
    elements.set('#whoami', el);
    serve({ '/api/whoami': { login: 'bob', name: 'Bob', admin: false, enforced: true } });
    await loadWhoami();
    assert.equal(state.me.login, 'bob');
    assert.equal(el.innerHTML, '<span class="who-name">Bob</span><span class="ro-badge">read-only</span>');
    assert.equal(document.body.classList.contains('read-only'), true);
  });

  test('an admin leaves read-only mode', async () => {
    serve({ '/api/whoami': { login: 'ana', admin: true } });
    await loadWhoami();
    assert.equal(document.body.classList.contains('read-only'), false);
  });

  test('a failed fetch keeps the single-user default', async () => {
    state.me = { login: '', name: '', admin: true, enforced: false };
    serve({});
    await loadWhoami();
    assert.equal(state.me.admin, true);
  });
});

describe('loadCapabilities', () => {
  test('caps, then the create form, then the networks', async () => {
    const order = [];
    const asked = serve({ '/api/capabilities': { bootc: true }, '/api/nads': ['br0'] });
    await loadCapabilities(async () => { order.push(`form after ${asked.length} fetch`); });
    assert.deepEqual(asked, ['/api/capabilities', '/api/nads']);
    assert.deepEqual(order, ['form after 1 fetch']);
    assert.deepEqual(state.caps, { bootc: true });
    assert.deepEqual(state.availableNADs, ['br0']);
  });

  test('without the bootc plugin the bootc source option goes', async () => {
    const opt = makeElement();
    elements.set('[name=sourceType] option[value=bootc]', opt);
    serve({ '/api/capabilities': { bootc: false }, '/api/nads': [] });
    await loadCapabilities(async () => {});
    assert.equal(opt.removed, true);
  });

  test('with the bootc plugin it stays', async () => {
    const opt = makeElement();
    elements.set('[name=sourceType] option[value=bootc]', opt);
    serve({ '/api/capabilities': { bootc: true }, '/api/nads': [] });
    await loadCapabilities(async () => {});
    assert.equal(opt.removed, undefined);
  });

  test('failures keep the caps and empty the networks', async () => {
    state.caps = { storageClass: 'x', canExpand: false, canSnapshot: false };
    state.availableNADs = ['stale'];
    serve({});
    await loadCapabilities(async () => {});
    assert.equal(state.caps.storageClass, 'x');
    assert.deepEqual(state.availableNADs, []);
  });
});
