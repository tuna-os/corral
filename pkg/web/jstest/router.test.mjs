// Unit tests for the content router and screen dispatch (pkg/web/static/router.js).
//
//   just test-js        (node --test pkg/web/jstest/*.test.mjs)

import './dom-stub.mjs';

import assert from 'node:assert/strict';
import { beforeEach, describe, test } from 'node:test';

import { Router } from '../static/router.js';

describe('Router', () => {
  let targetEl;
  let router;
  let appState;

  beforeEach(() => {
    targetEl = { innerHTML: '', querySelector: () => null };
    appState = {
      selected: { type: 'dc' },
      tab: 'summary',
      nodes: [{ name: 'node-1' }, { name: 'node-2' }],
    };
    router = new Router({
      target: () => targetEl,
      defaultScreen: 'dc',
      appState,
      onRenderTree: () => {},
    });
  });

  test('registers and dispatches screens by selection type', () => {
    let dcRendered = false;
    let nodeRenderedWith = null;

    router.registerScreen('dc', (main) => {
      dcRendered = true;
      main.innerHTML = '<h1>Datacenter</h1>';
    });

    router.registerScreen('node', (main, sel) => {
      nodeRenderedWith = sel.name;
      main.innerHTML = `<h1>Node ${sel.name}</h1>`;
    });

    router.renderContent();
    assert.equal(dcRendered, true);
    assert.equal(targetEl.innerHTML, '<h1>Datacenter</h1>');

    appState.selected = { type: 'node', name: 'node-1' };
    router.renderContent();
    assert.equal(nodeRenderedWith, 'node-1');
    assert.equal(targetEl.innerHTML, '<h1>Node node-1</h1>');
  });

  test('falls back to defaultScreen when an unregistered type is selected', () => {
    let dcRendered = false;
    router.registerScreen('dc', (main) => {
      dcRendered = true;
      main.innerHTML = '<h1>Datacenter Fallback</h1>';
    });

    appState.selected = { type: 'unknown_screen' };
    router.renderContent();
    assert.equal(dcRendered, true);
    assert.equal(targetEl.innerHTML, '<h1>Datacenter Fallback</h1>');
  });

  test('fallbackSelection prefers last parent node if it still exists', () => {
    assert.deepEqual(router.fallbackSelection(), { type: 'dc' });

    router.lastParentNode = 'node-1';
    assert.deepEqual(router.fallbackSelection(), { type: 'node', name: 'node-1' });

    // If node was removed from cluster, fall back to dc
    router.lastParentNode = 'deleted-node';
    assert.deepEqual(router.fallbackSelection(), { type: 'dc' });
  });

  test('select updates state and calls registered screen and markRendered', () => {
    let customRendered = false;
    let markRenderedCalls = 0;

    router.onMarkRendered = () => { markRenderedCalls++; };
    router.registerScreen('custom', () => { customRendered = true; });

    router.select({ type: 'custom', key: 'foo' }, 'details');
    assert.deepEqual(appState.selected, { type: 'custom', key: 'foo' });
    assert.equal(appState.tab, 'details');
    assert.equal(customRendered, true);
    assert.equal(markRenderedCalls, 1);
  });
});
