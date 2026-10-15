// Unit tests for the screens the poll draws when it cannot show the fleet
// (pkg/web/static/content/unavailable.js).

import { elements, makeElement } from './dom-stub.mjs';

import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, test } from 'node:test';

import { offlineMarkup, renderIdentityLost, renderOffline } from '../static/content/unavailable.js';

const realFetch = globalThis.fetch;
beforeEach(() => elements.clear());
afterEach(() => { globalThis.fetch = realFetch; });

describe('offlineMarkup', () => {
  test('a kubevirt context gets the cluster guidance', () => {
    const html = offlineMarkup(['qemu', 'kubevirt'], 'dial tcp: refused');
    assert.match(html, /No cluster connected/);
    assert.match(html, /<code>dial tcp: refused<\/code>/);
    assert.match(html, /corral doctor --fix/);
  });

  test('unknown backends default to the cluster guidance', () => {
    assert.match(offlineMarkup([], 'x'), /No cluster connected/);
  });

  test('local-only backends are never told to install KubeVirt', () => {
    const html = offlineMarkup(['qemu', 'incus', 'qemu'], 'x');
    assert.match(html, /No VMs yet/);
    assert.match(html, /from the qemu, incus backend/);
    assert.doesNotMatch(html, /KubeVirt/);
  });

  test('both pages offer the retry button', () => {
    assert.match(offlineMarkup([], 'x'), /id="offline-retry"/);
    assert.match(offlineMarkup(['qemu'], 'x'), /id="offline-retry"/);
  });

  test('the message is escaped', () => {
    assert.match(offlineMarkup([], '<b>'), /<code>&lt;b&gt;<\/code>/);
  });
});

describe('renderOffline', () => {
  test('asks for the contexts and wires retry', async () => {
    const content = makeElement();
    const retry = makeElement();
    elements.set('#content', content);
    elements.set('#offline-retry', retry);
    globalThis.fetch = async () => new Response(JSON.stringify({ contexts: [{ backend: 'libvirt' }] }),
      { headers: { 'Content-Type': 'application/json' } });
    const onRetry = () => {};
    await renderOffline('boom', onRetry);
    assert.match(content.innerHTML, /from the libvirt backend/);
    assert.equal(retry.onclick, onRetry);
  });

  test('a failed contexts fetch falls back to the cluster page', async () => {
    const content = makeElement();
    elements.set('#content', content);
    globalThis.fetch = async () => { throw new TypeError('Failed to fetch'); };
    await renderOffline('boom', () => {});
    assert.match(content.innerHTML, /No cluster connected/);
  });
});

test('renderIdentityLost draws the identity page and wires retry', () => {
  const content = makeElement();
  const retry = makeElement();
  elements.set('#content', content);
  elements.set('#identity-retry', retry);
  const onRetry = () => {};
  renderIdentityLost(onRetry);
  assert.match(content.innerHTML, /id="identity-lost"/);
  assert.match(content.innerHTML, /Tailscale/);
  assert.equal(retry.onclick, onRetry);
});
