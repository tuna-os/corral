// VNC console transport: one noVNC connection to a VM's VNC WebSocket.
//
// The contract every console transport keeps (#288): open*(screen, vm) puts a
// live connection in `screen` and returns a session; session.close() tears it
// down and is safe to call twice. console.js owns the toolbar around it and
// decides when to close. Unit tests: pkg/web/jstest/console.test.mjs.

import { wsURL } from '../api.js';

// The X11 keysym for a character typed into the guest: Return and Tab have
// their own, Latin-1 is its own code point, and the rest of Unicode is the
// code point with 0x01000000 set.
export function keysymFor(char) {
  const point = char.codePointAt(0);
  if (char === '\n') return 0xff0d;
  if (char === '\t') return 0xff09;
  return point <= 0xff ? point : 0x01000000 | point;
}

// The keysyms and codes for Ctrl+Alt+F1..F7 and PrtSc, as sendChord takes them.
export function chordFor(key) {
  if (key === 'print') return [[0xff61, 'PrintScreen']];
  const f = Number(key.slice(1));
  return [[0xffe3, 'ControlLeft'], [0xffe9, 'AltLeft'], [0xffbd + f, `F${f}`]];
}

export async function openVNC(screen, vm) {
  const { default: RFB } = await import('../vendor/novnc-rfb.esm.js');
  screen.replaceChildren();
  const rfb = new RFB(screen, wsURL('vnc', vm));
  rfb.scaleViewport = true;  // noVNC local scaling — fits any window size
  rfb.resizeSession = false; // remote resize is opt-in (guest must support it)
  return {
    on: (event, fn) => rfb.addEventListener(event, fn),
    // fit: scale locally; one: 1:1 pixels; remote: ask the guest to resize.
    setScale(mode) {
      rfb.scaleViewport = mode === 'fit';
      rfb.resizeSession = mode === 'remote';
    },
    // Press the keys in order, release them in reverse.
    sendChord(keys) {
      for (const [keysym, code] of keys) rfb.sendKey(keysym, code, true);
      for (const [keysym, code] of [...keys].reverse()) rfb.sendKey(keysym, code, false);
      rfb.focus();
    },
    sendText(text) {
      for (const char of text.replace(/\r\n?/g, '\n')) rfb.sendKey(keysymFor(char), '', undefined);
      rfb.focus();
    },
    ctrlAltDel: () => rfb.sendCtrlAltDel(),
    close() {
      try { rfb.disconnect(); } catch { /* already gone */ }
    },
  };
}
