// Dashboard widgets and live charts (#348).
//
// mountDashboard() lays a set of widgets out on a vendored gridstack.js grid:
// drag a widget by its title bar, resize it from the corner, remove it from
// its menu and add it back from the toolbar. The layout is a per-browser
// preference, so it lives in localStorage, one key per dashboard scope.
// Every drag has a keyboard and menu equivalent: arrow keys on a focused
// title bar move the widget, Shift+arrows resize it, and the ⋯ menu does
// both plus removal.
//
// timeChart() draws a live time series with the vendored uPlot, polling a
// usage history endpoint until its element leaves the DOM.
//
// Both libraries are classic scripts loaded ahead of this module (see
// index.html), so they are globals here — no build step (ADR-0004).

const GridStack = globalThis.GridStack;
const uPlot = globalThis.uPlot;

const COLUMNS = 12;
const LAYOUT_PREFIX = 'corral.dashboard.';
const LIST_PREFIX = 'corral.dashboards.';
const CURRENT_PREFIX = 'corral.dashboard.current.';

// ── Named dashboards ───────────────────────────────
//
// One scope can hold several dashboards, each with its own arrangement and its
// own density. Prism calls this Manage Dashboard, and the point is that an
// operator watching capacity wants a different screen from one chasing a
// failure. Neither should have to rebuild the other to see it.
//
// Four decisions, none of them forced by the reference:
//
// - They live in this browser, like every other layout here. Nothing about a
//   dashboard reaches the server, so none of this is shared or synced.
// - The first one is called Default and keeps the old storage key. An
//   arrangement built before this change is still there afterwards, and there
//   is no migration step to get wrong.
// - Deleting a dashboard deletes its arrangement. Every widget is still in
//   Add widget, so there is nothing else to keep.
// - The last dashboard will not delete. A scope with none has no screen.

const DEFAULT_ID = 'default';

// The default dashboard writes to the key that already existed. Anything else
// takes a suffix, so the two cannot collide.
const layoutKey = (scope, id) => `${LAYOUT_PREFIX}${scope}${id === DEFAULT_ID ? '' : `.${id}`}`;

function dashboards(scope) {
  try {
    const raw = localStorage.getItem(LIST_PREFIX + scope);
    const parsed = raw ? JSON.parse(raw) : null;
    if (Array.isArray(parsed) && parsed.length) return parsed;
  } catch { /* fall through to the default */ }
  return [{ id: DEFAULT_ID, name: 'Default' }];
}

function saveDashboards(scope, list) {
  try { localStorage.setItem(LIST_PREFIX + scope, JSON.stringify(list)); } catch { /* not persisted */ }
}

function currentDashboard(scope) {
  const list = dashboards(scope);
  let id = null;
  try { id = localStorage.getItem(CURRENT_PREFIX + scope); } catch { /* not stored */ }
  // A stored id can name a dashboard deleted here or in another tab.
  return list.some((d) => d.id === id) ? id : list[0].id;
}

function setCurrentDashboard(scope, id) {
  try { localStorage.setItem(CURRENT_PREFIX + scope, id); } catch { /* not persisted */ }
}

// ── Layout persistence ────────────────────────────

function loadLayout(scope, id) {
  try {
    const raw = localStorage.getItem(layoutKey(scope, id));
    const parsed = raw ? JSON.parse(raw) : null;
    return Array.isArray(parsed) ? parsed : null;
  } catch { return null; } // private window / blocked storage: use defaults
}

function saveLayout(scope, id, layout) {
  try { localStorage.setItem(layoutKey(scope, id), JSON.stringify(layout)); } catch { /* not persisted */ }
}

function clearLayout(scope, id) {
  try { localStorage.removeItem(layoutKey(scope, id)); } catch { /* nothing to clear */ }
}

// ── Density ───────────────────────────────────────────────────────

// How much room the dashboard gives each widget.
//
// The data grid has had three named density modes for a while, and the
// dashboard had none: how many widgets fit on a screen was fixed. Prism's
// dashboard carries a Data Density setting for exactly this, and the names
// here are the grid's own, so one word means one thing across the UI.
//
// `margin` is the gap between widgets and `cellHeight` is the height of one
// grid row, so a widget keeps the same row span and changes size with the
// mode. The stylesheet reads the mode from a data attribute for the padding
// inside a widget.
const DASH_DENSITIES = [
  { id: 'compact', label: 'Compact', margin: 2, cellHeight: 62 },
  { id: 'cosy', label: 'Cosy', margin: 5, cellHeight: 80 },
  { id: 'roomy', label: 'Roomy', margin: 10, cellHeight: 98 },
];
const DENSITY_PREFIX = 'corral.dashboard.density.';
const DEFAULT_DENSITY = 'cosy';

// Per dashboard, not per scope: a dashboard built to fit everything on one
// screen and one built to read charts want different answers. The default
// dashboard keeps the key that already existed, as its layout does.
const densityKey = (scope, id) => `${DENSITY_PREFIX}${scope}${id === DEFAULT_ID ? '' : `.${id}`}`;

function loadDensity(scope, id) {
  try {
    const stored = localStorage.getItem(densityKey(scope, id));
    return DASH_DENSITIES.some((d) => d.id === stored) ? stored : DEFAULT_DENSITY;
  } catch { return DEFAULT_DENSITY; }
}

function saveDensity(scope, id, mode) {
  try { localStorage.setItem(densityKey(scope, id), mode); } catch { /* not persisted */ }
}

function clearDensity(scope, id) {
  try { localStorage.removeItem(densityKey(scope, id)); } catch { /* nothing to clear */ }
}

// ── Grid ──────────────────────────────────────────────────────────

const esc = (s) => String(s ?? '').replace(/[&<>"']/g,
  (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const MENU = [
  ['left', 'Move left', 'ArrowLeft'],
  ['right', 'Move right', 'ArrowRight'],
  ['up', 'Move up', 'ArrowUp'],
  ['down', 'Move down', 'ArrowDown'],
  ['wider', 'Wider', 'Shift+ArrowRight'],
  ['narrower', 'Narrower', 'Shift+ArrowLeft'],
  ['taller', 'Taller', 'Shift+ArrowDown'],
  ['shorter', 'Shorter', 'Shift+ArrowUp'],
  ['remove', 'Remove widget', 'Delete'],
];

const KEY_ACTIONS = {
  ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down',
  'Shift+ArrowRight': 'wider', 'Shift+ArrowLeft': 'narrower',
  'Shift+ArrowDown': 'taller', 'Shift+ArrowUp': 'shorter',
  Delete: 'remove',
};

// mountDashboard renders the widget grid into root and returns a handle.
//
//   scope    localStorage key suffix: one saved layout per scope
//   widgets  { id: { title, w, h, minW?, minH?, live?, render(body) } }
//            render() fills the widget body; live widgets (charts) poll on
//            their own and are not re-rendered by refresh()
//   layout   the default [{ id, x, y, w, h }], also what "Reset layout" restores
//
// The handle's refresh() re-renders every non-live widget in place, so a
// data poll never tears down the grid, an open menu or keyboard focus.
export function mountDashboard(root, { scope, widgets, layout }) {
  // Which of this scope's dashboards is showing. Everything below keys off
  // this, so a remount after a switch loads that dashboard's own arrangement.
  const board = currentDashboard(scope);
  const saved = (loadLayout(scope, board) || layout).filter((n) => widgets[n.id]);
  root.innerHTML = `
    <div class="dash-toolbar">
      <label class="dash-add" hidden>Add widget
        <select aria-label="Add a widget to this dashboard"><option value="">Choose…</option></select>
      </label>
      <label class="dash-pick">Dashboard
        <select class="dash-select" aria-label="Which dashboard"></select>
      </label>
      <button type="button" class="btn sm dash-new">New</button>
      <button type="button" class="btn sm dash-rename">Rename</button>
      <button type="button" class="btn sm dash-delete">Delete</button>
      <label class="dash-density">Density
        <select class="dash-density-select" aria-label="Widget density">
          ${DASH_DENSITIES.map((d) => `<option value="${d.id}">${d.label}</option>`).join('')}
        </select>
      </label>
      <button type="button" class="btn sm dash-reset">Reset layout</button>
      <span class="muted dash-hint">Drag a title bar to move · drag the corner to resize · or focus a title bar and use the arrow keys</span>
    </div>
    <div class="grid-stack" data-scope="${esc(scope)}"></div>
    <div class="sr-only" aria-live="polite"></div>`;
  const gridEl = root.querySelector('.grid-stack');
  const live = root.querySelector('[aria-live]');
  const addSel = root.querySelector('.dash-add select');

  const itemEl = (n) => {
    const def = widgets[n.id];
    const el = document.createElement('div');
    el.className = 'grid-stack-item';
    el.setAttribute('gs-id', n.id);
    if (n.x !== undefined && n.y !== undefined) {
      el.setAttribute('gs-x', n.x);
      el.setAttribute('gs-y', n.y);
    } else {
      el.setAttribute('gs-auto-position', 'true');
    }
    el.setAttribute('gs-w', n.w ?? def.w);
    el.setAttribute('gs-h', n.h ?? def.h);
    el.setAttribute('gs-min-w', def.minW ?? 2);
    el.setAttribute('gs-min-h', def.minH ?? 2);
    el.innerHTML = `
      <section class="grid-stack-item-content widget" aria-label="${esc(def.title)}">
        <header class="widget-head" tabindex="0"
          title="Drag to move · arrow keys move · Shift+arrow keys resize">
          <span class="widget-title">${esc(def.title)}</span>
          <span class="spacer"></span>
          <button type="button" class="widget-menu-btn" aria-haspopup="menu" aria-expanded="false"
            aria-label="Actions for ${esc(def.title)}">⋯</button>
          <div class="widget-menu" role="menu" hidden>
            ${MENU.map(([act, label, keys]) => `<button type="button" role="menuitem" data-wact="${act}">${label}<kbd>${esc(keys)}</kbd></button>`).join('')}
          </div>
        </header>
        <div class="widget-body"></div>
      </section>`;
    return el;
  };

  for (const n of saved) gridEl.appendChild(itemEl(n));

  const density = () => DASH_DENSITIES.find((d) => d.id === loadDensity(scope, board)) || DASH_DENSITIES[1];
  const mode = density();
  root.dataset.density = mode.id;

  const grid = GridStack.init({
    column: COLUMNS,
    cellHeight: mode.cellHeight,
    margin: mode.margin,
    float: false,
    animate: false,
    handle: '.widget-head',
    resizable: { handles: 'se' },
  }, gridEl);

  const announce = (msg) => { live.textContent = msg; };

  // One column on a phone.
  //
  // A 12-column grid scaled to 420px gives each widget about a third of the
  // screen: the capacity figures wrap and clip, the charts collapse to a
  // sliver, and the task table shows timestamps and nothing else. The grid is
  // the wrong shape at that width, not merely cramped.
  //
  // The narrow layout is derived, never saved. Collapsing to one column moves
  // every node, and persisting that would overwrite the arrangement the
  // operator built on a larger screen with a single stack — a phone visit
  // would quietly destroy their desktop dashboard. So `persist()` does nothing
  // while the grid is narrow, and the saved layout comes back when the screen
  // does.
  const narrow = window.matchMedia('(max-width: 760px)');
  let isNarrow = false;
  const applyColumns = () => {
    isNarrow = narrow.matches;
    // 'list' stacks the widgets in their current order rather than trying to
    // preserve x/y that mean nothing in one column.
    grid.column(isNarrow ? 1 : COLUMNS, isNarrow ? 'list' : 'moveScale');
    root.classList.toggle('dash-narrow', isNarrow);
  };
  applyColumns();
  narrow.addEventListener('change', applyColumns);

  const persist = () => {
    if (isNarrow) return; // derived layout — see applyColumns above
    // Read the nodes directly: grid.save() leaves out sizes that match its own
    // defaults, which would restore as this dashboard's defaults instead.
    saveLayout(scope, board, grid.getGridItems().map((el) => el.gridstackNode).filter(Boolean)
      .map(({ id, x, y, w, h }) => ({ id, x, y, w, h })));
    syncAddMenu();
  };

  function syncAddMenu() {
    const shown = new Set(grid.getGridItems().map((el) => el.getAttribute('gs-id')));
    const hidden = Object.keys(widgets).filter((id) => !shown.has(id));
    addSel.innerHTML = '<option value="">Choose…</option>' +
      hidden.map((id) => `<option value="${esc(id)}">${esc(widgets[id].title)}</option>`).join('');
    addSel.closest('label').hidden = hidden.length === 0;
  }

  function renderBody(el) {
    const def = widgets[el.getAttribute('gs-id')];
    const body = el.querySelector('.widget-body');
    try { def.render(body); } catch (e) { body.innerHTML = `<p class="muted">${esc(e.message)}</p>`; }
  }

  function closeMenus(except) {
    gridEl.querySelectorAll('.widget-menu:not([hidden])').forEach((m) => {
      if (m === except) return;
      m.hidden = true;
      m.parentElement.querySelector('.widget-menu-btn').setAttribute('aria-expanded', 'false');
    });
  }

  function act(el, action) {
    const n = el.gridstackNode;
    if (!n) return;
    const title = widgets[n.id].title;
    if (action === 'remove') {
      grid.removeWidget(el);
      announce(`${title} removed. Add it back from “Add widget”.`);
      root.querySelector('.dash-add select')?.focus();
      return;
    }
    const minW = n.minW ?? 1;
    const minH = n.minH ?? 1;
    const next = { x: n.x, y: n.y, w: n.w, h: n.h };
    switch (action) {
      case 'left': next.x = Math.max(0, n.x - 1); break;
      case 'right': next.x = Math.min(COLUMNS - n.w, n.x + 1); break;
      case 'up': next.y = Math.max(0, n.y - 1); break;
      case 'down': next.y = n.y + 1; break;
      case 'wider': next.w = Math.min(COLUMNS - n.x, n.w + 1); break;
      case 'narrower': next.w = Math.max(minW, n.w - 1); break;
      case 'taller': next.h = n.h + 1; break;
      case 'shorter': next.h = Math.max(minH, n.h - 1); break;
      default: return;
    }
    grid.update(el, next);
    const now = el.gridstackNode;
    announce(`${title}: column ${now.x + 1}, row ${now.y + 1}, ${now.w} wide, ${now.h} tall`);
  }

  function wire(el) {
    const head = el.querySelector('.widget-head');
    const btn = el.querySelector('.widget-menu-btn');
    const menu = el.querySelector('.widget-menu');
    btn.onclick = (e) => {
      e.stopPropagation();
      const open = menu.hidden;
      closeMenus(menu);
      menu.hidden = !open;
      btn.setAttribute('aria-expanded', String(open));
      if (open) menu.querySelector('button')?.focus();
    };
    menu.onclick = (e) => {
      const b = e.target.closest('[data-wact]');
      if (!b) return;
      menu.hidden = true;
      btn.setAttribute('aria-expanded', 'false');
      act(el, b.dataset.wact);
      if (b.dataset.wact !== 'remove') head.focus();
    };
    menu.onkeydown = (e) => {
      const items = [...menu.querySelectorAll('button')];
      const i = items.indexOf(document.activeElement);
      if (e.key === 'Escape') { menu.hidden = true; btn.setAttribute('aria-expanded', 'false'); btn.focus(); }
      else if (e.key === 'ArrowDown') items[(i + 1) % items.length].focus();
      else if (e.key === 'ArrowUp') items[(i - 1 + items.length) % items.length].focus();
      else return;
      e.preventDefault();
      e.stopPropagation();
    };
    head.onkeydown = (e) => {
      if (e.target !== head) return;
      const action = KEY_ACTIONS[(e.shiftKey ? 'Shift+' : '') + e.key];
      if (!action) return;
      e.preventDefault();
      act(el, action);
    };
  }

  grid.getGridItems().forEach((el) => { wire(el); renderBody(el); });

  grid.on('change', persist);
  grid.on('removed', persist);
  grid.on('added', persist);
  grid.on('dragstart resizestart', () => closeMenus());
  // Charts size themselves to their widget; tell them the widget changed.
  grid.on('resizestop', (_e, el) => el.querySelectorAll('.dash-chart').forEach((c) => c._resize?.()));

  addSel.onchange = () => {
    const id = addSel.value;
    if (!id) return;
    const el = itemEl({ id });
    gridEl.appendChild(el);
    grid.makeWidget(el);
    wire(el);
    renderBody(el);
    persist();
    announce(`${widgets[id].title} added.`);
    el.querySelector('.widget-head').focus();
  };
  // ── The dashboard picker ─────────────────────────────
  //
  // Each of these remounts into the same root, which is how Reset layout
  // already works. The remount re-reads the store, so there is one path that
  // builds a dashboard and no second way for the two to disagree.
  const remount = () => {
    grid.destroy(false);
    mountDashboard(root, { scope, widgets, layout });
  };

  const boardSel = root.querySelector('.dash-select');
  const list = dashboards(scope);
  boardSel.innerHTML = list.map((d) =>
    `<option value="${esc(d.id)}"${d.id === board ? ' selected' : ''}>${esc(d.name)}</option>`).join('');
  boardSel.onchange = () => { setCurrentDashboard(scope, boardSel.value); remount(); };

  root.querySelector('.dash-new').onclick = () => {
    const name = (prompt('Name for the new dashboard:', '') || '').trim();
    if (!name) return;
    // An id derived from the clock, because two dashboards may share a name
    // and the id is what the storage keys are built from.
    const id = `d${Date.now().toString(36)}`;
    saveDashboards(scope, [...dashboards(scope), { id, name }]);
    setCurrentDashboard(scope, id);
    // No layout saved for it, so the remount lays out the defaults.
    remount();
  };

  root.querySelector('.dash-rename').onclick = () => {
    const current = dashboards(scope).find((d) => d.id === board);
    const name = (prompt('Rename this dashboard:', current?.name || '') || '').trim();
    if (!name) return;
    saveDashboards(scope, dashboards(scope).map((d) => (d.id === board ? { ...d, name } : d)));
    remount();
  };

  const delBtn = root.querySelector('.dash-delete');
  // The last one cannot go: a scope with no dashboard has no screen to draw.
  delBtn.disabled = list.length < 2;
  delBtn.title = delBtn.disabled ? 'A scope keeps at least one dashboard' : '';
  delBtn.onclick = () => {
    const current = dashboards(scope).find((d) => d.id === board);
    if (!confirm(`Delete the dashboard "${current?.name || board}"? Its arrangement goes with it.`)) return;
    clearLayout(scope, board);
    clearDensity(scope, board);
    const rest = dashboards(scope).filter((d) => d.id !== board);
    saveDashboards(scope, rest);
    setCurrentDashboard(scope, rest[0].id);
    remount();
  };

  const densitySel = root.querySelector('.dash-density-select');
  densitySel.value = mode.id;
  densitySel.onchange = () => {
    saveDensity(scope, board, densitySel.value);
    const next = density();
    root.dataset.density = next.id;
    // Both, or a widget keeps its pixel height and only the gaps move.
    grid.margin(next.margin);
    grid.cellHeight(next.cellHeight);
    announce(`Widget density: ${next.label}.`);
  };

  root.querySelector('.dash-reset').onclick = () => {
    clearLayout(scope, board);
    // Density is part of how this dashboard looks, so the way back to the
    // defaults has to cover it. Every other thing the operator can change
    // here is restored by this button.
    clearDensity(scope, board);
    remount();
  };
  syncAddMenu();

  // "Reset layout" mounts afresh into the same root, so the handle looks the
  // current grid up through root rather than closing over this one.
  root._dashRefresh = () => grid.getGridItems().forEach((el) => {
    if (!widgets[el.getAttribute('gs-id')].live) renderBody(el);
  });
  return { refresh: () => root._dashRefresh?.() };
}

// One listener for every dashboard: a click outside an open widget menu closes it.
document.addEventListener('click', (e) => {
  document.querySelectorAll('.widget-menu:not([hidden])').forEach((m) => {
    if (m.parentElement.contains(e.target)) return;
    m.hidden = true;
    m.parentElement.querySelector('.widget-menu-btn')?.setAttribute('aria-expanded', 'false');
  });
});

// ── Charts ────────────────────────────────────────────────────────

export const fmtCPU = (milli) => (milli >= 1000 ? `${(milli / 1000).toFixed(milli >= 10000 ? 0 : 1)} cores` : `${Math.round(milli)}m`);

export function fmtBytes(b) {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let v = Number(b) || 0;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
}

const cssVar = (name, fallback) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;

// timeChart draws one metric from a usage history endpoint and keeps it live.
//
//   load     async () => [{ t: epochMillis, cpu, mem }]
//   metric   'cpu' | 'mem' — the sample field to plot
//   label    series name for the legend and screen readers
//   empty    HTML shown while the endpoint has no samples
//   height   plot height in px; defaults to filling the box
//
// The rendered .dash-chart carries data-points (the sample count), so a
// test can see that a chart drew data without reading the canvas.
export function timeChart(box, { load, metric, label, empty, height, every = 15000 }) {
  const fmt = metric === 'mem' ? fmtBytes : fmtCPU;
  box.innerHTML = `<div class="dash-chart" data-points="0" role="img" aria-label="${esc(label)}"></div>
    <div class="muted dash-chart-note"></div>`;
  const el = box.querySelector('.dash-chart');
  const note = box.querySelector('.dash-chart-note');
  let plot = null;
  let timer = null;

  const size = () => ({
    width: Math.max(160, el.clientWidth || box.clientWidth || 300),
    // clientHeight includes the widget body's padding; leave room for it and the note.
    height: height || Math.max(60, (box.clientHeight || 140) - (note.offsetHeight || 18) - 30),
  });
  el._resize = () => plot?.setSize(size());

  const draw = async () => {
    if (!el.isConnected) { clearInterval(timer); return; }
    let samples;
    try { samples = await load(); } catch { samples = null; }
    if (!el.isConnected) return;
    if (!samples || !samples.length) {
      el.dataset.points = '0';
      note.innerHTML = empty || 'No samples yet.';
      return;
    }
    const data = [samples.map((s) => s.t / 1000), samples.map((s) => s[metric] ?? null)];
    const last = data[1][data[1].length - 1];
    const peak = Math.max(...data[1].filter((v) => v != null));
    el.dataset.points = String(samples.length);
    el.setAttribute('aria-label', `${label}: now ${fmt(last)}, peak ${fmt(peak)}`);
    note.textContent = `now ${fmt(last)} · peak ${fmt(peak)} · ${samples.length} samples`;
    if (plot) { plot.setData(data); return; }
    if (!uPlot) return; // vendored script missing: the note still carries the numbers
    const accent = cssVar('--accent', '#f0883e');
    const muted = cssVar('--muted', '#8b949e');
    const grid = cssVar('--border', '#30363d');
    const axis = { stroke: muted, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid, width: 1 } };
    plot = new uPlot({
      ...size(),
      legend: { show: false },
      cursor: { drag: { x: false, y: false } },
      scales: { y: { range: (_u, _min, max) => [0, Math.max(max * 1.1, metric === 'mem' ? 1 << 20 : 10)] } },
      axes: [axis, { ...axis, size: 80, values: (_u, ticks) => ticks.map(fmt) }],
      series: [{}, { label, stroke: accent, width: 1.5, fill: /^#[0-9a-f]{6}$/i.test(accent) ? `${accent}22` : 'rgba(240,136,62,.13)', value: (_u, v) => (v == null ? '—' : fmt(v)) }],
    }, data, el);
  };

  draw();
  timer = setInterval(draw, every);
  if (typeof ResizeObserver === 'function') {
    const ro = new ResizeObserver(() => { if (el.isConnected) el._resize(); else ro.disconnect(); });
    ro.observe(box);
  }
  return el;
}
