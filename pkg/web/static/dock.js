// Bottom dock: the task panel (an Alpine.js island, see ADR-0004).

import { api } from './api.js';
import { emit } from './state.js';

// Whether the dock is open is an operator preference, like the sidebar width
// and the dock height. Without this the dock shuts on every reload and the
// height someone chose is invisible until they open it again.
const DOCK_OPEN_KEY = 'corral.dockOpen';
const storedCollapsed = () => {
  try { return localStorage.getItem(DOCK_OPEN_KEY) !== '1'; } catch { return true; }
};
const rememberOpen = (open) => {
  try { localStorage.setItem(DOCK_OPEN_KEY, open ? '1' : '0'); } catch { /* private mode */ }
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
    tasks: [],
    summary: '',
    _lastFp: '',

    start() {
      // Alpine has no hook for "a property changed" without a watcher, and the
      // header click is the only thing that flips it, so persist from there.
      this.$watch('collapsed', (v) => rememberOpen(!v));
      this.refresh();
      setInterval(() => this.refresh(), 5000);
    },

    async refresh() {
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
  }));
});