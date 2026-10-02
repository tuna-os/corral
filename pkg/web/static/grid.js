// Small, dependency-free data grid used by Corral's inventory views.
// State is browser-local: no layout or filter preference is sent to the API.

const storageKey = (id) => `corral-grid:${id}`;
const clamp = (n, lo, hi) => Math.max(lo, Math.min(hi, n));

function load(id, columns) {
  const fallback = { order: columns.map((c) => c.id), hidden: [], widths: {}, sort: [], filters: {} };
  try {
    const value = JSON.parse(localStorage.getItem(storageKey(id)) || 'null');
    if (!value) return fallback;
    const valid = new Set(columns.map((c) => c.id));
    const order = [...new Set((value.order || []).filter((x) => valid.has(x)))];
    columns.forEach((c) => { if (!order.includes(c.id)) order.push(c.id); });
    return {
      ...fallback, ...value, order,
      hidden: (value.hidden || []).filter((x) => valid.has(x)),
      sort: (value.sort || []).filter((x) => valid.has(x.id)),
      filters: value.filters || {}, widths: value.widths || {},
    };
  } catch { return fallback; }
}

function save(id, state) {
  try { localStorage.setItem(storageKey(id), JSON.stringify(state)); } catch { /* private mode */ }
}

function compare(a, b) {
  if (a == null) return b == null ? 0 : -1;
  if (b == null) return 1;
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' });
}

function csvValue(value) {
  return `"${String(value ?? '').replaceAll('"', '""')}"`;
}

export function mountGrid(host, options) {
  if (!host) return;
  const { id, columns, rows, rowKey, onRowClick, selected = new Set(), onSelectionChange } = options;
  const state = load(id, columns);
  const byID = new Map(columns.map((c) => [c.id, c]));
  let dragID = '';
  let viewportStart = 0;

  host.className = 'data-grid';
  host.dataset.grid = id;
  host.innerHTML = `<div class="grid-toolbar">
      <details class="grid-columns"><summary class="btn sm">Columns</summary><div class="grid-menu"></div></details>
      <details class="grid-views"><summary class="btn sm">Views</summary><div class="grid-menu grid-view-menu">
        <label>View name <input class="grid-view-name" maxlength="40"></label>
        <button type="button" class="btn sm grid-view-save">Save current</button>
        <div class="grid-view-list"></div>
      </div></details>
      <button type="button" class="btn sm grid-export">Export CSV</button>
      <span class="grid-result-count muted" aria-live="polite"></span>
    </div><div class="grid-scroll"><table><colgroup></colgroup><thead></thead><tbody></tbody></table></div>`;

  const scroll = host.querySelector('.grid-scroll');
  const table = host.querySelector('table');
  const head = host.querySelector('thead');
  const body = host.querySelector('tbody');
  const colgroup = host.querySelector('colgroup');

  const visibleColumns = () => state.order.filter((x) => !state.hidden.includes(x)).map((x) => byID.get(x)).filter(Boolean);
  const valueFor = (row, col) => col.value ? col.value(row) : row[col.id];
  const filteredRows = () => {
    let result = rows.filter((row) => columns.every((col) => {
      const needle = String(state.filters[col.id] || '').trim().toLocaleLowerCase();
      return !needle || String(valueFor(row, col) ?? '').toLocaleLowerCase().includes(needle);
    }));
    if (state.sort.length) result = [...result].sort((a, b) => {
      for (const item of state.sort) {
        const col = byID.get(item.id);
        const order = compare(valueFor(a, col), valueFor(b, col));
        if (order) return item.dir === 'desc' ? -order : order;
      }
      return 0;
    });
    return result;
  };

  function setSort(columnID, append) {
    const existing = state.sort.find((x) => x.id === columnID);
    const next = existing ? (existing.dir === 'asc' ? 'desc' : null) : 'asc';
    if (!append) state.sort = [];
    else state.sort = state.sort.filter((x) => x.id !== columnID);
    if (next) state.sort.push({ id: columnID, dir: next });
    save(id, state);
    render();
  }

  function move(columnID, delta) {
    const at = state.order.indexOf(columnID);
    const to = clamp(at + delta, 0, state.order.length - 1);
    if (at === to) return;
    state.order.splice(at, 1);
    state.order.splice(to, 0, columnID);
    save(id, state);
    render();
  }

  function resize(columnID, width) {
    state.widths[columnID] = clamp(Math.round(width), 64, 600);
    save(id, state);
    render();
  }

  function renderColumnMenu() {
    const menu = host.querySelector('.grid-columns .grid-menu');
    menu.replaceChildren();
    state.order.forEach((columnID, index) => {
      const col = byID.get(columnID);
      const row = document.createElement('div');
      row.className = 'grid-column-option';
      const checked = !state.hidden.includes(columnID);
      row.innerHTML = `<label><input type="checkbox" ${checked ? 'checked' : ''}> <span></span></label>
        <span class="grid-column-actions">
          <button type="button" title="Move left" aria-label="Move ${col.label} left">←</button>
          <button type="button" title="Move right" aria-label="Move ${col.label} right">→</button>
          <button type="button" title="Narrow column" aria-label="Narrow ${col.label}">−</button>
          <button type="button" title="Widen column" aria-label="Widen ${col.label}">+</button>
        </span>`;
      row.querySelector('span').textContent = col.label;
      const buttons = row.querySelectorAll('button');
      row.querySelector('input').onchange = (event) => {
        state.hidden = event.target.checked ? state.hidden.filter((x) => x !== columnID) : [...state.hidden, columnID];
        save(id, state); render();
      };
      buttons[0].disabled = index === 0; buttons[0].onclick = () => move(columnID, -1);
      buttons[1].disabled = index === state.order.length - 1; buttons[1].onclick = () => move(columnID, 1);
      buttons[2].onclick = () => resize(columnID, (state.widths[columnID] || col.width || 140) - 20);
      buttons[3].onclick = () => resize(columnID, (state.widths[columnID] || col.width || 140) + 20);
      menu.appendChild(row);
    });
  }

  function views() {
    try { return JSON.parse(localStorage.getItem(`${storageKey(id)}:views`) || '{}'); } catch { return {}; }
  }
  function saveViews(value) {
    try { localStorage.setItem(`${storageKey(id)}:views`, JSON.stringify(value)); } catch { /* private mode */ }
  }
  function renderViews() {
    const list = host.querySelector('.grid-view-list');
    list.replaceChildren();
    Object.keys(views()).sort().forEach((name) => {
      const row = document.createElement('div');
      row.className = 'grid-saved-view';
      const apply = document.createElement('button');
      apply.type = 'button'; apply.className = 'grid-view-apply'; apply.textContent = name;
      apply.onclick = () => { Object.assign(state, views()[name]); save(id, state); render(); };
      const remove = document.createElement('button');
      remove.type = 'button'; remove.title = `Delete ${name}`; remove.setAttribute('aria-label', `Delete saved view ${name}`); remove.textContent = '×';
      remove.onclick = () => { const all = views(); delete all[name]; saveViews(all); renderViews(); };
      row.append(apply, remove); list.appendChild(row);
    });
  }
  host.querySelector('.grid-view-save').onclick = () => {
    const input = host.querySelector('.grid-view-name');
    const name = input.value.trim();
    if (!name) { input.focus(); return; }
    const all = views(); all[name] = JSON.parse(JSON.stringify(state)); saveViews(all); input.value = ''; renderViews();
  };

  // Update checkbox state in place: re-rendering would replace the element
  // the user just toggled and lose its focus.
  function syncSelection() {
    body.querySelectorAll('tr[data-key]').forEach((tr) => {
      const box = tr.querySelector('td.check input');
      if (box) box.checked = selected.has(tr.dataset.key);
    });
    const all = head.querySelector('th.check input');
    if (!all) return;
    const keys = filteredRows().map(rowKey);
    all.checked = keys.length > 0 && keys.every((key) => selected.has(key));
    all.indeterminate = keys.some((key) => selected.has(key)) && !all.checked;
  }

  function render() {
    const cols = visibleColumns();
    const result = filteredRows();
    host.querySelector('.grid-result-count').textContent = `${result.length} of ${rows.length}`;
    colgroup.replaceChildren();
    const selectCol = document.createElement('col'); selectCol.style.width = '38px'; colgroup.appendChild(selectCol);
    cols.forEach((col) => { const el = document.createElement('col'); el.style.width = `${state.widths[col.id] || col.width || 140}px`; colgroup.appendChild(el); });

    head.replaceChildren();
    const labels = document.createElement('tr');
    const selectHead = document.createElement('th'); selectHead.className = 'check';
    const all = document.createElement('input'); all.type = 'checkbox'; all.className = options.checkAllClass || 'grid-check-all'; all.title = 'Select all visible rows'; all.setAttribute('aria-label', 'Select all visible rows');
    const keys = result.map(rowKey); all.checked = keys.length > 0 && keys.every((key) => selected.has(key)); all.indeterminate = keys.some((key) => selected.has(key)) && !all.checked;
    all.onchange = () => { keys.forEach((key) => all.checked ? selected.add(key) : selected.delete(key)); onSelectionChange?.(selected); syncSelection(); };
    selectHead.appendChild(all); labels.appendChild(selectHead);
    cols.forEach((col) => {
      const th = document.createElement('th'); th.draggable = true; th.dataset.column = col.id;
      th.addEventListener('dragstart', () => { dragID = col.id; });
      th.addEventListener('dragover', (event) => event.preventDefault());
      th.addEventListener('drop', (event) => { event.preventDefault(); const from = state.order.indexOf(dragID); const to = state.order.indexOf(col.id); if (from >= 0 && to >= 0 && from !== to) { state.order.splice(from, 1); state.order.splice(to, 0, dragID); save(id, state); render(); } });
      const button = document.createElement('button'); button.type = 'button'; button.className = 'grid-sort';
      const sortAt = state.sort.findIndex((x) => x.id === col.id); const sort = state.sort[sortAt];
      button.textContent = `${col.label}${sort ? ` ${sort.dir === 'asc' ? '▲' : '▼'}${state.sort.length > 1 ? sortAt + 1 : ''}` : ''}`;
      button.title = 'Sort; hold Shift to add another column'; button.onclick = (event) => setSort(col.id, event.shiftKey);
      const handle = document.createElement('span'); handle.className = 'grid-resizer'; handle.setAttribute('role', 'separator'); handle.tabIndex = 0; handle.setAttribute('aria-label', `Resize ${col.label}`);
      handle.onkeydown = (event) => { if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); resize(col.id, (state.widths[col.id] || col.width || 140) + (event.key === 'ArrowLeft' ? -10 : 10)); } };
      handle.onpointerdown = (event) => { event.preventDefault(); const startX = event.clientX; const startWidth = state.widths[col.id] || th.getBoundingClientRect().width; const move = (e) => { state.widths[col.id] = clamp(startWidth + e.clientX - startX, 64, 600); const target = [...colgroup.children][cols.indexOf(col) + 1]; target.style.width = `${state.widths[col.id]}px`; }; const up = () => { document.removeEventListener('pointermove', move); document.removeEventListener('pointerup', up); save(id, state); renderColumnMenu(); }; document.addEventListener('pointermove', move); document.addEventListener('pointerup', up); };
      th.append(button, handle); labels.appendChild(th);
    });
    const filters = document.createElement('tr'); filters.className = 'grid-filters'; filters.appendChild(document.createElement('th'));
    cols.forEach((col) => { const th = document.createElement('th'); const input = document.createElement('input'); input.type = 'search'; input.placeholder = `Filter ${col.label}`; input.setAttribute('aria-label', `Filter ${col.label}`); input.value = state.filters[col.id] || ''; input.oninput = () => { state.filters[col.id] = input.value; save(id, state); viewportStart = 0; render(); requestAnimationFrame(() => host.querySelector(`[aria-label="Filter ${CSS.escape(col.label)}"]`)?.focus()); }; th.appendChild(input); filters.appendChild(th); });
    head.append(labels, filters);

    body.replaceChildren();
    const virtual = result.length > 500;
    const rowHeight = 39;
    const count = virtual ? Math.ceil(scroll.clientHeight / rowHeight) + 8 : result.length;
    const shown = virtual ? result.slice(viewportStart, viewportStart + count) : result;
    if (virtual && viewportStart) { const spacer = document.createElement('tr'); spacer.className = 'grid-spacer'; spacer.style.height = `${viewportStart * rowHeight}px`; body.appendChild(spacer); }
    shown.forEach((row) => {
      const tr = document.createElement('tr'); tr.dataset.key = rowKey(row); tr.tabIndex = 0;
      tr.onclick = (event) => { if (!event.target.closest('.check')) onRowClick?.(row); };
      tr.onkeydown = (event) => { if (event.key === 'Enter') onRowClick?.(row); };
      const checkCell = document.createElement('td'); checkCell.className = 'check'; const check = document.createElement('input'); check.type = 'checkbox'; check.className = options.checkClass || 'grid-check'; check.checked = selected.has(rowKey(row)); check.setAttribute('aria-label', `Select ${rowKey(row)}`); check.onchange = () => { check.checked ? selected.add(rowKey(row)) : selected.delete(rowKey(row)); onSelectionChange?.(selected); syncSelection(); }; checkCell.appendChild(check); tr.appendChild(checkCell);
      options.decorateRow?.(tr, row);
      cols.forEach((col) => { const td = document.createElement('td'); const rendered = col.render?.(row); if (rendered instanceof Node) td.appendChild(rendered); else td.textContent = rendered ?? valueFor(row, col) ?? ''; tr.appendChild(td); });
      body.appendChild(tr);
    });
    if (virtual && viewportStart + shown.length < result.length) { const spacer = document.createElement('tr'); spacer.className = 'grid-spacer'; spacer.style.height = `${(result.length - viewportStart - shown.length) * rowHeight}px`; body.appendChild(spacer); }
    table.setAttribute('aria-rowcount', String(result.length));
    renderColumnMenu(); renderViews();
  }

  scroll.onscroll = () => { if (rows.length <= 500) return; const next = Math.max(0, Math.floor(scroll.scrollTop / 39) - 3); if (next !== viewportStart) { viewportStart = next; render(); } };
  host.querySelector('.grid-export').onclick = () => {
    const cols = visibleColumns(); const result = filteredRows();
    const csv = [cols.map((c) => csvValue(c.label)).join(','), ...result.map((row) => cols.map((c) => csvValue(valueFor(row, c))).join(','))].join('\r\n');
    const link = document.createElement('a'); link.href = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' })); link.download = `${id}.csv`; link.click(); setTimeout(() => URL.revokeObjectURL(link.href), 0);
  };
  render();
}
