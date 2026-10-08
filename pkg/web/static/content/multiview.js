// Multiview screen: several live VNC consoles in one persisted grid.

import { vmKey } from '../api.js';
import { renderContent, select } from '../app.js';
import { wsURL } from '../console.js';
import { icon } from '../icons.js';
import { state } from '../state.js';
import { $, esc } from '../ui/dom.js';

// ── Multiview: persisted, rearrangeable live consoles ─────────────
// Preset sizes make resizing keyboard-accessible; pointer users can also drag
// a tile by its title. Arrow buttons are the equivalent of that drag action.

let multiviewRFBs = [];
const MULTIVIEW_KEY = 'corral.multiview.v1';

// The connected tile grid, kept across renders. disconnectMultiview() owns
// teardown, so it is also what invalidates this: once the connections are gone
// the element holding them is a grid of dead canvases.
let liveGrid = null;
let liveGridKey = '';

export function disconnectMultiview() {
  for (const r of multiviewRFBs) { try { r.disconnect(); } catch { /* gone */ } }
  multiviewRFBs = [];
  liveGrid = null;
  liveGridKey = '';
}

function loadMultiviewLayout() {
  try { return JSON.parse(localStorage.getItem(MULTIVIEW_KEY) || '{}'); }
  catch { return {}; }
}

function saveMultiviewLayout(layout) {
  try { localStorage.setItem(MULTIVIEW_KEY, JSON.stringify(layout)); } catch { /* private mode */ }
}

function moveMultiviewTile(grid, tile, delta, layout) {
  const tiles = [...grid.querySelectorAll('.mv-tile')];
  const from = tiles.indexOf(tile);
  const to = Math.max(0, Math.min(tiles.length - 1, from + delta));
  if (from === to) return;
  if (to < from) grid.insertBefore(tile, tiles[to]);
  else grid.insertBefore(tile, tiles[to].nextSibling);
  layout.order = [...grid.querySelectorAll('.mv-tile')].map((el) => el.dataset.key);
  saveMultiviewLayout(layout);
  tile.querySelector('.mv-title').focus();
}

export async function renderMultiview(main) {
  const running = state.vms.filter((v) => v.running);
  const layout = loadMultiviewLayout();
  const rank = new Map((layout.order || []).map((key, i) => [key, i]));
  const shown = running.slice(0, 6).sort((a, b) =>
    (rank.get(vmKey(a)) ?? 999) - (rank.get(vmKey(b)) ?? 999));

  // Which VMs are on screen, independent of the order they sit in: dragging a
  // tile rearranges the DOM directly, and treating that as a change would tear
  // down every connection on the next poll as a reward for rearranging them.
  const want = shown.map(vmKey).slice().sort().join(',');
  // Only the grid is kept. The heading counts running VMs, which can change
  // while the six on screen do not, so it is rebuilt either way.
  const reuse = !!liveGrid && liveGridKey === want && shown.length > 0;
  if (!reuse) disconnectMultiview();

  main.innerHTML = `
    <div class="page-head"><h1>${icon('cube')} Multiview</h1>
      <span class="muted">${running.length} running VM${running.length === 1 ? '' : 's'}${running.length > 6 ? ' — showing first 6' : ''}</span>
    </div>
    ${shown.length ? `<p class="muted mv-help">Drag titles to rearrange, or use Move and Size. Layout is saved in this browser.</p>
      <div id="mv-grid" class="mv-grid"></div>`
      : `<p class="console-msg">No running VMs. Start some and they appear here, live.</p>`}`;
  if (!shown.length) return;

  if (reuse) {
    // The tiles are already connected; put them back and leave them alone.
    $('#mv-grid').replaceWith(liveGrid);
    return;
  }

  let RFB;
  try {
    ({ default: RFB } = await import('../vendor/novnc-rfb.esm.js'));
  } catch (e) {
    $('#mv-grid').innerHTML = `<p class="console-msg">noVNC failed to load: ${esc(e.message)}</p>`;
    return;
  }
  const grid = $('#mv-grid');
  let dragged = null;
  for (const vm of shown) {
    const key = vmKey(vm);
    const tile = document.createElement('section');
    tile.className = `mv-tile mv-${layout.sizes?.[key] || 'normal'}`;
    tile.dataset.key = key;
    tile.innerHTML = `<div class="mv-title" draggable="true" tabindex="0" aria-label="${esc(vm.name)} console tile; drag to rearrange">
        <button class="mv-open" title="Open console tab">${esc(vm.name)}</button>
        <span class="muted">${esc(vm.namespace)}</span><span class="spacer"></span>
        <button class="btn xs mv-left" aria-label="Move ${esc(vm.name)} left">←</button>
        <button class="btn xs mv-right" aria-label="Move ${esc(vm.name)} right">→</button>
        <label class="mv-size-label">Size <select class="mv-size" aria-label="Resize ${esc(vm.name)} tile">
          <option value="normal">Normal</option><option value="wide">Wide</option>
          <option value="tall">Tall</option><option value="large">Large</option>
        </select></label>
      </div><div class="mv-screen"></div>`;
    tile.querySelector('.mv-size').value = layout.sizes?.[key] || 'normal';
    tile.querySelector('.mv-open').onclick = () => {
      disconnectMultiview();
      select({ type: 'vm', key });
      state.tab = 'console';
      renderContent();
    };
    tile.querySelector('.mv-left').onclick = () => moveMultiviewTile(grid, tile, -1, layout);
    tile.querySelector('.mv-right').onclick = () => moveMultiviewTile(grid, tile, 1, layout);
    tile.querySelector('.mv-size').onchange = (e) => {
      tile.className = `mv-tile mv-${e.target.value}`;
      layout.sizes ||= {};
      layout.sizes[key] = e.target.value;
      saveMultiviewLayout(layout);
    };
    const title = tile.querySelector('.mv-title');
    title.ondragstart = () => { dragged = tile; tile.classList.add('dragging'); };
    title.ondragend = () => { dragged = null; tile.classList.remove('dragging'); };
    tile.ondragover = (e) => { if (dragged && dragged !== tile) e.preventDefault(); };
    tile.ondrop = (e) => {
      e.preventDefault();
      if (!dragged || dragged === tile) return;
      const tiles = [...grid.querySelectorAll('.mv-tile')];
      moveMultiviewTile(grid, dragged, tiles.indexOf(tile) - tiles.indexOf(dragged), layout);
    };
    grid.appendChild(tile);
    try {
      const tileRFB = new RFB(tile.querySelector('.mv-screen'), wsURL('vnc', vm));
      tileRFB.viewOnly = false;
      tileRFB.scaleViewport = true;
      tile.querySelector('.mv-screen').onclick = () => tileRFB.focus();
      tileRFB.addEventListener('disconnect', () => {
        tile.querySelector('.mv-screen').innerHTML = `<p class="console-msg">disconnected</p>`;
      });
      multiviewRFBs.push(tileRFB);
    } catch {
      tile.querySelector('.mv-screen').innerHTML = `<p class="console-msg">connect failed</p>`;
    }
  }
  layout.order = shown.map(vmKey);
  saveMultiviewLayout(layout);
  // Remember the grid the connections live in, so the next poll renders the
  // heading around it instead of tearing all six down and dialling again.
  liveGrid = grid;
  liveGridKey = want;
}