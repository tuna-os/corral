// Settings screen: theme and branding.

import { api } from '../api.js';
import { icon } from '../icons.js';
import { MODES, setThemeMode, themeMode } from '../ui/theme.js';
import { $, esc } from '../ui/dom.js';

// ── Settings: theme & branding ────────────────────────────────────
// Live-preview settings form: changes apply to the page immediately;
// Save persists to config.yaml via PUT /api/theme.

let themePreview = null; // <style> element for live preview

export async function renderSettings(main) {
  let theme;
  try { theme = await api('/api/theme'); }
  catch (e) { main.innerHTML = `<p class="console-msg">${esc(e.message)}</p>`; return; }

  const presets = [
    ['#f0883e', 'Orange'],
    ['#3b82f6', 'Blue'],
    ['#22c55e', 'Green'],
    ['#a855f7', 'Purple'],
    ['#ef4444', 'Red'],
    ['#f59e0b', 'Amber'],
  ];

  main.innerHTML = `
    <div class="page-head"><h1>${icon('cog')} Settings</h1></div>

    <h2 class="section">${icon('cog')} Appearance</h2>
    <div class="hw-edit" style="align-items:center">
      <div class="seg" role="radiogroup" aria-label="Colour scheme">
        ${MODES.map((m) => `<button type="button" class="btn sm seg-btn" role="radio" data-theme-mode="${m}"
          aria-checked="${m === themeMode() ? 'true' : 'false'}">${m === 'system' ? 'System' : m === 'light' ? 'Light' : 'Dark'}</button>`).join('')}
      </div>
      <span class="muted">System follows this device. Remembered in this browser only.</span>
    </div>

    <h2 class="section">${icon('cpu')} Accent colour</h2>
    <div class="hw-edit" style="align-items:center">
      <label>Colour <input type="color" id="set-accent" value="${esc(theme.accent)}"></label>
      <div style="display:flex;gap:8px;flex-wrap:wrap">
        ${presets.map(([hex, label]) =>
          `<button class="btn sm preset" data-hex="${esc(hex)}" style="${hex === theme.accent ? 'border-color:' + esc(hex) + ';outline:1px solid ' + esc(hex) : ''}">${esc(label)}</button>`).join('')}
      </div>
    </div>

    <h2 class="section">Branding</h2>
    <div class="hw-edit">
      <label>Title <input id="set-brand-title" value="${esc(theme.brand_title)}" placeholder="Corral"></label>
      <label>Emoji <input id="set-brand-emoji" value="${esc(theme.brand_emoji)}" placeholder="🤠" maxlength="4" style="width:80px"></label>
      <label>Subtitle <input id="set-brand-subtitle" value="${esc(theme.brand_subtitle)}" placeholder="Virtual Environment" style="width:200px"></label>
    </div>

    <h2 class="section">Custom CSS</h2>
    <textarea id="set-css" rows="10" style="width:100%;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:var(--radius);padding:10px;font-family:monospace;font-size:.85rem" placeholder="/* Custom CSS injected into every page */">${esc(theme.custom_css)}</textarea>

    <div class="dialog-actions" style="justify-content:flex-start;margin-top:16px">
      <button class="btn primary" id="set-save">${icon('download')} Save</button>
      <button class="btn" id="set-reset-css" ${theme.custom_css ? '' : 'disabled'}>Reset CSS</button>
      <span class="muted" style="margin-left:12px;font-size:.82rem" id="set-status"></span>
    </div>`;

  // Live preview: update CSS custom properties and branding on every change.
  const accent = $('#set-accent');
  const title = $('#set-brand-title');
  const emoji = $('#set-brand-emoji');
  const subtitle = $('#set-brand-subtitle');
  const css = $('#set-css');

  const updatePreview = () => {
    document.documentElement.style.setProperty('--accent', accent.value);
    // Accent-2: slightly darkened variant for hover states.
    const hex = accent.value.replace('#', '');
    if (hex.length === 6) {
      const dim = (c) => {
        let v = parseInt(c, 16);
        v = Math.round(v * 0.875);
        return v.toString(16).padStart(2, '0');
      };
      document.documentElement.style.setProperty('--accent-2', '#' + dim(hex.slice(0, 2)) + dim(hex.slice(2, 4)) + dim(hex.slice(4, 6)));
    }
    // Branding.
    const brand = document.querySelector('.brand');
    if (brand) {
      brand.innerHTML = `${emoji.value ? emoji.value + ' ' : ''}<strong>${esc(title.value || 'Corral')}</strong> <span>${esc(subtitle.value || '')}</span>`;
    }
    // Custom CSS preview.
    if (!themePreview) {
      themePreview = document.createElement('style');
      themePreview.id = 'corral-theme-preview';
      document.head.appendChild(themePreview);
    }
    themePreview.textContent = css.value;
  };

  // Preset swatches.
  main.querySelectorAll('[data-theme-mode]').forEach((b) => {
    b.onclick = () => {
      setThemeMode(b.dataset.themeMode);
      // Only the pressed state changes, so update it in place: re-rendering
      // the screen would take focus off the control that was just used.
      main.querySelectorAll('[data-theme-mode]').forEach(
        (o) => o.setAttribute('aria-checked', o === b ? 'true' : 'false'),
      );
    };
  });

  main.querySelectorAll('.preset').forEach((b) => {
    b.onclick = () => {
      accent.value = b.dataset.hex;
      updatePreview();
      // Highlight the active preset.
      main.querySelectorAll('.preset').forEach((p) => p.style.outline = '');
      b.style.outline = `1px solid ${b.dataset.hex}`;
      b.style.borderColor = b.dataset.hex;
    };
  });

  accent.oninput = updatePreview;
  title.oninput = updatePreview;
  emoji.oninput = updatePreview;
  subtitle.oninput = updatePreview;
  css.oninput = updatePreview;

  // Save.
  $('#set-save').onclick = async () => {
    const btn = $('#set-save');
    const st = $('#set-status');
    btn.disabled = true;
    st.textContent = 'Saving…';
    try {
      await api('/api/theme', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          accent: accent.value,
          brand_title: title.value,
          brand_emoji: emoji.value,
          brand_subtitle: subtitle.value,
          custom_css: css.value,
        }),
      });
      st.textContent = '✓ Saved';
      st.style.color = 'var(--green)';
      // Remove the preview style — the server will inject the real one on reload.
      if (themePreview) { themePreview.remove(); themePreview = null; }
    } catch (e) {
      st.textContent = `✗ ${e.message}`;
      st.style.color = 'var(--red)';
    }
    btn.disabled = false;
    setTimeout(() => { st.textContent = ''; st.style.color = ''; }, 4000);
  };

  // Reset custom CSS.
  $('#set-reset-css').onclick = () => {
    css.value = '';
    updatePreview();
  };
  css.oninput = () => {
    $('#set-reset-css').disabled = !css.value.trim();
  };

  // Apply the current theme's custom CSS as live preview too.
  updatePreview();
}