// Light or dark, and who decides.
//
// Three states, not two. "System" is the default and the one most people want:
// the console follows the desktop, so it is dark at night with everything else
// and light in the morning with everything else. The other two are an explicit
// override for the operator whose console should not agree with their desktop
// — a projector in a bright room, or a NOC screen that stays dark whatever the
// laptop does.
//
// Only --bg, --panel, --text and friends change. --accent stays where the
// server put it, because that one belongs to whoever branded this corral.

const KEY = 'corral.theme';
export const MODES = ['system', 'light', 'dark'];

export function themeMode() {
  try {
    const v = localStorage.getItem(KEY);
    return MODES.includes(v) ? v : 'system';
  } catch { return 'system'; }
}

/** The scheme actually showing, with "system" resolved. */
export function effectiveTheme() {
  const mode = themeMode();
  if (mode !== 'system') return mode;
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

/**
 * Apply a mode. "system" removes the attribute rather than writing a value,
 * which is what lets the stylesheet's prefers-color-scheme rule apply and keeps
 * the console following the desktop as it changes, with no listener here.
 */
export function setThemeMode(mode) {
  const next = MODES.includes(mode) ? mode : 'system';
  if (next === 'system') document.documentElement.removeAttribute('data-theme');
  else document.documentElement.setAttribute('data-theme', next);
  try { localStorage.setItem(KEY, next); } catch { /* private mode */ }
  document.dispatchEvent(new CustomEvent('corral:theme', { detail: next }));
  return next;
}

/** Step through the three modes, for a single keystroke or one palette entry. */
export function cycleThemeMode() {
  return setThemeMode(MODES[(MODES.indexOf(themeMode()) + 1) % MODES.length]);
}
