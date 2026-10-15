// Unit tests for the console transports (pkg/web/static/console/*.js): the
// key encoding, and the open/close contract with the vendored libraries
// replaced by fakes. The real noVNC handshake is ui-smoke's to check.

import assert from 'node:assert/strict';
import { beforeEach, describe, test } from 'node:test';

import { openRDP } from '../static/console/rdp.js';
import { openTTY } from '../static/console/tty.js';
import { chordFor, keysymFor } from '../static/console/vnc.js';

globalThis.location = { protocol: 'http:', host: 'corral.test' };

describe('keysymFor', () => {
  test('Return and Tab have their own keysyms', () => {
    assert.equal(keysymFor('\n'), 0xff0d);
    assert.equal(keysymFor('\t'), 0xff09);
  });

  test('Latin-1 is its own code point', () => {
    assert.equal(keysymFor('a'), 0x61);
    assert.equal(keysymFor('é'), 0xe9);
  });

  test('the rest of Unicode sets 0x01000000', () => {
    assert.equal(keysymFor('€'), 0x010020ac);
    assert.equal(keysymFor('😀'), 0x0101f600);
  });
});

describe('chordFor', () => {
  test('Ctrl+Alt+Fn', () => {
    assert.deepEqual(chordFor('f1'), [[0xffe3, 'ControlLeft'], [0xffe9, 'AltLeft'], [0xffbe, 'F1']]);
    assert.deepEqual(chordFor('f7'), [[0xffe3, 'ControlLeft'], [0xffe9, 'AltLeft'], [0xffc4, 'F7']]);
  });

  test('PrtSc', () => {
    assert.deepEqual(chordFor('print'), [[0xff61, 'PrintScreen']]);
  });
});

describe('openTTY', () => {
  let sockets;
  let terms;
  let listeners;
  beforeEach(() => {
    sockets = [];
    terms = [];
    listeners = new Map();
    globalThis.window = {
      addEventListener: (ev, fn) => listeners.set(fn, ev),
      removeEventListener: (ev, fn) => listeners.delete(fn),
    };
    globalThis.Terminal = class {
      constructor() { this.written = []; terms.push(this); }
      loadAddon() {}
      open(el) { this.el = el; }
      write(s) { this.written.push(s); }
      onData(fn) { this.dataFn = fn; }
      focus() {}
      dispose() { this.disposed = true; }
    };
    globalThis.FitAddon = { FitAddon: class { fit() {} } };
    globalThis.WebSocket = class {
      static OPEN = 1;
      constructor(url) { this.url = url; this.sent = []; this.readyState = 1; sockets.push(this); }
      send(d) { this.sent.push(d); }
      close() { this.closed = true; }
    };
  });

  test('connects the VM serial socket and types into it', () => {
    openTTY({}, { namespace: 'ns', name: 'vm1', context: 'lab' });
    assert.equal(sockets[0].url, 'ws://corral.test/api/tty/ns/vm1?context=lab');
    assert.equal(sockets[0].binaryType, 'arraybuffer');
    terms[0].dataFn('ls\r');
    assert.deepEqual([...sockets[0].sent[0]], [...new TextEncoder().encode('ls\r')]);
  });

  test('says when the server hangs up', () => {
    openTTY({}, { namespace: 'ns', name: 'vm1' });
    sockets[0].onclose();
    assert.match(terms[0].written.join(''), /\[disconnected\]/);
  });

  test('close releases the socket, the terminal and the resize listener', () => {
    const s = openTTY({}, { namespace: 'ns', name: 'vm1' });
    assert.equal(listeners.size, 1);
    s.close();
    assert.equal(sockets[0].closed, true);
    assert.equal(terms[0].disposed, true);
    assert.equal(listeners.size, 0);
    // The socket's own close event arrives after: nothing is written to a
    // terminal that is gone.
    sockets[0].onclose();
    assert.deepEqual(terms[0].written, []);
  });
});

describe('openRDP', () => {
  let made;
  beforeEach(() => {
    made = {};
    globalThis.document = { createElement: () => ({}) };
    class ConfigBuilder {
      constructor() { this.cfg = {}; }
      withUsername(v) { this.cfg.username = v; return this; }
      withPassword(v) { this.cfg.password = v; return this; }
      withDestination(v) { this.cfg.destination = v; return this; }
      withProxyAddress(v) { this.cfg.proxy = v; return this; }
      withAuthToken(v) { this.cfg.token = v; return this; }
      build() { return this.cfg; }
    }
    class UserInteraction {
      constructor(el) { this.el = el; made.ui = this; this.cad = 0; }
      setVisibility() {}
      connect(cfg) {
        made.cfg = cfg;
        return new Promise((resolve, reject) => { made.resolve = resolve; made.reject = reject; });
      }
      ctrlAltDel() { this.cad++; }
      shutdown() { this.shut = true; }
    }
    globalThis.window = { IronRemoteDesktop: { ConfigBuilder, UserInteraction } };
  });

  const screen = () => ({ children: [], appendChild(c) { this.children.push(c); } });

  test('connects through the RDCleanPath proxy with the credentials', () => {
    const el = screen();
    openRDP(el, { namespace: 'ns', name: 'win11' }, { username: 'u', password: 'p', onFail() {} });
    assert.equal(el.children[0].id, 'rdp-iron');
    assert.deepEqual(made.cfg, {
      username: 'u', password: 'p', destination: 'win11',
      proxy: 'ws://corral.test/api/rdp/ns/win11?rdcleanpath=1', token: 'corral',
    });
  });

  test('Ctrl+Alt+Del waits for the session', async () => {
    const s = openRDP(screen(), { namespace: 'ns', name: 'w' }, { username: 'u', password: 'p', onFail() {} });
    s.ctrlAltDel();
    assert.equal(made.ui.cad, 0);
    made.resolve({ run() {} });
    await new Promise((r) => setImmediate(r));
    s.ctrlAltDel();
    assert.equal(made.ui.cad, 1);
  });

  test('a failed connection reaches onFail', async () => {
    let failed;
    openRDP(screen(), { namespace: 'ns', name: 'w' }, { username: 'u', password: 'p', onFail: (e) => { failed = e; } });
    made.reject(new Error('refused'));
    await new Promise((r) => setImmediate(r));
    assert.equal(failed.message, 'refused');
  });

  test('close shuts the session down', () => {
    const s = openRDP(screen(), { namespace: 'ns', name: 'w' }, { username: 'u', password: 'p', onFail() {} });
    s.close();
    assert.equal(made.ui.shut, true);
  });
});
