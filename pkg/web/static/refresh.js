// Fleet loading and rendering. Poll timing and render policy are independently
// testable in poll.js; routing is owned by router.js.
import { api } from './api.js';
import { renderIdentityLost, renderOffline } from './content/unavailable.js';
import { loadPools } from './pools.js';
import { singleFlight } from './poll.js';
import { pollingState, renderFingerprint } from './refresh-state.js';
import { applyInitialRoute, renderContent } from './router.js';
import { emit, state } from './state.js';
import { renderTree, treeView } from './tree.js';
import { loadCapabilityData } from './ui/capabilities.js';
import { $, toast } from './ui/dom.js';
import { interacting } from './ui/interaction.js';
import { activeContextMenu } from './ui/menu.js';

// The floor between polls. The real wait is this plus twice how long the last
// poll took; see startPolling() in poll.js.
export const POLL_MS = 5000;

// One refresh at a time, and one asked for meanwhile runs after it; see
// singleFlight() in poll.js. A declared function, so the modules that import
// refresh can hold it before this module's body has run.
const refreshSingle = singleFlight(refreshOnce);
export function refresh(force = false) {
  return refreshSingle(force);
}

async function refreshOnce(force = false) {
  try {
    state.vms = await api('/api/vms');
  } catch (e) {
    const notice = pollingState.failed(e);
    if (notice === 'identity') {
      renderTree();
      renderIdentityLost(() => refresh(true));
    } else if (notice === 'offline') {
      // Static screens, such as Extensions, remain usable without a fleet.
      renderTree();
      renderOffline(e.message, () => { pollingState.offlineShown = false; refresh(true); });
    } else if (notice === 'toast') {
      toast(`Refresh failed: ${e.message}`);
    }
    return;
  }
  // Check recovery even for an already-forced refresh.
  force = pollingState.recovered() || force;
  // Nodes are the cluster topology view; a local-only deployment (QEMU/Incus/
  // libvirt) has none, and a nodes failure must never blank a working VM list.
  try { state.nodes = await api('/api/nodes'); } catch { state.nodes = []; }
  // Each registered capability fetches its own data, and one that fails
  // cannot blank the rest of the page. See ui/capabilities.js.
  await loadCapabilityData();
  pollingState.offlineShown = false;
  if (treeView === 'pool') await loadPools();
  // The image catalogue changes far more slowly than the fleet and is only
  // needed by Storage View, so it is fetched while that view is showing rather
  // than on every poll. Best-effort: a storage failure must not blank the
  // fleet, exactly as a nodes failure must not.
  if (treeView === 'storage' || state.selected.type === 'storage') {
    try { state.images = await api('/api/images'); } catch { /* keep what we had */ }
    try { state.dataVolumes = await api('/api/datavolumes'); } catch { state.dataVolumes = []; }
  }
  try { state.cts = await api('/api/cts'); } catch { state.cts = []; } // best-effort — don't fail the whole refresh over CTs
  emit('inventory', { vms: state.vms, cts: state.cts, nodes: state.nodes });
  if (applyInitialRoute()) return;
  const fp = renderFingerprint();
  if (!pollingState.shouldRender(fp, { force, menu: !!activeContextMenu, interacting: interacting() })) return;

  // The tree reconciles its rows in place, so its scroll position, focus and
  // any in-flight drag survive on their own. The content pane still rebuilds
  // from markup, so it keeps the save-and-restore until it reconciles too.
  const contentEl = $('#content');
  const contentScroll = contentEl ? contentEl.scrollTop : 0;
  // A view rebuilds the markup around whatever the operator was using, which
  // blurs the focused element and resets the grid's own scroller. Both are
  // noted here because this is the last moment they can still be read, and put
  // back afterwards if the element outlived the render — the inventory grid is
  // reused rather than rebuilt, so its rows usually do.
  const gridScroller = contentEl?.querySelector('.grid-scroll');
  const gridScroll = gridScroller ? { top: gridScroller.scrollTop, left: gridScroller.scrollLeft } : null;
  const wasFocused = contentEl?.contains(document.activeElement) ? document.activeElement : null;
  renderTree();
  // Every screen that holds a live connection now carries its own element
  // across a render — the VM console tabs in content/vm.js, the Multiview grid
  // in content/multiview.js — so the pane is always safe to render and nothing
  // has to be skipped to protect it.
  renderContent();
  // Focus before the scroll, and without letting the focus move the view.
  //
  // Putting focus on an element scrolls it into view. Restoring the scroll
  // first and focusing second therefore undoes the scroll whenever the render
  // moved that element — rows added or removed above it — and the pane jumps
  // to the focused row instead of staying where the operator left it. Proxmox
  // carries an override on its grid view for this exact order, with the same
  // reason written on it. `preventScroll` says it outright, and the scroll
  // restore still comes after, so the operator's position always wins.
  //
  // Only if the render actually dropped focus: if something else has taken it
  // in the meantime, putting it back would steal it.
  if (wasFocused?.isConnected && document.activeElement === document.body) {
    wasFocused.focus({ preventScroll: true });
  }
  if (contentEl) contentEl.scrollTop = contentScroll;
  const scrollerNow = contentEl?.querySelector('.grid-scroll');
  if (scrollerNow && gridScroll) { scrollerNow.scrollTop = gridScroll.top; scrollerNow.scrollLeft = gridScroll.left; }
}
