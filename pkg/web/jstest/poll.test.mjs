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

describe('PollingState', () => {
  test('isPending is true while poll is executing and false when done', async () => {
    let finish;
    const blocker = new Promise((resolve) => { finish = resolve; });
    const poller = new PollingState({
      fetchData: async () => blocker,
    });

    assert.equal(poller.isPending(), false);
    const p = poller.poll();
    assert.equal(poller.isPending(), true);
    finish();
    await p;
    assert.equal(poller.isPending(), false);
  });

  test('deduplicates renders when fingerprint is unchanged', async () => {
    let renders = 0;
    let dataVersion = 1;
    const poller = new PollingState({
      fetchData: async () => {},
      fingerprint: () => `v${dataVersion}`,
      render: () => { renders++; },
    });

    await poller.poll();
    assert.equal(renders, 1);

    await poller.poll();
    assert.equal(renders, 1, 'unchanged fingerprint does not re-render');

    dataVersion = 2;
    await poller.poll();
    assert.equal(renders, 2, 'changed fingerprint re-renders');

    await poller.poll(true);
    assert.equal(renders, 3, 'force=true re-renders even if fingerprint is identical');
  });

  test('defers render when gesture is in flight and flushes onSettled', async () => {
    let inGesture = false;
    let renders = 0;
    let dataVersion = 1;
    const poller = new PollingState({
      fingerprint: () => `v${dataVersion}`,
      render: () => { renders++; },
      shouldDefer: () => inGesture,
    });

    await poller.poll();
    assert.equal(renders, 1);

    dataVersion = 2;
    inGesture = true;
    await poller.poll();
    assert.equal(renders, 1, 'render was deferred due to gesture');
    assert.equal(poller.renderDeferred, true);

    inGesture = false;
    await poller.onSettled();
    assert.equal(renders, 2, 'onSettled flushed deferred render');
    assert.equal(poller.renderDeferred, false);
  });

  test('skips render when shouldSkip returns true (e.g. open menu)', async () => {
    let openMenu = false;
    let renders = 0;
    let dataVersion = 1;
    const poller = new PollingState({
      fingerprint: () => `v${dataVersion}`,
      render: () => { renders++; },
      shouldSkip: () => openMenu,
    });

    await poller.poll();
    assert.equal(renders, 1);

    dataVersion = 2;
    openMenu = true;
    await poller.poll();
    assert.equal(renders, 1, 'render skipped due to menu');

    openMenu = false;
    await poller.poll();
    assert.equal(renders, 2, 'renders once menu closes');
  });

  test('handles 401 identityLost and forces render on recovery', async () => {
    let identityCalls = 0;
    let renders = 0;
    let failWith401 = true;
    const poller = new PollingState({
      fetchData: async () => {
        if (failWith401) {
          const err = new Error('Unauthorized');
          err.status = 401;
          throw err;
        }
      },
      onIdentityLost: () => { identityCalls++; },
      render: () => { renders++; },
    });

    await poller.poll();
    assert.equal(identityCalls, 1);
    assert.equal(poller.identityLost, true);

    await poller.poll();
    assert.equal(identityCalls, 1, 'identity lost callback fired only once');

    failWith401 = false;
    await poller.poll();
    assert.equal(poller.identityLost, false);
    assert.equal(renders, 1, 'recovering from identity lost forces render');
  });

  test('initial failure invokes onOffline; subsequent failures invoke onError with dedup', async () => {
    let offlineCalls = [];
    let errorCalls = [];
    let failMessage = 'initial connection refused';

    const poller = new PollingState({
      fetchData: async () => {
        if (failMessage) throw new Error(failMessage);
      },
      fingerprint: () => 'data-fp',
      onOffline: (err) => { offlineCalls.push(err.message); },
      onError: (err) => { errorCalls.push(err.message); },
    });

    await poller.poll();
    assert.deepEqual(offlineCalls, ['initial connection refused']);
    assert.equal(poller.offlineShown, true);

    await poller.poll();
    assert.equal(offlineCalls.length, 1, 'offline screen not shown repeatedly');

    // Server comes back
    failMessage = '';
    await poller.poll();
    assert.equal(poller.offlineShown, false);
    assert.equal(poller.lastError, null);

    // Now a failure happens after we have had successful renders
    failMessage = 'temporary glitch';
    await poller.poll();
    assert.deepEqual(errorCalls, ['temporary glitch']);

    // Same failure again: toast should be deduplicated
    await poller.poll();
    assert.deepEqual(errorCalls, ['temporary glitch'], 'deduplicated repeated error toast');

    // Different failure: should notify
    failMessage = 'network timeout';
    await poller.poll();
    assert.deepEqual(errorCalls, ['temporary glitch', 'network timeout']);
  });
});

