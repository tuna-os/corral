// Content router: extensible screen selection and dispatch.
//
// Extracted from app.js (#605). Each content screen registers its renderer
// with registerScreen(type, renderer), and router.dispatch() renders whatever
// screen state.selected names into #content.

import { emit, state } from './state.js';
import { capabilityScreen } from './ui/capabilities.js';
import { $ } from './ui/dom.js';
import { decodeSelection, encodeSelection, readRoute, writeRoute } from './ui/route.js';

const defaultScreens = new Map();
function getScreens() {
  if (typeof globalThis !== 'undefined') {
    if (!globalThis.__corralScreens) globalThis.__corralScreens = new Map();
    return globalThis.__corralScreens;
  }
  return defaultScreens;
}

export function registerScreen(type, renderer) {
  getScreens().set(type, renderer);
}

export class Router {
  constructor({
    target = () => $('#content'),
    defaultScreen = 'dc',
    appState = state,
    screenMap = getScreens(),
    onRenderTree = null,
    getTreeView = null,
    onSetTreeView = null,
    getGridAddress = null,
    onApplyGridAddress = null,
  } = {}) {
    this.screens = screenMap;
    this.target = typeof target === 'function' ? target : () => target;
    this.defaultScreen = defaultScreen;
    this.state = appState;
    this.lastParentNode = '';
    this.onMarkRendered = null;
    this.onDisconnectConsoles = null;
    this.onRenderTree = onRenderTree;
    this.getTreeView = getTreeView;
    this.onSetTreeView = onSetTreeView;
    this.getGridAddress = getGridAddress;
    this.onApplyGridAddress = onApplyGridAddress;
  }

  registerScreen(type, renderer) {
    this.screens.set(type, renderer);
  }

  getScreen(type) {
    return this.screens.get(type) || capabilityScreen(type) || null;
  }

  // Where to go when the thing on screen stops existing.
  //
  // A guest can vanish under you: somebody deletes it, or a migration finishes
  // and it is gone from the node you were watching. Proxmox walks up the parent
  // chain and selects the nearest ancestor that still exists.
  fallbackSelection(nodes = this.state.nodes || []) {
    if (this.lastParentNode && nodes.some((n) => n.name === this.lastParentNode)) {
      return { type: 'node', name: this.lastParentNode };
    }
    return { type: this.defaultScreen };
  }

  renderContent() {
    const main = this.target();
    this.syncRoute();

    const selected = this.state.selected || { type: this.defaultScreen };
    const renderer = this.getScreen(selected.type);

    if (renderer) {
      return renderer(main, selected, this);
    }

    // Default fallback (e.g. datacenter)
    const defaultRenderer = this.getScreen(this.defaultScreen);
    if (defaultRenderer) {
      return defaultRenderer(main, { type: this.defaultScreen }, this);
    }
  }

  select(sel, openTab = 'summary') {
    this.onDisconnectConsoles?.();
    if (this.state.selected?.type === 'multiview' && sel?.type !== 'multiview') {
      import('./content/multiview.js').then((m) => m.disconnectMultiview?.());
    }
    this.state.selected = sel;
    this.state.tab = openTab;
    if (this.onRenderTree) this.onRenderTree();
    this.renderContent();
    if (this.onMarkRendered) this.onMarkRendered();
    emit('select', { selected: this.state.selected, tab: this.state.tab });
  }

  syncRoute() {
    if (typeof document !== 'undefined' && document.body?.classList?.contains('console-popout')) return;
    const view = this.getTreeView ? this.getTreeView() : undefined;
    const grid = this.getGridAddress ? this.getGridAddress() : undefined;
    writeRoute({
      view,
      sel: encodeSelection(this.state.selected),
      tab: this.state.tab,
      grid,
    });
  }

  applyRoute() {
    const route = readRoute();
    if (this.onSetTreeView && this.getTreeView && route.view && route.view !== this.getTreeView()) {
      this.onSetTreeView(route.view);
    }
    const selected = decodeSelection(route.sel);
    this.onDisconnectConsoles?.();
    this.state.selected = selected;
    this.state.tab = route.tab;
    if (this.onRenderTree) this.onRenderTree();
    this.renderContent();
    if (this.onApplyGridAddress && route.grid) {
      this.onApplyGridAddress(route.grid);
    }
    if (this.onMarkRendered) this.onMarkRendered();
    emit('select', { selected: this.state.selected, tab: this.state.tab });
  }
}

export const router = new Router();

export function renderContent() {
  return router.renderContent();
}

export function select(sel, openTab = 'summary') {
  return router.select(sel, openTab);
}

export function syncRoute() {
  return router.syncRoute();
}

export function applyRoute() {
  return router.applyRoute();
}
