// Widgets and sections that a plugin declares (RFC-0002, stages 1 and 2).
//
// A plugin never sends this page code or markup. It declares items in its
// metadata; the server runs each item's command, checks the document it
// prints against a closed set of shapes - rows, a table or a message - and
// passes on only the checked document. This module draws those three shapes
// and nothing else, and every string it draws goes through esc(). A plugin
// that wants a shape this module does not draw has to wait for corral to
// learn it; that is the price of not running anyone's code in the browser.
//
// Where the items appear, and where they do not:
//
//   - Widgets wait in the dashboard's "Add widget" list. Installing a plugin
//     does not rearrange a dashboard the operator built.
//   - Sections open from the Extensions screen and the command palette. They
//     stay out of the sidebar tree, which is the operator's model of the
//     fleet.
//   - Nothing a plugin declares can act. Documents are read-only; a row with
//     a button in it is a later stage, with its own consent step.

import { api } from '../api.js';
import { select } from '../app.js';
import { icon } from '../icons.js';
import { registerCapability } from '../ui/capabilities.js';
import { esc } from '../ui/dom.js';
import { pollWhileVisible } from '../ui/visibility.js';

// ── Data ─────────────────────────────────────────────────────────

let declared = [];

// The address of an item: plugin, then id. Both are checked on the server
// against a pattern that allows no slash, so the split is unambiguous.
const itemKey = (plugin, id) => `${plugin}/${id}`;

function findSection(key) {
  for (const p of declared) {
    for (const s of p.sections || []) if (itemKey(p.name, s.id) === key) return { plugin: p.name, item: s };
  }
  return null;
}

async function fetchDocument(plugin, id) {
  return (await api(`/api/plugins/${encodeURIComponent(plugin)}/ui/${encodeURIComponent(id)}`)).document;
}

// ── Drawing a document ───────────────────────────────────────────

const STATES = new Set(['ok', 'warn', 'bad', 'muted']);

export function documentHTML(doc) {
  if (!doc) return '<p class="muted">The plugin sent nothing to show.</p>';
  switch (doc.kind) {
    case 'rows':
      if (!doc.rows?.length) return '<p class="muted">Nothing to report.</p>';
      return `<table class="pdoc-rows"><tbody>${doc.rows.map((r) => {
        const tone = STATES.has(r.state) ? r.state : '';
        return `<tr><td class="muted">${esc(r.label)}</td>
          <td class="${tone ? `pdoc-${tone}` : ''}">${esc(r.value)}</td></tr>`;
      }).join('')}</tbody></table>`;
    case 'table':
      // A table's rows are lists of cells; a rows document's are label/value.
      if (!doc.rows?.length) return '<p class="muted">Nothing to report.</p>';
      return `<div class="pdoc-scroll"><table class="pdoc-table">
        <thead><tr>${(doc.columns || []).map((c) => `<th>${esc(c)}</th>`).join('')}</tr></thead>
        <tbody>${doc.rows.map((row) => `<tr>${row.map((c) => `<td>${esc(c)}</td>`).join('')}</tr>`).join('')}</tbody>
      </table></div>`;
    case 'message':
      return `<p class="pdoc-message">${esc(doc.text)}</p>`;
    default:
      // The server refuses any other kind, so this is a corral older than the
      // server it talks to. Say so rather than guess.
      return `<p class="muted">This page cannot show a “${esc(doc.kind)}” document. Reload to pick up a newer corral.</p>`;
  }
}

// Fills `el` with an item's document, and again on the item's interval while
// the page is visible and `el` is on it.
function showItem(el, plugin, item) {
  let shown = false;
  const draw = async () => {
    try {
      const doc = await fetchDocument(plugin, item.id);
      if (!el.isConnected) return;
      el.innerHTML = documentHTML(doc);
      el.dataset.pdocState = 'ok';
    } catch (e) {
      if (!el.isConnected) return;
      // Keep a document already on screen; a passing failure should not blank
      // a widget the operator is reading. Mark it stale instead.
      if (shown) {
        el.dataset.pdocState = 'stale';
        const note = el.querySelector('.pdoc-stale') ||
          el.insertAdjacentElement('afterbegin', Object.assign(document.createElement('p'), { className: 'pdoc-stale muted' }));
        note.textContent = `Not updated: ${e.message}`;
        return;
      }
      el.dataset.pdocState = 'error';
      el.innerHTML = `<div class="pdoc-error"><p><strong>corral-${esc(plugin)} did not answer.</strong></p>
        <p class="muted">${esc(e.message)}</p>
        <button type="button" class="btn sm pdoc-retry">Try again</button></div>`;
      el.querySelector('.pdoc-retry').onclick = () => { el.innerHTML = '<p class="muted">loading…</p>'; draw(); };
      return;
    }
    shown = true;
  };
  el.innerHTML = '<p class="muted">loading…</p>';
  if (item.refresh_ms > 0) pollWhileVisible(draw, item.refresh_ms, () => el.isConnected);
  else draw();
}

// ── The section screen ───────────────────────────────────────────

function renderSection(main, key) {
  const found = findSection(key);
  if (!found) {
    main.innerHTML = `<div class="page-head"><h1>${icon('extension')} Not available</h1></div>
      <p class="muted">No installed plugin offers this screen any more. See Extensions for what is installed.</p>`;
    return;
  }
  // A poll re-renders the content pane when anything changes. The document
  // has its own interval, so leave a section that is already showing alone.
  if (main.querySelector(`.pdoc-section[data-key="${CSS.escape(key)}"]`)) return;
  const { plugin, item } = found;
  main.innerHTML = `<div class="page-head"><h1>${icon(item.icon || 'extension')} ${esc(item.title)}</h1></div>
    <p class="muted pdoc-source">From <code>corral-${esc(plugin)}</code>. Read-only: a plugin can show information here, not act on the fleet.</p>
    <div class="pdoc-section" data-key="${esc(key)}"></div>`;
  showItem(main.querySelector('.pdoc-section'), plugin, item);
}

// ── Widgets ──────────────────────────────────────────────────────

function widgetsFor(scope) {
  // Datacenter only. A node screen is about one machine, and nothing in the
  // contract tells a plugin which machine it is being asked about.
  if (scope) return {};
  const out = {};
  for (const p of declared) {
    for (const w of p.widgets || []) {
      out[`plugin:${itemKey(p.name, w.id)}`] = {
        title: w.title, w: 4, h: 3,
        // Live: the widget keeps its own interval, so the dashboard's refresh
        // on every poll does not refetch it.
        live: true,
        render(body) {
          body.innerHTML = '<div class="pdoc-widget"></div>';
          showItem(body.firstElementChild, p.name, w);
        },
      };
    }
  }
  return out;
}

// ── Registration ─────────────────────────────────────────────────

registerCapability({
  name: 'plugin-ui',

  async load() {
    declared = (await api('/api/plugins/ui')).plugins || [];
  },
  // A failed load keeps the declarations already held. They change only on
  // install or removal, and dropping them would drop the plugin's widgets
  // from a dashboard that is mounting at that moment.
  fingerprint() { return declared; },

  screens: { pluginui: renderSection },

  widgets: widgetsFor,

  paletteEntries() {
    return declared.flatMap((p) => (p.sections || []).map((s) => ({
      id: `pluginui:${itemKey(p.name, s.id)}`,
      icon: s.icon || 'extension',
      label: s.title,
      sub: `from corral-${p.name}`,
      keywords: `plugin extension ${p.name}`,
      run: () => select({ type: 'pluginui', key: itemKey(p.name, s.id) }),
    })));
  },
});

// What the plugins have added, for the Extensions screen.
export function pluginContributions() {
  return declared;
}

// Opens a section. The Extensions screen calls this.
export function openSection(plugin, id) {
  select({ type: 'pluginui', key: itemKey(plugin, id) });
}
