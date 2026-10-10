// Topology: the fleet drawn as the machines it runs on.
//
// Every other screen lists guests. This one places them: a card per node, the
// guests on it inside, coloured by state, with what each node carries summed at
// the top. It is the third view that Prism's overview / diagram / table switch
// implies, and the one corral did not have.
//
// It is built to be used, not only read. A guest tile opens on click or Enter,
// carries the same right-click menu as everywhere else, and drags onto another
// node to migrate - through the tree's own drop rule, so a refused move is
// refused here for the same reason with the same words.
//
// What this screen decides, since no reference specifies it:
// - A node is a card whether or not it has guests: an empty node is the one an
//   operator moving load around is looking for.
// - A guest whose node is not a cluster node (a local QEMU or Incus host) gets
//   a card named after that host. A guest with no node at all - a stopped one,
//   most often - goes in a last card of its own, not in a node it is not on.
// - Nothing is drawn as a graph with lines. At fleet sizes a wall of cards that
//   reflows to the window stays readable where a node-and-edge drawing does not.

import { vmKey } from '../api.js';
import { select } from '../app.js';
import { icon } from '../icons.js';
import { vmMenuItems } from '../menus.js';
import { makeDraggable } from '../pools.js';
import { state } from '../state.js';
import { dropTargetNode } from '../tree.js';
import { capabilityNodeBadges } from '../ui/capabilities.js';
import { esc } from '../ui/dom.js';
import { attachContextMenu } from '../ui/menu.js';
import { keyed, reconcile } from '../ui/reconcile.js';

const UNPLACED = '\u0000unplaced';

// Where a guest runs, or UNPLACED. The KubeVirt backend reports a guest with no
// node as the literal "\u2014" rather than an empty string (pkg/kubevirt,
// client.go), and pkg/proxmox already treats that as no node. Changing the API
// would reach the CLI and the TUI, so this side honours the convention.
const placeOf = (vm) => (vm.node && vm.node !== '\u2014' ? vm.node : UNPLACED);

// "4Gi" and "512Mi" to GiB, for the per-node total. Anything unreadable counts
// as nothing rather than as NaN, which would poison the whole sum.
function gib(mem) {
  const m = /^(\d+(?:\.\d+)?)\s*([KMGT]i?)?/i.exec(String(mem || ''));
  if (!m) return 0;
  const n = Number(m[1]);
  const unit = (m[2] || 'Gi').toUpperCase().replace('I', '');
  return n * ({ K: 1 / (1024 * 1024), M: 1 / 1024, G: 1, T: 1024 }[unit] ?? 0);
}

const fmtGiB = (n) => (n >= 10 || Number.isInteger(n) ? String(Math.round(n)) : n.toFixed(1));

// The state class a tile takes, from the same fields the tree and grid read.
function tone(vm) {
  if (/paused/i.test(vm.status || '')) return 'paused';
  if (vm.ready || vm.running) return 'running';
  if (/start|creat|migrat/i.test(vm.status || '')) return 'changing';
  if (/error|fail|crash|unschedulable/i.test(vm.status || '')) return 'failed';
  return 'stopped';
}

function guestTile(vm) {
  const li = document.createElement('li');
  li.className = `topo-guest is-${tone(vm)}`;
  li.tabIndex = 0;
  li.dataset.vmKey = vmKey(vm);
  li.title = `${vm.name} · ${vm.status || 'unknown'}${vm.cpu ? ` · ${vm.cpu} vCPU` : ''}${vm.mem ? ` · ${vm.mem}` : ''}`;
  li.setAttribute('aria-label', li.title);
  li.innerHTML = `<span class="topo-guest-name">${esc(vm.name)}</span>`;
  const open = () => select({ type: 'vm', key: vmKey(vm) });
  li.onclick = open;
  li.onkeydown = (e) => {
    if (e.target !== li) return;
    if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(); }
  };
  attachContextMenu(li, () => vmMenuItems(vm));
  makeDraggable(li, vm);
  // The guest object is in the signature, not only its name: the handlers
  // above close over it, so a reused tile must not hold an older one.
  return keyed(li, `vm:${vmKey(vm)}`, [vm]);
}

// One card per place a guest can be. Cards are created once and updated in
// place on every render, so the guests inside can be reconciled rather than
// rebuilt; see the extension points document for why that matters.
const cards = new Map();

function cardFor(grid, id) {
  let card = cards.get(id);
  if (card && card.isConnected) return card;
  card = document.createElement('section');
  card.className = 'topo-node';
  card.dataset.node = id;
  card.innerHTML = `
    <header class="topo-head">
      <span class="dot"></span>
      <button type="button" class="topo-name"></button>
      <span class="topo-role muted"></span>
      <span class="topo-badges"></span>
    </header>
    <div class="topo-meta muted"></div>
    <ul class="topo-guests" role="list"></ul>`;
  cards.set(id, card);
  grid.appendChild(card);
  return card;
}

export function renderTopology(main) {
  let grid = main.querySelector('#topo-grid');
  if (!grid) {
    cards.clear();
    main.innerHTML = `
      <div class="page-head">
        <h1>${icon('datacenter')} Topology</h1>
        <div class="topo-legend muted" aria-hidden="true">
          <span class="topo-key is-running"></span>running
          <span class="topo-key is-changing"></span>changing
          <span class="topo-key is-paused"></span>paused
          <span class="topo-key is-stopped"></span>stopped
          <span class="topo-key is-failed"></span>failed
        </div>
      </div>
      <p class="muted topo-hint">Drag a guest onto another node to migrate it.</p>
      <div id="topo-grid"></div>`;
    grid = main.querySelector('#topo-grid');
  }

  // Group the fleet by where each guest runs.
  const byPlace = new Map();
  for (const vm of state.vms) {
    const place = placeOf(vm);
    if (!byPlace.has(place)) byPlace.set(place, []);
    byPlace.get(place).push(vm);
  }
  const clusterNodes = new Map(state.nodes.map((n) => [n.name, n]));
  // Every cluster node, guests or not, then any other host a guest names, in
  // name order, then the guests that are on no node at all.
  const places = [...new Set([...clusterNodes.keys(), ...byPlace.keys()])]
    .filter((p) => p !== UNPLACED)
    .sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
  if (byPlace.has(UNPLACED)) places.push(UNPLACED);

  const wanted = new Set(places);
  for (const [id, card] of cards) {
    if (!wanted.has(id)) { card.remove(); cards.delete(id); }
  }

  for (const place of places) {
    const card = cardFor(grid, place);
    // Keep the cards in order. appendChild on a card already in the grid moves
    // it, and a card already in place is left alone.
    grid.appendChild(card);
    const guests = (byPlace.get(place) || []).slice()
      .sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
    const node = clusterNodes.get(place);
    const unplaced = place === UNPLACED;

    const dot = card.querySelector('.topo-head .dot');
    dot.className = `dot ${unplaced ? 'off' : node ? (node.ready ? 'on' : 'off') : 'mid'}`;
    const name = card.querySelector('.topo-name');
    name.textContent = unplaced ? 'Not on a node' : place;
    name.disabled = !node;
    name.onclick = node ? () => select({ type: 'node', name: place }) : null;
    card.querySelector('.topo-role').textContent = unplaced ? 'stopped or local'
      : node ? (node.roles || 'worker') : 'host';
    card.querySelector('.topo-badges').innerHTML = (unplaced ? [] : capabilityNodeBadges(place))
      .map((b) => `<span class="pill ${esc(b.tone || '')}">${esc(b.label)}</span>`).join('');

    const cpu = guests.reduce((n, vm) => n + (Number(vm.cpu) || 0), 0);
    const mem = guests.reduce((n, vm) => n + gib(vm.mem), 0);
    const running = guests.filter((vm) => tone(vm) === 'running').length;
    card.querySelector('.topo-meta').textContent = guests.length
      ? `${guests.length} guest${guests.length === 1 ? '' : 's'} · ${running} running · ${cpu} vCPU · ${fmtGiB(mem)} GiB`
      : 'No guests';
    card.classList.toggle('is-empty', !guests.length);
    card.classList.toggle('is-unplaced', unplaced);

    // A cluster node takes drops, through the tree's own rule. It is wired
    // once, because the card outlives every poll and a listener per poll would
    // stack up. So the rule gets a view of the node that reads the current
    // poll's data: handing it this poll's object would leave a node that went
    // down still accepting drops, judged by readiness from when it was wired.
    if (node && !card.dataset.dropWired) {
      dropTargetNode(card, {
        name: place,
        get ready() { return !!state.nodes.find((n) => n.name === place)?.ready; },
      });
      card.dataset.dropWired = '1';
    }

    reconcile(card.querySelector('.topo-guests'), guests.map(guestTile));
  }
}

