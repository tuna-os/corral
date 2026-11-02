// Unit tests for the fleet poll's schedule (pkg/web/static/poll.js).

import assert from 'node:assert/strict';
import { describe, test } from 'node:test';

import { PollingState, singleFlight, startPolling } from '../static/poll.js';

// A run whose calls stay pending until the test releases them, in order.
function gatedRun() {
  const calls = [];
  const gates = [];
  const run = (force) => {
    calls.push(force);
    return new Promise((resolve) => gates.push(resolve));
  };
  const release = async () => {
    gates.shift()();
    // Let the awaiting loop take its next step.
    await new Promise((r) => setImmediate(r));
  };
  return { run, calls, release };
}

describe('singleFlight', () => {
  test('passes force through to a lone call', async () => {
    const calls = [];
    const refresh = singleFlight(async (force) => { calls.push(force); });
    await refresh();
    await refresh(true);
    assert.deepEqual(calls, [false, true]);
  });

  test('a call while one runs waits for it, then runs once', async () => {
    const g = gatedRun();
    const refresh = singleFlight(g.run);
    const first = refresh();
    refresh();
    refresh();
    refresh();
    assert.deepEqual(g.calls, [false], 'only one run in flight');
    await g.release();
    assert.deepEqual(g.calls, [false, false], 'the three waiting calls collapse into one rerun');
    await g.release();
    await first;
    assert.deepEqual(g.calls, [false, false]);
  });

  test('a forced call made meanwhile keeps its force', async () => {
    const g = gatedRun();
    const refresh = singleFlight(g.run);
    const first = refresh();
    refresh(true);
    refresh(false);
    await g.release();
    assert.deepEqual(g.calls, [false, true]);
    await g.release();
    await first;
  });

  test('calls made during the rerun get one more run', async () => {
    const g = gatedRun();
    const refresh = singleFlight(g.run);
    const first = refresh();
    refresh();
    await g.release();
    refresh(true);
    await g.release();
    assert.deepEqual(g.calls, [false, false, true]);
    await g.release();
    await first;
  });

  test('only the first caller waits for the reruns; the others return at once', async () => {
    const g = gatedRun();
    const refresh = singleFlight(g.run);
    const first = refresh();
    let secondDone = false;
    refresh().then(() => { secondDone = true; });
    await new Promise((r) => setImmediate(r));
    assert.equal(secondDone, true);
    await g.release();
    await g.release();
    await first;
  });

  test('a throwing run releases the guard', async () => {
    let n = 0;
    const refresh = singleFlight(async () => { n++; if (n === 1) throw new Error('boom'); });
    await assert.rejects(refresh(), /boom/);
    await refresh();
    assert.equal(n, 2);
  });

  test('a throwing run drops the rerun that was waiting', async () => {
    const calls = [];
    let fail;
    const refresh = singleFlight((force) => {
      calls.push(force);
      return new Promise((_, reject) => { fail = reject; });
    });
    const first = refresh();
    refresh(true);
    fail(new Error('boom'));
    await assert.rejects(first, /boom/);
    assert.deepEqual(calls, [false]);
  });
});

describe('startPolling', () => {
  // A clock the test drives: schedule() records, now() reads `time`.
  function fakeClock() {
    const clock = { time: 0, scheduled: [] };
    clock.now = () => clock.time;
    clock.schedule = (fn, ms) => { clock.scheduled.push({ fn, ms }); };
    clock.next = () => clock.scheduled.shift();
    return clock;
  }

  test('the first poll is one interval after start', () => {
    const clock = fakeClock();
    let calls = 0;
    startPolling(async () => { calls++; }, { interval: 5000, now: clock.now, schedule: clock.schedule });
    assert.equal(calls, 0);
    assert.equal(clock.scheduled.length, 1);
    assert.equal(clock.scheduled[0].ms, 5000);
  });

  test('the next poll waits the interval plus twice the last poll', async () => {
    const clock = fakeClock();
    let calls = 0;
    startPolling(async () => { calls++; clock.time += 1500; }, { interval: 5000, now: clock.now, schedule: clock.schedule });
    await clock.next().fn();
    assert.equal(calls, 1);
    assert.equal(clock.scheduled.length, 1);
    assert.equal(clock.scheduled[0].ms, 5000 + 3000);
  });

  test('a fast poll keeps the interval', async () => {
    const clock = fakeClock();
    startPolling(async () => {}, { interval: 5000, now: clock.now, schedule: clock.schedule });
    await clock.next().fn();
    assert.equal(clock.next().ms, 5000);
  });

  test('a hidden page fetches nothing but keeps its cadence', async () => {
    const clock = fakeClock();
    let calls = 0;
    let hidden = true;
    startPolling(async () => { calls++; }, {
      interval: 5000, hidden: () => hidden, now: clock.now, schedule: clock.schedule,
    });
    await clock.next().fn();
    assert.equal(calls, 0);
    const again = clock.next();
    assert.equal(again.ms, 5000);
    hidden = false;
    await again.fn();
    assert.equal(calls, 1);
  });

  test('a throwing poll still schedules the next one', async () => {
    const clock = fakeClock();
    startPolling(async () => { clock.time += 10; throw new Error('render failed'); }, {
      interval: 5000, now: clock.now, schedule: clock.schedule,
    });
    await assert.rejects(clock.next().fn(), /render failed/);
    assert.equal(clock.next().ms, 5020);
  });
});

// The policy can be tested without importing a screen or installing a DOM.
describe('PollingState', () => {
  test('deduplicates both poll renders and renders performed by navigation', () => {
    const policy = new PollingState();
    assert.equal(policy.shouldRender('fleet-1'), true);
    assert.equal(policy.shouldRender('fleet-1'), false);
    policy.markRendered('selected-vm');
    assert.equal(policy.shouldRender('selected-vm'), false);
    assert.equal(policy.shouldRender('fleet-2'), true);
  });

  test('a gesture leaves the fingerprint stale until the deferred refresh renders', () => {
    const policy = new PollingState();
    policy.markRendered('old');
    assert.equal(policy.shouldRender('new', { interacting: true }), false);
    assert.equal(policy.shouldRender('newer', { interacting: true }), false);
    assert.equal(policy.lastRenderFp, 'old');
    assert.equal(policy.takeDeferred(), true);
    assert.equal(policy.takeDeferred(), false);
    assert.equal(policy.shouldRender('newer'), true);
  });

  test('a context menu holds the change for the next poll without scheduling a gesture refresh', () => {
    const policy = new PollingState();
    policy.markRendered('old');
    assert.equal(policy.shouldRender('new', { menu: true, interacting: true }), false);
    assert.equal(policy.takeDeferred(), false);
    assert.equal(policy.lastRenderFp, 'old');
    assert.equal(policy.shouldRender('new'), true);
  });

  test('unchanged data during a gesture needs no deferred render', () => {
    const policy = new PollingState();
    policy.markRendered('same');
    assert.equal(policy.shouldRender('same', { interacting: true }), false);
    assert.equal(policy.takeDeferred(), false);
  });

  test('forced refresh bypasses deduplication, menus and gestures', () => {
    const policy = new PollingState();
    policy.markRendered('same');
    assert.equal(policy.shouldRender('same', { force: true, menu: true, interacting: true }), true);
    assert.equal(policy.takeDeferred(), false);
  });
});

describe('PollingState failure recovery', () => {
  test('initial offline guidance is shown once and leaves static screens usable', () => {
    const policy = new PollingState();
    const error = new Error('unreachable');
    assert.equal(policy.failed(error), 'offline');
    assert.equal(policy.failed(error), null);
    policy.markRendered('extensions');
    assert.equal(policy.failed(error), null);
    assert.equal(policy.failed(new Error('another failure')), 'toast');
    assert.equal(policy.failed(new Error('another failure')), null);
    assert.equal(policy.recovered(), false);
    assert.equal(policy.lastError, '');
    assert.equal(policy.failed(error), 'toast');
  });

  test('identity loss is reported once and recovery forces an unchanged fleet to render', () => {
    const policy = new PollingState();
    policy.markRendered('fleet');
    assert.equal(policy.failed({ status: 401 }), 'identity');
    assert.equal(policy.failed({ status: 401 }), null);
    assert.equal(policy.identityLost, true);
    assert.equal(policy.shouldRender('fleet', { force: policy.recovered() }), true);
    assert.equal(policy.identityLost, false);
    assert.equal(policy.recovered(), false);
    assert.equal(policy.failed({ status: 401 }), 'identity');
  });
});
