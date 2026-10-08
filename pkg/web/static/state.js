// Shared state and event bus for the web UI modules (#340).
//
// Every module reads and writes the same `state` object instead of its own
// module-level copy, so a module never needs another module's globals. ES
// module bindings are read-only to importers; a property on a shared object
// is not, which is why the fleet lives here rather than in app.js.
//
// The bus carries the events the tree, the grid and the dock all react to:
//   select    — the content selection changed; detail is { selected, tab }
//   inventory — a poll replaced the fleet; detail is { vms, cts, nodes }
//   tasks     — the task log changed; detail is the task list
// A listener gets the detail object, and on() returns an unsubscribe function.

export const state = {
  vms: [],
  cts: [], // Containers (#50) — pet pods, not KubeVirt VMs
  nodes: [],
  // Power-manageable hosts from host-power plugins (sdk.CapHostPower). Empty
  // unless such a plugin is installed; core has no provider knowledge.
  hostPower: { hosts: [] },
  caps: { storageClass: '', canExpand: false, canSnapshot: false },
  // Authenticated tailnet identity + privilege (see /api/whoami). Defaults to
  // admin so the UI is fully enabled until told otherwise (single-user mode).
  me: { login: '', name: '', admin: true, enforced: false },
  availableNADs: [],
  selected: { type: 'dc' }, // {type:'dc'} | {type:'node',name} | {type:'vm',key}
  // One selection model backs both the inventory grid and sidebar tree.
  selectedVMKeys: new Set(),
  tab: 'summary',
  ctTab: 'summary',
};

const bus = new EventTarget();

export function on(event, fn) {
  const handler = (e) => fn(e.detail);
  bus.addEventListener(event, handler);
  return () => bus.removeEventListener(event, handler);
}

export function emit(event, detail) {
  bus.dispatchEvent(new CustomEvent(event, { detail }));
}
