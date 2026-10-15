// The URL as the address of what is on screen.
//
// Until now the page had one address. A click selected a guest, opened a tab
// or switched the tree view, and the URL never moved: nobody could send a
// colleague a link to a VM's console, a reload went back to whatever this
// browser had stored, and the back button did nothing at all.
//
// Proxmox VE solves this by keeping its navigation state in the browser's
// history, and its StateProvider draws the line that matters: the view, the
// selected resource and the open tab go in the URL, while everything else —
// pane widths, what is collapsed, column order — stays in local storage. The
// first group describes *where you are*, and a colleague opening your link
// wants to arrive there. The second describes how you like your workspace,
// and sending that to somebody else would be rude.
//
// Only the division of state is taken from Proxmox. The encoding is not:
// pve-manager is AGPL-3.0 and corral is Apache-2.0, so nothing here is
// derived from its code. Its format is a positional, dictionary-compressed
// token list, which needs a central table naming every tab in the product.
// Corral just left that shape behind in the capability registry, so this uses
// plain readable keys instead and nothing has to be registered.
//
//   #view=storage&sel=vm:/corral-vms/web-prod&tab=console
//   #sort=mem:desc&f.status=stopped        a filtered, sorted inventory list
//
// A guest's key already begins with a slash, so its address reads `vm://ns/name`.
// That is left alone: trimming the slash would mean knowing which selection
// types carry one, which is the central table this format exists to avoid.
//
// A key is absent when it holds its default, so the datacenter stays at `#`.

const DEFAULTS = { view: 'server', sel: 'dc', tab: 'summary' };

// Read the current address. Keys this module does not know are ignored rather
// than carried, so a stale link from an older build degrades to its defaults.
export function readRoute() {
  const hash = location.hash.replace(/^#/, '');
  const params = new URLSearchParams(hash);
  const route = { ...DEFAULTS };
  for (const key of Object.keys(DEFAULTS)) {
    const value = params.get(key);
    if (value) route[key] = value;
  }
  route.grid = readGrid(params);
  return route;
}

// The inventory grid's sort and filters, when the address carries them.
//
//   sort=mem:desc,name:asc      one or more columns, in priority order
//   f.status=stopped            a filter per column, by column id
//
// Null when the address says nothing about the grid, so a link to a guest does
// not reset somebody's filters to empty. Column ids are not checked here: the
// grid drops any it does not have, and this module knows no column names.
function readGrid(params) {
  const sort = (params.get('sort') || '').split(',').filter(Boolean).map((part) => {
    const [id, dir] = part.split(':');
    return { id, dir: dir === 'desc' ? 'desc' : 'asc' };
  }).filter((x) => x.id);
  const filters = {};
  for (const [key, value] of params) {
    if (key.startsWith('f.') && key.length > 2 && value) filters[key.slice(2)] = value;
  }
  if (!sort.length && !Object.keys(filters).length) return null;
  return { sort, filters };
}

// `sel` is one string: the selection's type, and the thing it names.
//
// The payload goes into both `key` and `name`, because the two halves of the
// UI disagree about which it is — a guest is addressed by key and a node by
// name — and each renderer reads the field it expects. The alternative is a
// table here mapping every selection type to its field, which is the kind of
// central list that a capability must not have to join.
export function decodeSelection(sel) {
  const at = sel.indexOf(':');
  if (at < 0) return { type: sel };
  const type = sel.slice(0, at);
  const payload = sel.slice(at + 1);
  return { type, key: payload, name: payload };
}

export function encodeSelection(selected) {
  const payload = selected.key ?? selected.name;
  return payload ? `${selected.type}:${payload}` : selected.type;
}

// The part of an address that names a place: view, selection and tab. Two
// addresses that agree on this are the same place, whatever the grid shows.
function encodePlace(route) {
  const params = new URLSearchParams();
  for (const key of Object.keys(DEFAULTS)) {
    if (route[key] && route[key] !== DEFAULTS[key]) params.set(key, route[key]);
  }
  return params.toString();
}

function encode(route) {
  const params = new URLSearchParams(encodePlace(route));
  const grid = route.grid;
  if (grid) {
    if (grid.sort?.length) params.set('sort', grid.sort.map((x) => `${x.id}:${x.dir}`).join(','));
    for (const [id, value] of Object.entries(grid.filters || {})) {
      if (String(value).trim()) params.set(`f.${id}`, value);
    }
  }
  // URLSearchParams escapes the separators inside a guest key, which makes a
  // readable address unreadable. They are safe in a fragment, so put them back.
  return params.toString().replace(/%2F/g, '/').replace(/%3A/g, ':');
}

// Write the address, but only when it actually changed.
//
// Every render calls this, including the ones the 5-second poll causes, so a
// no-op has to stay a no-op: a push on each poll would fill the history with
// hundreds of identical entries and make the back button useless.
export function writeRoute(route) {
  const next = encode(route);
  const current = location.hash.replace(/^#/, '');
  if (next === current) return;
  const url = next ? `#${next}` : location.pathname + location.search;
  // A move to a different place is a history entry, so back and forward walk
  // the screens. A change to the grid alone is not: a filter typed one letter
  // at a time would otherwise leave an entry per keystroke, and back would
  // spell the word out in reverse before reaching the last screen. So the
  // grid's changes replace the current entry rather than add one.
  //
  // `pushState` rather than assigning location.hash, because assigning it
  // fires hashchange and the listener would apply the state we just wrote.
  const samePlace = encodePlace(route) === encodePlace(readRoute());
  if (samePlace) history.replaceState(null, '', url);
  else history.pushState(null, '', url);
}

// Called when the address changes under us: the back button, the forward
// button, or a pasted link in the same tab.
export function onRouteChange(handler) {
  addEventListener('popstate', handler);
  // A pasted fragment in the same document fires hashchange and not popstate.
  addEventListener('hashchange', handler);
}
