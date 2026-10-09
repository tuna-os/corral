// A registry of what a capability contributes to the UI.
//
// ADR-0007 keeps provider code out of core, and the Go side holds to that:
// core calls any plugin that declares a capability and knows nothing about the
// provider behind it. The UI did not hold to it. Host power, the one
// capability with a hook, was named in six core files — the tree built its
// rows, the Datacenter dashboard built its widget, the node context menu
// appended its actions, the alert list read its state, renderContent() knew
// its selection type and the poll fetched its endpoint. A second capability
// would have cost all of that again.
//
// A capability now registers itself, and core asks the registry. This is
// stage 0 of RFC-0002: the entries arrive at run time, and the contract that a
// plugin speaks does not change. Nothing here loads code from a plugin. The
// registered objects are part of the embedded UI, exactly as before.
//
// Every hook is optional. A capability implements the ones it has.

const registered = [];

export function registerCapability(cap) {
  registered.push(cap);
}

// For a screen that wants to say which capabilities are present.
export function capabilities() {
  return registered.slice();
}

// Each capability fetches its own data on the poll.
//
// One capability's failure must not blank the page, in the same way that a
// failure to list nodes must not blank a working VM list. So each load is
// isolated, and a capability that fails is asked to forget what it held
// rather than keep showing data that may be stale.
export async function loadCapabilityData() {
  await Promise.all(registered.map(async (cap) => {
    try {
      await cap.load?.();
    } catch {
      cap.clear?.();
    }
  }));
}

// What the poll compares to decide whether anything needs redrawing. A
// capability that leaves this out can never trigger a render of its own, which
// means its data appears late or not at all.
export function capabilityFingerprint() {
  return registered.map((cap) => cap.fingerprint?.() ?? null);
}

// Rows for the sidebar tree. A capability receives the same sink that a tree
// view renderer does: it only answers appendChild, and the diff places the
// rows afterwards.
export function capabilityTreeRows(sink) {
  for (const cap of registered) cap.treeRows?.(sink);
}

// Dashboard widgets for one scope. `scope` is the node name on a node screen,
// and undefined on the Datacenter screen, so a capability can narrow what it
// shows to the machine in front of the operator.
export function capabilityWidgets(scope) {
  const widgets = {};
  for (const cap of registered) Object.assign(widgets, cap.widgets?.(scope) || {});
  return widgets;
}

// Where those widgets sit before anybody moves them. A saved layout wins over
// this, so an entry here only decides a first visit.
export function capabilityLayout(scope) {
  const layout = [];
  for (const cap of registered) layout.push(...(cap.layout?.(scope) || []));
  return layout;
}

// The renderer for a selection a capability owns, or null when no capability
// claims that type.
export function capabilityScreen(type) {
  for (const cap of registered) {
    const render = cap.screens?.[type];
    if (render) return render;
  }
  return null;
}

// Extra context-menu entries. `kind` names what was right-clicked ('node'),
// and `subject` identifies it. A capability returns nothing when it has no
// business with that subject, and core adds no separator for an empty list.
export function capabilityMenuItems(kind, subject) {
  const items = [];
  for (const cap of registered) items.push(...(cap.menuItems?.(kind, subject) || []));
  return items;
}

// Short marks a capability puts on a node wherever a node is drawn - today the
// topology view - such as the power state of the machine under it. Each is
// { label, tone }, where tone is one of the dot classes 'on', 'off' or 'mid'.
// A screen that draws nodes asks here, so it never has to know which
// capability knows what about a node.
export function capabilityNodeBadges(nodeName) {
  const badges = [];
  for (const cap of registered) badges.push(...(cap.nodeBadges?.(nodeName) || []));
  return badges;
}

// Lines for the Alerts widget, as HTML strings, in the same shape that widget
// already builds for nodes and VMs.
export function capabilityAlerts() {
  const alerts = [];
  for (const cap of registered) alerts.push(...(cap.alerts?.() || []));
  return alerts;
}
