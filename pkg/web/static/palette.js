// Command palette (Ctrl/Cmd+K), keyboard shortcuts and their `?` overlay (#349).
//
// The palette searches the same things the tree shows — VMs, CTs, nodes and
// pools — plus the verbs an operator reaches for ("stop web-prod", "migrate
// db-prod", "create VM"). Whatever was run from it recently ranks first, so
// the second visit to a VM is two keystrokes.
//
// It is a keyboard equivalent, never a second way around the rules: an action
// that mutates is not offered to a read-only caller, exactly as the toolbar
// that holds the same buttons is hidden from them.

// Injected by app.js so this module does not import the entry point back.
let ctx = {};
export function bindPalette(helpers) { ctx = helpers; }

const RECENT_KEY = 'corral.palette.recent';
const RECENT_MAX = 20;
const RESULTS_MAX = 50;

function loadRecent() {
  try {
    const list = JSON.parse(localStorage.getItem(RECENT_KEY) || '[]');
    return Array.isArray(list) ? list.filter((id) => typeof id === 'string') : [];
  } catch { return []; }
}

function remember(id) {
  const list = [id, ...loadRecent().filter((x) => x !== id)].slice(0, RECENT_MAX);
  try { localStorage.setItem(RECENT_KEY, JSON.stringify(list)); } catch { /* private mode */ }
}

const readOnly = () => document.body.classList.contains('read-only');

// ── entries ───────────────────────────────────────────────────────
// Each entry: { id, kind, label, sub, keywords, mutates, run }. The id is
// stable across polls so "recently used" survives a refresh and a reload.

function entries() {
  const { vms, cts, nodes, pools, go, openVM, vmAction, ctAction, openPool, createVM, createCT, icon } = ctx;
  const out = [];
  const add = (e) => out.push({ keywords: '', mutates: false, ...e });

  const views = [
    ['dc', 'Datacenter', 'datacenter'],
    ['doctor', 'Cluster health', 'health'],
    ['extensions', 'Extensions', 'extension'],
    ['multiview', 'Multiview', 'cube'],
    ['settings', 'Settings', 'cog'],
  ];
  for (const [type, label, ic] of views) {
    add({ id: `view:${type}`, kind: 'view', icon: icon(ic), label, sub: 'go to', run: () => go({ type }) });
  }
  add({ id: 'action:create-vm', kind: 'create', icon: icon('plus'), label: 'Create VM', sub: 'new virtual machine', keywords: 'new', mutates: true, run: createVM });
  add({ id: 'action:create-ct', kind: 'create', icon: icon('plus'), label: 'Create CT', sub: 'new container', keywords: 'new container', mutates: true, run: createCT });

  for (const vm of vms()) {
    const key = ctx.vmKey(vm);
    const where = [vm.backend, vm.namespace, vm.node].filter(Boolean).join(' · ');
    add({ id: `vm:${key}`, kind: 'vm', icon: icon(vm.isTemplate ? 'template' : 'cube'), label: vm.name, sub: where, keywords: (vm.tags || []).join(' '), run: () => openVM(key) });

    const cap = vm.capabilities || {};
    const verb = (act, label, when = true) => {
      if (!when) return;
      add({
        id: `vm-${act}:${key}`, kind: 'action', icon: icon({ console: 'desktop', start: 'play' }[act] || act),
        label: `${label} ${vm.name}`, sub: where, mutates: act !== 'console',
        run: () => (act === 'console' ? openVM(key, 'console') : vmAction(key, act)),
      });
    };
    verb('console', 'Console', !!(cap.vnc || cap.tty));
    verb('start', 'Start', !!cap.start && !vm.running);
    verb('stop', 'Stop', !!cap.stop && vm.running);
    verb('restart', 'Restart', !!(cap.start && cap.stop) && vm.running);
    verb('migrate', 'Migrate', vm.backend === 'kubevirt' && vm.ready && vm.liveMigratable);
  }

  for (const c of cts()) {
    const key = `${c.namespace}/${c.name}`;
    add({ id: `ct:${key}`, kind: 'ct', icon: icon('container'), label: c.name, sub: `container · ${c.namespace}`, run: () => go({ type: 'ct', key }) });
    const running = c.ready || c.phase === 'Running';
    add({
      id: `ct-${running ? 'stop' : 'start'}:${key}`, kind: 'action', icon: icon(running ? 'stop' : 'play'),
      label: `${running ? 'Stop' : 'Start'} ${c.name}`, sub: `container · ${c.namespace}`, mutates: true,
      run: () => ctAction(key, running ? 'stop' : 'start'),
    });
  }

  for (const n of nodes()) {
    add({ id: `node:${n.name}`, kind: 'node', icon: icon('server'), label: n.name, sub: `node${n.roles ? ` · ${n.roles}` : ''}`, run: () => go({ type: 'node', name: n.name }) });
  }

  for (const f of pools().folders || []) {
    add({ id: `pool:${f.path}`, kind: 'pool', icon: icon('folder'), label: f.path, sub: 'pool', run: () => openPool(f.path) });
  }

  return readOnly() ? out.filter((e) => !e.mutates) : out;
}

// ── matching ──────────────────────────────────────────────────────
// Subsequence match, scored so a prefix beats a word start beats a scatter.
// Every query word must match somewhere in the entry; "stop web" finds
// "Stop web-prod" and "web st" does too.

function scoreWord(word, text, scatter) {
  const idx = text.indexOf(word);
  if (idx === 0) return 100;
  if (idx > 0) return /[\s\-_./·]/.test(text[idx - 1]) ? 80 : 60;
  if (!scatter) return 0;
  let t = 0;
  let gaps = 0;
  for (const ch of word) {
    const at = text.indexOf(ch, t);
    if (at < 0) return 0;
    gaps += at - t;
    t = at + 1;
  }
  return Math.max(1, 40 - gaps);
}

// With nothing typed, the list reads top-down: places, then the create verbs,
// then the fleet. Per-guest verbs come last; they are for searching.
const BROWSE_ORDER = { view: 50, create: 40, action: 0, vm: 30, ct: 20, node: 10, pool: 10 };

export function rank(list, query, recent) {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const recency = new Map(recent.map((id, i) => [id, recent.length - i]));
  const scored = [];
  for (const e of list) {
    const label = e.label.toLowerCase();
    const hay = `${label} ${(e.sub || '').toLowerCase()} ${e.keywords.toLowerCase()} ${e.kind}`;
    let score = 0;
    let ok = true;
    for (const w of words) {
      // Letters scattered across the name ("dbp" → db-prod) count; scattered
      // across the namespace, node and tags they would match almost anything.
      const s = Math.max(scoreWord(w, label, true) * 1.5, scoreWord(w, hay, false));
      if (!s) { ok = false; break; }
      score += s;
    }
    if (!ok) continue;
    // An exact name beats anything else: typing a VM's whole name and hitting
    // Enter must land on that VM, not on "Stop <name>".
    if (words.length && label === words.join(' ')) score += 1000;
    const r = recency.get(e.id) || 0;
    // Recently used ranks first when browsing (empty query) and breaks near-
    // ties when searching, without letting a stale hit outrank a real match.
    score += r ? (words.length ? 30 + r : 10000 + r) : 0;
    if (!words.length) score += BROWSE_ORDER[e.kind] || 0;
    scored.push({ e, score });
  }
  scored.sort((a, b) => b.score - a.score || a.e.label.localeCompare(b.e.label));
  return scored.slice(0, RESULTS_MAX).map((s) => s.e);
}

// ── palette dialog ────────────────────────────────────────────────

let dlg = null;
let results = [];
let active = 0;

function build() {
  dlg = document.createElement('dialog');
  dlg.id = 'palette';
  dlg.setAttribute('aria-label', 'Command palette');
  dlg.innerHTML = `
    <input id="palette-input" type="text" autocomplete="off" spellcheck="false"
      role="combobox" aria-expanded="true" aria-controls="palette-list" aria-autocomplete="list"
      placeholder="Search VMs, nodes, pools and actions…">
    <ul id="palette-list" role="listbox" aria-label="Results"></ul>
    <div class="palette-foot muted"><kbd>↑</kbd><kbd>↓</kbd> move · <kbd>Enter</kbd> run · <kbd>Esc</kbd> close · <kbd>?</kbd> shortcuts</div>`;
  document.body.appendChild(dlg);

  const input = dlg.querySelector('#palette-input');
  input.addEventListener('input', () => { active = 0; update(); });
  input.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') { move(1); e.preventDefault(); }
    else if (e.key === 'ArrowUp') { move(-1); e.preventDefault(); }
    else if (e.key === 'Enter') { runActive(); e.preventDefault(); }
  });
  // A click on the backdrop lands on the dialog itself; one inside the card
  // lands on a child.
  dlg.addEventListener('click', (e) => { if (e.target === dlg) dlg.close(); });
  dlg.querySelector('#palette-list').addEventListener('click', (e) => {
    const li = e.target.closest('li[data-i]');
    if (!li) return;
    active = Number(li.dataset.i);
    runActive();
  });
}

function update() {
  const input = dlg.querySelector('#palette-input');
  const list = dlg.querySelector('#palette-list');
  const recent = loadRecent();
  results = rank(entries(), input.value, recent);
  if (active >= results.length) active = Math.max(0, results.length - 1);
  const { esc } = ctx;
  list.innerHTML = results.length
    ? results.map((e, i) => `<li id="palette-opt-${i}" role="option" data-i="${i}" data-id="${esc(e.id)}"
        aria-selected="${i === active}" class="${i === active ? 'active' : ''}">
        ${e.icon}<span class="palette-label">${esc(e.label)}</span>
        <span class="muted">${esc(e.sub || '')}</span>
        ${recent.includes(e.id) ? '<span class="chip mini">recent</span>' : ''}</li>`).join('')
    : '<li class="muted palette-empty">No matches.</li>';
  if (results.length) input.setAttribute('aria-activedescendant', `palette-opt-${active}`);
  else input.removeAttribute('aria-activedescendant');
}

function move(delta) {
  if (!results.length) return;
  active = (active + delta + results.length) % results.length;
  const list = dlg.querySelector('#palette-list');
  list.querySelectorAll('li[data-i]').forEach((li) => {
    const on = Number(li.dataset.i) === active;
    li.classList.toggle('active', on);
    li.setAttribute('aria-selected', String(on));
    if (on) li.scrollIntoView({ block: 'nearest' });
  });
  dlg.querySelector('#palette-input').setAttribute('aria-activedescendant', `palette-opt-${active}`);
}

function runActive() {
  const e = results[active];
  if (!e) return;
  remember(e.id);
  dlg.close();
  e.run();
}

export async function openPalette() {
  if (!dlg) build();
  closeShortcuts();
  if (!dlg.open) dlg.showModal();
  const input = dlg.querySelector('#palette-input');
  input.value = '';
  active = 0;
  update();
  input.focus();
  // Pools are only polled while Pool View is showing; fetch them once so the
  // palette can find one from any view.
  await ctx.ensurePools();
  if (dlg.open) update();
}

// ── shortcut overlay ──────────────────────────────────────────────

const SHORTCUTS = [
  [['Ctrl', 'K'], 'Open the command palette (⌘K on macOS)'],
  [['?'], 'Show this list'],
  [['/'], 'Filter the tree'],
  [['c'], 'Open the selected VM\'s console'],
  [['s'], 'Start or stop the selected VM'],
  [['g', 'd'], 'Go to the datacenter'],
  [['Esc'], 'Close a dialog'],
];

let help = null;

function closeShortcuts() { if (help?.open) help.close(); }

export function openShortcuts() {
  if (!help) {
    help = document.createElement('dialog');
    help.id = 'shortcuts';
    help.setAttribute('aria-labelledby', 'shortcuts-title');
    help.innerHTML = `
      <h2 id="shortcuts-title">Keyboard shortcuts</h2>
      <dl class="shortcuts">${SHORTCUTS.map(([keys, what]) =>
        `<dt>${keys.map((k) => `<kbd>${k}</kbd>`).join(' then ')}</dt><dd>${what}</dd>`).join('')}</dl>
      <p class="hint">Single-key shortcuts are ignored while you type in a field or a console.</p>
      <div class="dialog-actions"><button type="button" class="btn" id="shortcuts-close">Close</button></div>`;
    document.body.appendChild(help);
    help.querySelector('#shortcuts-close').onclick = () => help.close();
    help.addEventListener('click', (e) => { if (e.target === help) help.close(); });
  }
  // Start/stop mutates, so a read-only caller is not told it exists.
  help.querySelectorAll('dt').forEach((dt) => {
    const mutating = dt.textContent.trim() === 's';
    dt.hidden = mutating && readOnly();
    dt.nextElementSibling.hidden = dt.hidden;
  });
  if (!help.open) help.showModal();
}

// ── global keys ───────────────────────────────────────────────────

// A key typed into a field, or into a live console, belongs to that field:
// `s` in a VM name is a letter, and Ctrl+K in a serial shell kills a line.
function typing(target) {
  if (!(target instanceof Element)) return false;
  if (target.closest('input, textarea, select, [contenteditable=""], [contenteditable="true"]')) return true;
  return !!target.closest('#vnc-screen, #tty-screen, #rdp-screen, .xterm');
}

let pendingG = 0;

function onKey(e) {
  if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === 'k') {
    // Inside the palette's own box Ctrl+K reopens it, which is harmless; in a
    // console it is the console's key.
    if (typing(e.target) && e.target.id !== 'palette-input') return;
    // Another dialog (the create wizard, a move preflight) owns the screen;
    // jumping elsewhere from behind it would strand it.
    if (document.querySelector('dialog[open]:not(#palette):not(#shortcuts)') || document.querySelector('.modal')) return;
    e.preventDefault();
    openPalette();
    return;
  }
  if (e.ctrlKey || e.metaKey || e.altKey || e.defaultPrevented) return;
  if (typing(e.target)) return;
  // A modal owns the keyboard while it is open; the overlay's own keys aside.
  if (document.querySelector('dialog[open]:not(#shortcuts)') || document.querySelector('.modal')) return;

  if (pendingG && Date.now() - pendingG < 1200) {
    pendingG = 0;
    if (e.key === 'd') { e.preventDefault(); closeShortcuts(); ctx.go({ type: 'dc' }); }
    return;
  }
  pendingG = 0;

  switch (e.key) {
    case '?': e.preventDefault(); openShortcuts(); break;
    case '/': e.preventDefault(); closeShortcuts(); ctx.focusFilter(); break;
    case 'g': pendingG = Date.now(); break;
    case 'c': {
      const key = ctx.selectedVMKey();
      if (key) { e.preventDefault(); ctx.openVM(key, 'console'); }
      break;
    }
    case 's': {
      if (readOnly()) break;
      const key = ctx.selectedVMKey();
      const vm = key && ctx.vms().find((v) => ctx.vmKey(v) === key);
      if (!vm) break;
      const cap = vm.capabilities || {};
      const act = vm.running ? 'stop' : 'start';
      if (!cap[act]) break;
      e.preventDefault();
      ctx.vmAction(key, act);
      break;
    }
  }
}

export function initKeys() { document.addEventListener('keydown', onKey); }
