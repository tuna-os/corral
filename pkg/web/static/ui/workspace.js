// Workspace layout: the sidebar width, the dock height, and whether either is
// collapsed. All three are operator preferences (#290, #341), so all three are
// remembered per browser and all three go through one primitive in
// ui/splitter.js rather than a bespoke handler each. Also the mobile drawer,
// which is the sidebar on a narrow screen.

import { $, toast } from './dom.js';
import { makeCollapsible, makeSplitter } from './splitter.js';

const TREE_WIDTH_DEFAULT = 270;
const TREE_WIDTH_MIN = 180;
const DOCK_HEIGHT_DEFAULT = 220;
const DOCK_HEIGHT_MIN = 90;

let treeCollapse = null;
const splitters = [];

/**
 * Put the workspace back to its shipped layout.
 *
 * Every size and collapse state here is remembered per browser, which is the
 * point — and also the trap. The vSphere Web Client is the cautionary example:
 * admins who closed its Recent Tasks pane had no way back, and the vendor's own
 * advice was to clear the browser cache. A layout you can customise needs a way
 * to undo the customisation, so this is the same "Reset layout" the dashboard
 * widgets already offer, for the workspace itself.
 */
export function resetWorkspaceLayout() {
  for (const sp of splitters) sp.reset();
  if (treeCollapse) treeCollapse.toggle(false); // a hidden pane is the thing hardest to get back
  // The dock is an Alpine island and owns its own open state, so it is asked
  // rather than reached into.
  document.dispatchEvent(new CustomEvent('corral:reset-layout'));
  toast('Workspace layout reset');
}

/** Collapse or restore the sidebar. Exported for the header button. */
export function toggleTree(force) {
  return treeCollapse ? treeCollapse.toggle(force) : false;
}

/** Close the mobile drawer, after a pick in it. */
export function closeDrawer() { $('#tree').classList.remove('open'); }

/** Restore the remembered layout and bind its handles. Called once at boot. */
export function initWorkspace() {
  const tree = $('#tree');
  const treeHandle = $('#tree-resizer');
  if (tree && treeHandle) {
    // Collapsing is a body class so the stylesheet owns what it looks like;
    // this module only owns the input that flips it.
    treeCollapse = makeCollapsible({
      className: 'tree-collapsed',
      storageKey: 'corral.treeCollapsed',
    });
    splitters.push(makeSplitter({
      handle: treeHandle,
      axis: 'x',
      cssVar: '--tree-w',
      storageKey: 'corral.treeWidth',
      def: TREE_WIDTH_DEFAULT,
      min: TREE_WIDTH_MIN,
      max: () => Math.max(TREE_WIDTH_MIN, Math.round(window.innerWidth * 0.6)),
      sizeFromPointer: (ev) => ev.clientX - tree.getBoundingClientRect().left,
      current: () => tree.getBoundingClientRect().width,
      collapsible: treeCollapse,
    }));
  }

  const dockHandle = $('#dock-resizer');
  const dock = $('#task-panel');
  if (dockHandle && dock) {
    // The dock grows upward, so the pointer maps to the distance from the
    // bottom of the window rather than to a coordinate.
    splitters.push(makeSplitter({
      handle: dockHandle,
      axis: 'y',
      cssVar: '--dock-h',
      storageKey: 'corral.dockHeight',
      def: DOCK_HEIGHT_DEFAULT,
      min: DOCK_HEIGHT_MIN,
      max: () => Math.max(DOCK_HEIGHT_MIN, Math.round(window.innerHeight * 0.6)),
      sizeFromPointer: (ev) => window.innerHeight - ev.clientY,
      current: () => $('#task-panel-body')?.getBoundingClientRect().height || DOCK_HEIGHT_DEFAULT,
    }));
  }

  // The header's menu button: the drawer on a narrow screen, collapse on a
  // wide one.
  $('#btn-menu').onclick = () => {
    if (window.innerWidth <= 760) $('#tree').classList.toggle('open');
    else toggleTree();
  };
}
