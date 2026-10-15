// Serial console transport: xterm.js over the VM's (or container's) TTY
// WebSocket. The open/close contract is the one in console/vnc.js.
//
// Terminal and FitAddon are the vendored classic scripts index.html loads.

import { wsURL } from '../api.js';

export function openTTY(screen, vm) {
  const term = new Terminal({ fontSize: 14, theme: { background: '#000000' }, cursorBlink: true });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(screen);
  fit.fit();
  const onResize = () => fit.fit();
  window.addEventListener('resize', onResize);

  const ws = new WebSocket(wsURL('tty', vm));
  ws.binaryType = 'arraybuffer';
  const enc = new TextEncoder();
  let closed = false;
  ws.onmessage = (e) => term.write(new Uint8Array(e.data));
  ws.onclose = () => { if (!closed) term.write('\r\n\x1b[33m[disconnected]\x1b[0m\r\n'); };
  term.onData((d) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
  });
  term.focus();

  return {
    close() {
      closed = true;
      window.removeEventListener('resize', onResize);
      try { ws.close(); } catch { /* already gone */ }
      try { term.dispose(); } catch { /* already gone */ }
    },
  };
}
