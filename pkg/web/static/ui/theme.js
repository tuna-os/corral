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
const KEY_DENSITY = 'corral.density';
const KEY_ACCENT = 'corral.accent';

export const MODES = ['system', 'light', 'dark'];
export const DENSITIES = ['compact', 'cosy', 'roomy'];

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

/** The current table & UI density mode ('compact' | 'cosy' | 'roomy'). */
export function densityMode() {
  try {
    const v = localStorage.getItem(KEY_DENSITY);
    return DENSITIES.includes(v) ? v : 'cosy';
  } catch { return 'cosy'; }
}

/** Apply a density mode. */
export function setDensityMode(density) {
  const next = DENSITIES.includes(density) ? density : 'cosy';
  if (next === 'cosy') {
    document.documentElement.removeAttribute('data-density');
  } else {
    document.documentElement.setAttribute('data-density', next);
  }
  try { localStorage.setItem(KEY_DENSITY, next); } catch { /* private mode */ }
  document.dispatchEvent(new CustomEvent('corral:density', { detail: next }));
  return next;
}

/** Step through density modes. */
export function cycleDensityMode() {
  return setDensityMode(DENSITIES[(DENSITIES.indexOf(densityMode()) + 1) % DENSITIES.length]);
}

/** Get user-customized accent hex, or empty string if using server default. */
export function customAccent() {
  try {
    return localStorage.getItem(KEY_ACCENT) || '';
  } catch { return ''; }
}

/**
 * Set or clear user-customized accent color.
 * If hex is empty or null, resets to server default.
 */
export function setCustomAccent(hex) {
  if (!hex) {
    try { localStorage.removeItem(KEY_ACCENT); } catch { /* private mode */ }
    document.documentElement.style.removeProperty('--accent');
    document.documentElement.style.removeProperty('--accent-2');
    document.dispatchEvent(new CustomEvent('corral:accent', { detail: '' }));
    return '';
  }
  const cleanHex = hex.trim();
  document.documentElement.style.setProperty('--accent', cleanHex);
  const bareHex = cleanHex.replace('#', '');
  if (bareHex.length === 6) {
    const dim = (c) => {
      let v = parseInt(c, 16);
      v = Math.round(v * 0.875);
      return v.toString(16).padStart(2, '0');
    };
    const accent2 = '#' + dim(bareHex.slice(0, 2)) + dim(bareHex.slice(2, 4)) + dim(bareHex.slice(4, 6));
    document.documentElement.style.setProperty('--accent-2', accent2);
  }
  try { localStorage.setItem(KEY_ACCENT, cleanHex); } catch { /* private mode */ }
  document.dispatchEvent(new CustomEvent('corral:accent', { detail: cleanHex }));
  return cleanHex;
}

/** Initialize user preferences from localStorage. */
export function initThemePreferences() {
  const d = densityMode();
  if (d && d !== 'cosy') document.documentElement.setAttribute('data-density', d);
  const a = customAccent();
  if (a) setCustomAccent(a);
}
