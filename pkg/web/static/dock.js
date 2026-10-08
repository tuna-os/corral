// Bottom dock: the task panel and the cluster events panel (an Alpine.js
// island, see ADR-0004).

import { api, findVM } from './api.js';
import { emit, on, state } from './state.js';

// Whether the dock is open, how tall it is, and which panel is showing are all
// operator preferences, like the sidebar width. Without this the dock shuts on
// every reload and the height someone chose is invisible until they open it
// again.
const DOCK_OPEN_KEY = 'corral.dockOpen';
const DOCK_PANEL_KEY = 'corral.dockPanel';

// The panels the dock offers. A list rather than hand-written blocks of markup:
// the tab strip and its keyboard navigation are generated from it, so another
// panel is a line here plus its tabpanel, not a rewrite of the head.
//
// #350 asks for Tasks / Cluster log / Events. corral's task log *is* its
// cluster log — one ring buffer of every server-side action across all nodes —
// so listing it twice under two names would be two tabs showing one thing.
const PANELS = [
  { id: 'tasks', title: 'Tasks' },
  { id: 'events', title: 'Events' },
];

const storedCollapsed = () => {
  try { return localStorage.getItem(DOCK_OPEN_KEY) !== '1'; } catch { return true; }
};
const rememberOpen = (open) => {
  try { localStorage.setItem(DOCK_OPEN_KEY, open ? '1' : '0'); } catch { /* private mode */ }
};
const storedPanel = () => {
  try {
    const v = localStorage.getItem(DOCK_PANEL_KEY);
    return PANELS.some((p) => p.id === v) ? v : 'tasks';
  } catch { return 'tasks'; }
};
const rememberPanel = (id) => {
  try { localStorage.setItem(DOCK_PANEL_KEY, id); } catch { /* private mode */ }
};

// ── Task panel (Proxmox-style activity log) ────────────────────────
// First Alpine.js island — see docs/adr/0004-web-ui-alpinejs-no-build.md.
// The poll loop stays a plain setInterval (Alpine is for render, not
// fetching); only the DOM sync (row templating, collapse toggle) moved to
// x-data/x-for/x-show, replacing the old innerHTML-string templating.
//
// Registered via Alpine.data() inside an alpine:init listener, not a bare
// `window.taskPanel = ...` assignment — Alpine (a deferred classic script)
// can start scanning the DOM before this module script finishes running,
// so a plain global isn't reliably defined in time. alpine:init only fires
// when Alpine.start() actually runs (after all deferred/module scripts have
// executed), so listening for it is timing-safe regardless of script order.
document.addEventListener('alpine:init', () => {
  Alpine.data('taskPanel', () => ({
    collapsed: storedCollapsed(),
    panels: PANELS,
    panel: storedPanel(),
    tasks: [],
    summary: '',
    events: [],
    eventsFor: '',
    eventsError: '',
    _lastFp: '',

    start() {
      // Alpine has no hook for "a property changed" without a watcher, and the
      // header click is the only thing that flips it, so persist from there.
      this.$watch('collapsed', (v) => rememberOpen(!v));
      this.$watch('panel', (v) => rememberPanel(v));
      // "Reset layout" puts the workspace back to how it ships, and the dock
      // ships closed on Tasks. The watchers above persist both.
      document.addEventListener('corral:reset-layout', () => {
        this.collapsed = true;
        this.panel = 'tasks';
      });
      // Events are per-VM, so a new selection means a different list. Listening
      // on the shared bus rather than polling the selection keeps this to one
      // fetch per actual change.
      on('select', () => { if (this.panel === 'events') this.loadEvents(); });
      this.refresh();
      setInterval(() => this.refresh(), 5000);
    },

    /** Switch panels, opening the dock if it was shut. */
    pick(id) {
      this.panel = id;
      this.collapsed = false;
      if (id === 'events') this.loadEvents();
      // Focus follows selection, which is what makes the arrow keys feel like
      // tabs rather than like moving a cursor past them.
      this.$nextTick(() => document.getElementById(`dock-tab-${id}`)?.focus());
    },

    /** Arrow-key movement along the tab strip, wrapping at both ends. */
    move(delta) {
      const at = this.panels.findIndex((p) => p.id === this.panel);
      const next = this.panels[(at + delta + this.panels.length) % this.panels.length];
      if (next) this.pick(next.id);
    },

    async refresh() {
      if (this.panel === 'events') this.loadEvents();
      let log;
      try { log = await api('/api/tasklog'); } catch { return; }
      const fp = JSON.stringify(log);
      if (fp === this._lastFp) return; // unchanged — don't reset panel scroll
      this._lastFp = fp;
      this.tasks = log;
      emit('tasks', log);
      const running = log.filter((t) => t.status === 'running').length;
      const errors = log.filter((t) => t.status === 'error').length;
      this.summary = log.length
        ? `${running ? `${running} running · ` : ''}${errors ? `${errors} failed · ` : ''}${log.length} total`
        : '';
    },

    async loadEvents() {
      const vm = state.selected.type === 'vm' ? findVM(state.selected.key) : null;
      if (!vm) {
        this.events = [];
        this.eventsFor = '';
        this.eventsError = '';
        return;
      }
      this.eventsFor = vm.name;
      try {
        this.events = await api(`/api/vms/${vm.namespace}/${vm.name}/events`);
        this.eventsError = '';
      } catch (e) {
        // A backend with no Kubernetes behind it has no events to give, which
        // is not a failure worth a toast: the panel says so and the rest of the
        // dock carries on.
        this.events = [];
        this.eventsError = e.message;
      }
    },
  }));
});
