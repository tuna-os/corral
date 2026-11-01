// The standalone console page (/console/{ns}/{name}, served by
// pkg/web/consolepage.go): one VM's VNC console and nothing else, for scripts
// and browser automation. Its contract:
//
//   - body[data-connected] is "true" while the console is up and "false"
//     otherwise. #vnc-screen carries the same flag, as in the dashboard.
//   - #vnc-canvas is the noVNC canvas. It gets keyboard focus on every
//     connect, so keys go to the guest.
//   - After a disconnect the page reconnects by itself. A guest reboot, a
//     logout that restarts the display server, and a VM that is not running
//     yet all end up connected again with no reload.
//
// Unit tests: pkg/web/jstest/consolepage.test.mjs.

// The wait before reconnect attempt n (counted from 0): 1 s, 2 s, 4 s, then
// every 5 s.
export function retryDelay(attempt) {
  return Math.min(1000 * 2 ** attempt, 5000);
}

// open is openVNC, or a fake in the tests; schedule is setTimeout.
export function runConsolePage({ body, screen, status, open, schedule = setTimeout, request = globalThis.fetch }) {
  const vm = { namespace: body.dataset.namespace, name: body.dataset.vm, context: body.dataset.context || '' };
  const scale = body.dataset.scale === 'fit' ? 'fit' : 'one';
  let attempt = 0;
  let notified = false;

  const notifyReady = () => {
    const nonce = body.dataset.readyNonce;
    if (!nonce || notified) return;
    notified = true;
    const path = `/api/console-ready/${encodeURIComponent(vm.namespace)}/${encodeURIComponent(vm.name)}`;
    // Only the real RFB connect callback calls this; a successful opener or
    // websocket upgrade is not readiness. No cross-origin callback is accepted.
    Promise.resolve().then(() => request(path, {
      method: 'POST',
      mode: 'same-origin',
      credentials: 'same-origin',
      redirect: 'error',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ nonce, context: vm.context }),
    })).catch(() => {
      // Failure leaves the consumer waiting rather than claiming readiness.
    });
  };

  const setConnected = (on) => {
    body.dataset.connected = String(on);
    screen.dataset.connected = String(on);
  };
  const retry = (why) => {
    setConnected(false);
    const wait = retryDelay(attempt++);
    status.textContent = `${why} Reconnecting in ${wait / 1000} s…`;
    schedule(connect, wait);
  };

  async function connect() {
    status.textContent = 'Connecting…';
    let session;
    try {
      session = await open(screen, vm);
    } catch (e) {
      retry(`VNC failed: ${e.message}.`);
      return;
    }
    session.setScale(scale);
    const canvas = screen.querySelector('canvas');
    if (canvas) canvas.id = 'vnc-canvas';
    let ended = false;
    session.on('connect', () => {
      if (ended) return;
      attempt = 0;
      setConnected(true);
      status.textContent = 'Connected.';
      session.focus();
      notifyReady();
    });
    session.on('disconnect', () => {
      if (ended) return;
      ended = true;
      retry('Console disconnected.');
    });
  }

  return connect();
}
