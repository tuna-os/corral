// The fleet poll's schedule: one refresh at a time, and the next one scheduled
// from the end of the last. Unit tests: pkg/web/jstest/poll.test.mjs.
//
// This used to be `setInterval(refresh, 5000)`. refresh() is asynchronous and
// awaits several endpoints in turn, so a fixed timer fires whether or not the
// previous one came back: on a large fleet or a loaded cluster the requests
// overlap, pile onto a server that is already behind, and can interleave their
// writes to state so a render draws half of one poll and half of another.
//
// Proxmox's UpdateStore does not use a fixed timer. It schedules the next load
// from the previous load's callback, and adds twice the time that load took,
// so a slow server is asked less often without anybody tuning anything. That
// is the behaviour here: singleFlight() keeps one refresh in flight, and
// startPolling() sets the delay from the runtime it measured.

// singleFlight wraps `run(force)` so only one call is in flight at a time.
//
// A refresh asked for while one runs is not dropped. It runs once the current
// one finishes, and a forced request stays forced, because the callers that
// force one have just changed something and need to see it. Any number of
// requests during one run collapse into a single rerun.
export function singleFlight(run) {
  let inFlight = false;
  let again = false;
  let againForced = false;
  return async function refresh(force = false) {
    if (inFlight) {
      again = true;
      againForced = againForced || force;
      return;
    }
    inFlight = true;
    try {
      let next = force;
      for (;;) {
        await run(next);
        if (!again) break;
        next = againForced;
        again = false;
        againForced = false;
      }
    } finally {
      inFlight = false;
      again = false;
      againForced = false;
    }
  };
}

// startPolling calls `refresh()` every `interval` ms plus twice how long the
// last call took, so a healthy server is polled on the interval and a slow one
// gets room to recover. The first call is one interval from now.
//
// While `hidden()` is true nothing is fetched, but the loop keeps its cadence;
// the caller catches up when the page is shown again. A refresh that throws
// still schedules the next tick: if it did not, polling would stop for the
// life of the page. The error itself is left to surface in the console.
// `now` and `schedule` are the clock, replaceable in tests.
export function startPolling(refresh, {
  interval, hidden = () => false, now = Date.now, schedule = setTimeout,
}) {
  const tick = async () => {
    const started = now();
    try {
      if (!hidden()) await refresh();
    } finally {
      schedule(tick, interval + (now() - started) * 2);
    }
  };
  schedule(tick, interval);
}

// PollingState coordinates refresh timing, dedup, failure tracking, and deferred renders.
//
// Extracted from app.js (#605). It decouples the poll cadence and lifecycle
// state from DOM rendering, making refresh logic independently testable.
export class PollingState {
  constructor({
    interval = 5000,
    fetchData = null,
    fingerprint = () => '',
    render = () => {},
    shouldDefer = () => false,
    shouldSkip = () => false,
    onIdentityLost = null,
    onOffline = null,
    onError = null,
  } = {}) {
    this.interval = interval;
    this.fetchData = fetchData;
    this.fingerprint = fingerprint;
    this.render = render;
    this.shouldDefer = shouldDefer;
    this.shouldSkip = shouldSkip;
    this.onIdentityLost = onIdentityLost;
    this.onOffline = onOffline;
    this.onError = onError;

    this.pending = false;
    this.lastError = null;
    this.lastFailure = '';
    this.lastRenderFp = '';
    this.renderDeferred = false;
    this.identityLost = false;
    this.offlineShown = false;

    this._singleFlightPoll = singleFlight((force) => this._executePoll(force));
  }

  isPending() {
    return this.pending;
  }

  markRendered() {
    this.lastRenderFp = this.fingerprint();
  }

  async poll(force = false) {
    return this._singleFlightPoll(force);
  }

  async _executePoll(force = false) {
    this.pending = true;
    try {
      let wasIdentityLost = this.identityLost;
      if (this.fetchData) {
        await this.fetchData(force || wasIdentityLost);
      }
      this.identityLost = false;
      this.lastError = null;
      this.lastFailure = '';
      this.offlineShown = false;

      const fp = this.fingerprint();
      if (!force && !wasIdentityLost && fp === this.lastRenderFp) return;
      if (!force && !wasIdentityLost && this.shouldSkip()) return;
      if (!force && !wasIdentityLost && this.shouldDefer()) {
        this.renderDeferred = true;
        return;
      }

      this.lastRenderFp = fp;
      this.render(force || wasIdentityLost);
    } catch (e) {
      this.lastError = e;
      if (e.status === 401) {
        if (!this.identityLost) {
          this.identityLost = true;
          this.onIdentityLost?.(() => this.poll(true));
        }
        return;
      }
      if (!this.lastRenderFp && !this.offlineShown) {
        this.offlineShown = true;
        this.onOffline?.(e, () => {
          this.offlineShown = false;
          this.poll(true);
        });
      } else if (this.lastRenderFp && e.message !== this.lastFailure) {
        this.onError?.(e);
      }
      this.lastFailure = e.message || String(e);
    } finally {
      this.pending = false;
    }
  }

  onSettled() {
    if (!this.renderDeferred) return;
    this.renderDeferred = false;
    return this.poll();
  }

  start({ hidden = () => false, now = Date.now, schedule = setTimeout } = {}) {
    startPolling(() => this.poll(), {
      interval: this.interval,
      hidden,
      now,
      schedule,
    });
  }
}
