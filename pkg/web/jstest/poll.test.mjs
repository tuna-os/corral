// Unit tests for the fleet poll's schedule (pkg/web/static/poll.js).

import assert from 'node:assert/strict';
import { describe, test } from 'node:test';

import { singleFlight, startPolling } from '../static/poll.js';

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
