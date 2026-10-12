// Is the operator in the middle of something?
//
// The poll redraws the content pane from markup strings every 5 seconds, which
// is fine when nobody is touching it and destructive when somebody is: a
// column being dragged to a new width, a half-made text selection, a row being
// dragged towards a node. The gesture does not fail visibly — the element it
// was working on simply stops existing, and the drag ends nowhere.
//
// app.js already had one special case for this, a guard that skipped the poll
// while a context menu was open. This is the same rule stated once for every
// gesture instead of once per symptom: while an interaction is live the poll
// holds off, and the moment it ends the deferred render runs, so nothing is
// lost but the five seconds nobody was looking at.
//
// A pointer gesture is bounded by pointerup, but a native HTML5 drag is not —
// it swallows the pointer stream and ends with dragend — so both are tracked.

let pointers = 0;
let dragging = false;
const listeners = new Set();

const settled = () => pointers <= 0 && !dragging;

function release() {
  if (!settled()) return;
  // Copied before iterating: a listener that re-renders may well add or drop
  // one, and mutating the set mid-iteration would skip an entry.
  for (const fn of [...listeners]) fn();
}

/** True while a pointer is down or a drag is in flight. */
export function interacting() {
  return !settled();
}

/**
 * Run `fn` once the current gesture finishes. Registered once at startup by
 * the poll loop, which uses it to run the render it chose to skip.
 */
export function onSettled(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function initInteractionTracking(target = document) {
  // Capture phase: a handler that stops propagation (the grid's own resize
  // handles do) must not also hide the gesture from this counter.
  target.addEventListener('pointerdown', () => { pointers++; }, true);
  // pointercancel fires instead of pointerup when the browser takes the
  // gesture away — a touch becoming a scroll, most often. Without it the
  // counter would never come back down and the poll would stop for good.
  for (const end of ['pointerup', 'pointercancel']) {
    target.addEventListener(end, () => {
      pointers = Math.max(0, pointers - 1);
      release();
    }, true);
  }
  target.addEventListener('dragstart', () => { dragging = true; }, true);
  target.addEventListener('dragend', () => { dragging = false; release(); }, true);
  // A drop outside any registered drop zone ends the drag without dragend in
  // some browsers; drop is the backstop.
  target.addEventListener('drop', () => { dragging = false; release(); }, true);
}
