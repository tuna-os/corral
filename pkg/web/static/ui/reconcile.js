// Keyed list reconciliation: update a container in place instead of rebuilding
// it.
//
// The poll loop re-derives the whole sidebar every 5 seconds. Rebuilding it
// means every row is a new DOM node, which throws away everything the browser
// attaches to node identity rather than to markup: keyboard focus, an
// in-progress drag, a text selection, CSS transitions mid-flight. tree.js
// already carved out the filter box by hand for exactly this reason — "a
// rebuilt input would drop focus and the caret mid-word" — and the same
// argument applies to every other row; it just wasn't visible until rows
// became draggable and focusable.
//
// Rows are matched by key and compared by signature. A row whose signature is
// unchanged is reused as-is, so its node identity survives the poll. The
// signature must therefore cover everything the row renders *and* everything
// its handlers close over, not just its visible text: a row that looks
// identical but was built from a newer object would otherwise keep handlers
// pointing at the stale one. Callers get this right by signing the source
// data, which is what the old whole-tree fingerprint did — only now per row
// instead of all-or-nothing.

export const KEY = 'rkey';
export const SIG = 'rsig';

// Tag an element for reconciliation. `sig` is stringified, so a caller can
// hand over the row's source data directly and let JSON decide equality.
export function keyed(el, key, sig) {
  el.dataset[KEY] = String(key);
  el.dataset[SIG] = typeof sig === 'string' ? sig : JSON.stringify(sig ?? null);
  return el;
}

// Bring `parent`'s children into line with `desired`, reusing the nodes whose
// key and signature both still match.
//
// `keep` names elements that are not part of the keyed list and must be left
// alone (the filter box, a toggle bar built once). They are neither matched
// nor removed; the keyed rows are ordered after them.
//
// Returns the number of rows that had to be created or replaced, which is what
// the tests assert on: "a poll that changes nothing touches nothing" is the
// property worth pinning down, and it is invisible from the DOM alone.
export function reconcile(parent, desired, { keep = [] } = {}) {
  const kept = new Set(keep);
  const existing = new Map();
  for (const child of [...parent.children]) {
    if (kept.has(child)) continue;
    const key = child.dataset?.[KEY];
    // An untagged child is not ours to reason about: it was appended by
    // something outside the keyed list and nothing here knows how to rebuild
    // it, so dropping it is the only safe reading of "not in `desired`".
    if (key === undefined) { child.remove(); continue; }
    // A key can legitimately appear more than once — the same guest shows up
    // under two pools — so each key holds a queue and every match consumes one
    // node. Keeping a single node per key instead would leave the duplicates
    // unmatched *and* unremoved, and they would pile up in the sidebar one poll
    // at a time.
    if (!existing.has(key)) existing.set(key, []);
    existing.get(key).push(child);
  }

  let changed = 0;
  // `cursor` walks the position each desired row should end up at. Inserting
  // before the node that currently follows the last placed row keeps the
  // relative order of reused nodes without moving them when the order is
  // already right — the common case on a poll.
  let cursor = null;
  const anchor = () => (cursor ? cursor.nextSibling : firstKeyedOrEnd(parent, kept));

  for (const next of desired) {
    const key = next.dataset[KEY];
    const queue = existing.get(key);
    const prev = queue?.shift();
    let node = next;
    // An unsigned row is always rebuilt. Comparing a missing signature against
    // another missing one would say "equal" and reuse a node built from data
    // nobody checked, which is the one way this function can be actively
    // wrong: a stale row that looks current.
    const signed = next.dataset[SIG] !== undefined;
    if (prev && signed && prev.dataset[SIG] === next.dataset[SIG]) {
      node = prev; // unchanged — keep the live node, and whatever it is holding
    } else {
      changed++;
    }
    // A node that was matched but not reused is replaced, so it still has to go.
    if (prev && prev !== node) prev.remove();
    const at = anchor();
    if (node !== at) parent.insertBefore(node, at);
    cursor = node;
  }

  // Whatever is left was not in `desired`: the VM went away, or a pool lost a
  // member. Every queue is drained, not just the first node in it.
  for (const queue of existing.values()) for (const stale of queue) stale.remove();
  return changed;
}

// Where the keyed region starts: after the kept elements, which stay at the
// front of the container.
function firstKeyedOrEnd(parent, kept) {
  for (const child of parent.children) if (!kept.has(child)) return child;
  return null;
}
