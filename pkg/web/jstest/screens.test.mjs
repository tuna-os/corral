import assert from 'node:assert/strict';
import { test } from 'node:test';
import { registerScreen, screenFor } from '../static/ui/screens.js';

test('a screen registers its own dispatch without changing the router', () => {
  const calls = [];
  const render = (main, name) => { calls.push([main, name]); return 'rendered'; };
  registerScreen('test-screen', render);
  assert.equal(screenFor('test-screen')('pane', 'selection'), 'rendered');
  assert.deepEqual(calls, [['pane', 'selection']]);
  assert.equal(screenFor('unknown-screen'), undefined);
  assert.throws(() => registerScreen('test-screen', () => {}), /already registered/);
  assert.equal(screenFor('test-screen'), render);
});
