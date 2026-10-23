// Consoles: the console tabs, their toolbars and the pop-out route. The
// connections themselves are transports in console/ (VNC, serial TTY, RDP),
// each opened into a screen element and closed by disconnectConsoles().

import { findVM, vmKey } from './api.js';
import { markRendered } from './app.js';
import { renderContent } from './router.js';
import { openRDP } from './console/rdp.js';
import { openTTY } from './console/tty.js';
import { chordFor, openVNC } from './console/vnc.js';
import { icon } from './icons.js';
import { state } from './state.js';
import { renderTree } from './tree.js';
import { $, esc } from './ui/dom.js';

// The open sessions. One console tab shows at a time, but each kind is held
// on its own so disconnectConsoles() can close whatever is open.
let vnc = null;
let tty = null;
let rdp = null;

// ── Consoles ──────────────────────────────────────────────────────

// toggleFullscreen puts el into (or out of) browser fullscreen.
function toggleFullscreen(el) {
  if (document.fullscreenElement) document.exitFullscreen();
  else el.requestFullscreen?.();
}
// Element fullscreen fires no window resize, but noVNC's scaling and xterm's
// fit addon both key off it — give them one.
document.addEventListener('fullscreenchange', () => window.dispatchEvent(new Event('resize')));

const CONSOLE_TABS_KEY = 'corral.consoleTabs.v1';
function loadConsoleTabs() {
  try { return JSON.parse(sessionStorage.getItem(CONSOLE_TABS_KEY) || '[]'); }
  catch { return []; }
}
function saveConsoleTabs(keys) {
  try { sessionStorage.setItem(CONSOLE_TABS_KEY, JSON.stringify(keys)); } catch { /* private mode */ }
}
function rememberConsole(vm) {
  const key = vmKey(vm);
  const keys = loadConsoleTabs().filter((k) => findVM(k));
  if (!keys.includes(key)) keys.push(key);
  saveConsoleTabs(keys);
  return keys;
}
function openConsole(vm) {
  disconnectConsoles();
  state.selected = { type: 'vm', key: vmKey(vm) };
  state.tab = 'console';
  renderTree();
  renderContent();
  markRendered();
}
function consoleTabStrip(vm) {
  const keys = rememberConsole(vm);
  return `<div class="console-tabs" role="tablist" aria-label="Open consoles">
    ${keys.map((key) => {
      const item = findVM(key);
      if (!item) return '';
      const active = key === vmKey(vm);
      return `<span class="console-tab ${active ? 'active' : ''}">
        <button role="tab" aria-selected="${active}" data-console-tab="${esc(key)}">${esc(item.name)}</button>
        <button class="console-tab-close" data-console-close="${esc(key)}" aria-label="Close ${esc(item.name)} console tab">×</button>
      </span>`;
    }).join('')}
  </div>`;
}
function bindConsoleTabs(body, vm) {
  body.querySelectorAll('[data-console-tab]').forEach((button) => {
    button.onclick = () => {
      const target = findVM(button.dataset.consoleTab);
      if (target) openConsole(target);
    };
  });
  body.querySelectorAll('[data-console-close]').forEach((button) => {
    button.onclick = () => {
      const keys = loadConsoleTabs().filter((key) => key !== button.dataset.consoleClose);
      saveConsoleTabs(keys);
      if (button.dataset.consoleClose === vmKey(vm) && keys.length) {
        const target = findVM(keys[keys.length - 1]);
        if (target) return openConsole(target);
      }
      if (button.dataset.consoleClose === vmKey(vm)) {
        disconnectConsoles();
        state.tab = 'summary';
        return renderContent();
      }
      button.closest('.console-tab')?.remove();
    };
  });
}

function popOutConsole(vm) {
  const key = vmKey(vm);
  const url = `${location.pathname}?console=${encodeURIComponent(key)}`;
  window.open(url, `corral-console-${key.replace(/[^a-z0-9]/gi, '-')}`, 'popup,width=1100,height=760');
}

async function pasteConsoleText() {
  let text = '';
  try { text = await navigator.clipboard.readText(); }
  catch { text = prompt('Paste text to type into the guest:', '') ?? ''; }
  if (text) vnc?.sendText(text);
}

function bindConsoleControls(vm, body, screen) {
  bindConsoleTabs(body, vm);
  $('#vnc-popout').onclick = () => popOutConsole(vm);
  $('#vnc-fullscreen').onclick = () => toggleFullscreen(screen);
  const applyScale = (mode) => vnc?.setScale(mode);
  body.querySelectorAll('[name=vnc-scale-mode]').forEach((input) => {
    input.onchange = () => { if (input.checked) applyScale(input.value); };
  });
  $('#vnc-paste').onclick = pasteConsoleText;
  body.querySelectorAll('[data-send-keys]').forEach((button) => {
    button.onclick = () => {
      const key = button.dataset.sendKeys;
      if (key === 'cad') return vnc?.ctrlAltDel();
      return vnc?.sendChord(chordFor(key));
    };
  });
}

export async function connectVNC(vm, body) {
  if (!vm.running) {
    body.innerHTML = `<p class="console-msg">VM is not running — start it to open the console.</p>`;
    return;
  }
  body.innerHTML = `${consoleTabStrip(vm)}
    <div class="toolbar console-bar">
      <button class="btn sm" id="vnc-popout" title="Open this console in its own browser window">Pop out</button>
      <button class="btn sm" id="vnc-fullscreen" title="Fullscreen (Esc to leave)">${icon('expand')} Fullscreen</button>
      <fieldset class="console-scale" aria-label="Console scale">
        <label class="console-opt"><input type="radio" name="vnc-scale-mode" id="vnc-scale" value="fit" checked> Fit</label>
        <label class="console-opt"><input type="radio" name="vnc-scale-mode" id="vnc-one" value="one"> 1:1</label>
        <label class="console-opt" title="Ask the guest to match the window (needs guest support)">
          <input type="radio" name="vnc-scale-mode" id="vnc-resize" value="remote"> Remote resize</label>
      </fieldset>
      <button class="btn sm" id="vnc-paste">Paste as text</button>
      <details class="send-keys"><summary class="btn sm">Send keys</summary>
        <div class="send-keys-menu" role="menu">
          <button role="menuitem" data-send-keys="cad">Ctrl+Alt+Del</button>
          ${[1, 2, 3, 4, 5, 6, 7].map((f) => `<button role="menuitem" data-send-keys="f${f}">Ctrl+Alt+F${f}</button>`).join('')}
          <button role="menuitem" data-send-keys="print">PrtSc</button>
        </div>
      </details>
    </div>
    <div id="vnc-screen"><p class="console-msg">Connecting…</p></div>`;
  try {
    const screen = $('#vnc-screen');
    vnc = await openVNC(screen, vm);
    bindConsoleControls(vm, body, screen);
    vnc.on('connect', () => {
      screen.dataset.connected = 'true';
      screen.setAttribute('aria-label', `${vm.name} console connected`);
    });
    vnc.on('disconnect', () => {
      if (state.tab === 'console') {
        screen.innerHTML = `<p class="console-msg">Console disconnected.<br>
          <button class="btn sm" id="vnc-reconnect" style="margin-top:10px">${icon('play')} Reconnect</button></p>`;
        const b = $('#vnc-reconnect');
        if (b) b.onclick = () => connectVNC(vm, body);
      }
    });
  } catch (e) {
    body.innerHTML = `<p class="console-msg">VNC failed: ${esc(e.message)}</p>`;
  }
}

export function connectTTY(vm, body) {
  if (!vm.running) {
    body.innerHTML = `<p class="console-msg">VM is not running — start it to open the serial console.</p>`;
    return;
  }
  body.innerHTML = `
    <div class="toolbar console-bar">
      <button class="btn sm" id="tty-fullscreen" title="Fullscreen (Esc to leave)">${icon('expand')} Fullscreen</button>
    </div>
    <div id="tty-screen"></div>
    <p class="hint" style="color:var(--muted);font-size:.78rem">Serial console via virtctl — hit Enter if blank.</p>`;
  $('#tty-fullscreen').onclick = () => toggleFullscreen($('#tty-screen'));
  tty = openTTY($('#tty-screen'), vm);
}

// Bumped every time the consoles are torn down. A caller holding on to the
// element a console was mounted in can compare this to know whether what it is
// holding is still connected or just a dead canvas.
let generation = 0;
export const consoleGeneration = () => generation;

export function disconnectConsoles() {
  generation++;
  vnc?.close();
  vnc = null;
  tty?.close();
  tty = null;
  rdp?.close();
  rdp = null;
}

// ── RDP console (IronRDP via RDCleanPath) ────────────────────────

export function connectRDP(vm, body) {
  if (!vm.running) {
    body.innerHTML = '<p class="console-msg">VM is stopped. Start it to connect via RDP.</p>';
    return;
  }

  body.innerHTML = `
    <div class="console-bar">
      <label class="console-opt" style="margin-right:12px">
        <input type="checkbox" id="rdp-scale" checked> Scale to fit
      </label>
      <button class="btn sm" id="rdp-fullscreen">${icon('expand')} Fullscreen</button>
      <button class="btn sm" id="rdp-ctrlaltdel">Ctrl+Alt+Del</button>
    </div>
    <div id="rdp-screen" style="background:#000;border:1px solid var(--border);border-radius:var(--radius);min-height:400px;display:flex;align-items:center;justify-content:center">
      <p class="console-msg">Initializing RDP…</p>
    </div>`;

  const screen = $('#rdp-screen');
  // Prompt for credentials.
  const username = prompt('RDP username:', '');
  if (username === null) { screen.innerHTML = '<p class="console-msg">RDP cancelled.</p>'; return; }
  const password = prompt('RDP password:', '');
  if (password === null) { screen.innerHTML = '<p class="console-msg">RDP cancelled.</p>'; return; }

  screen.innerHTML = '';
  rdp = openRDP(screen, vm, {
    username,
    password,
    onFail: (e) => {
      screen.innerHTML = `<p class="console-msg">RDP connection failed: ${esc(e.message || String(e))}</p>`;
    },
  });

  // Fullscreen toggles the container.
  $('#rdp-fullscreen').onclick = () => {
    if (document.fullscreenElement) {
      document.exitFullscreen();
    } else {
      screen.requestFullscreen();
    }
  };
  $('#rdp-ctrlaltdel').onclick = () => rdp?.ctrlAltDel();
}
