// Polling only while somebody can see the page.
//
// A browser tab that is behind another, or minimised, kept polling the fleet
// every five seconds, and so did the dock's task log, every live chart and
// every dashboard widget that fetches its own data. That is four or five
// requests every few seconds from a page nobody is looking at, multiplied by
// every operator who leaves a corral tab open. Proxmox does the same, so this
// goes one step past the reference rather than copying it.
//
// Two rules, used by every poller in the UI:
//
// - A tick that falls while the page is hidden does nothing. The timer keeps
//   running, so polling resumes on its own cadence with no restart to get
//   wrong.
// - The moment the page is shown again, the poller runs once at once. Without
//   that, an operator coming back would read data up to one interval old as if
//   it were current.

export const pageHidden = () => document.visibilityState === 'hidden';

// Call fn when the page becomes visible. Returns a function that removes the
// listener, which a poller tied to one element must call when the element goes.
export function onPageVisible(fn) {
  const handler = () => { if (!pageHidden()) fn(); };
  document.addEventListener('visibilitychange', handler);
  return () => document.removeEventListener('visibilitychange', handler);
}

// Run fn now, then every `ms` while the page is visible, for as long as
// `alive()` is true. The first tick after alive() turns false tears down both
// the timer and the visibility listener, so a widget that leaves the page takes
// its polling with it.
export function pollWhileVisible(fn, ms, alive = () => true) {
  let stopListening = null;
  const stop = () => { clearInterval(timer); stopListening?.(); };
  const tick = () => {
    if (!alive()) { stop(); return; }
    if (!pageHidden()) fn();
  };
  fn();
  const timer = setInterval(tick, ms);
  stopListening = onPageVisible(tick);
  return stop;
}
