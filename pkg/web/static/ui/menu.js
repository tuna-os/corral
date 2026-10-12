// Context menu primitive: positioning, keyboard navigation and the
// right-click / long-press / Shift+F10 wiring. Callers supply the items.

import { icon } from '../icons.js';
import { state } from '../state.js';
import { esc } from './dom.js';

// ── Context Menus ──────────────────────────────────────────────────

export let activeContextMenu = null;

function hideContextMenu() {
  if (activeContextMenu) {
    activeContextMenu.el.remove();
    if (activeContextMenu.trigger && typeof activeContextMenu.trigger.focus === 'function') {
      if (document.activeElement === document.body || activeContextMenu.el.contains(document.activeElement)) {
        activeContextMenu.trigger.focus();
      }
    }
    activeContextMenu = null;
  }
}

function showContextMenu(e, items, triggerEl) {
  if (e) {
    if (typeof e.preventDefault === 'function') e.preventDefault();
    if (typeof e.stopPropagation === 'function') e.stopPropagation();
  }
  hideContextMenu();

  const isReadOnly = document.body.classList.contains('read-only') || !state.me.admin;
  const rawItems = typeof items === 'function' ? items() : items;
  if (!rawItems || !rawItems.length) return;

  // Filter out mutating items for read-only users
  const filtered = rawItems.filter((it) => {
    if (!it) return false;
    if (isReadOnly && it.mutate) return false;
    return true;
  });

  // Clean trailing/duplicate separators
  const visible = [];
  for (const it of filtered) {
    if (it.separator) {
      if (visible.length && !visible[visible.length - 1].separator) {
        visible.push(it);
      }
    } else {
      visible.push(it);
    }
  }
  if (visible.length && visible[visible.length - 1].separator) {
    visible.pop();
  }
  if (!visible.length) return;

  const menu = document.createElement('div');
  menu.className = 'context-menu';
  menu.setAttribute('role', 'menu');
  menu.setAttribute('tabindex', '-1');

  for (const it of visible) {
    if (it.separator) {
      const sep = document.createElement('div');
      sep.className = 'menu-separator';
      sep.setAttribute('role', 'separator');
      menu.appendChild(sep);
      continue;
    }

    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = `menu-item${it.danger ? ' danger' : ''}${it.mutate ? ' menu-mutate' : ''}`;
    btn.setAttribute('role', 'menuitem');
    btn.setAttribute('tabindex', '-1');
    if (it.disabled) btn.disabled = true;
    if (it.title) btn.title = it.title;

    btn.innerHTML = `${it.icon ? icon(it.icon) : ''} <span class="menu-label">${esc(it.label)}</span>`;
    btn.onclick = (ev) => {
      ev.stopPropagation();
      hideContextMenu();
      if (typeof it.action === 'function') it.action();
    };

    menu.appendChild(btn);
  }

  document.body.appendChild(menu);

  // Position calculation:
  let x = e?.clientX;
  let y = e?.clientY;
  if ((x === undefined || y === undefined || (x === 0 && y === 0)) && triggerEl) {
    const rect = triggerEl.getBoundingClientRect();
    x = rect.left + 24;
    y = rect.bottom;
  }
  x = x || 10;
  y = y || 10;

  const rect = menu.getBoundingClientRect();
  const pad = 6;
  if (x + rect.width > window.innerWidth - pad) {
    x = Math.max(pad, window.innerWidth - rect.width - pad);
  }
  if (y + rect.height > window.innerHeight - pad) {
    y = Math.max(pad, window.innerHeight - rect.height - pad);
  }
  if (x < pad) x = pad;
  if (y < pad) y = pad;

  menu.style.left = `${Math.round(x)}px`;
  menu.style.top = `${Math.round(y)}px`;

  activeContextMenu = { el: menu, trigger: triggerEl, top: triggerEl?.getBoundingClientRect().top };

  // Focus the first enabled menu item
  const enabled = [...menu.querySelectorAll('button.menu-item:not(:disabled)')];
  if (enabled.length > 0) {
    enabled[0].focus();
  } else {
    menu.focus();
  }

  menu.addEventListener('keydown', (kev) => {
    const buttons = [...menu.querySelectorAll('button.menu-item:not(:disabled)')];
    if (!buttons.length) return;
    const currentIdx = buttons.indexOf(document.activeElement);

    if (kev.key === 'ArrowDown') {
      kev.preventDefault();
      const next = (currentIdx + 1) % buttons.length;
      buttons[next].focus();
    } else if (kev.key === 'ArrowUp') {
      kev.preventDefault();
      const prev = (currentIdx - 1 + buttons.length) % buttons.length;
      buttons[prev].focus();
    } else if (kev.key === 'Home') {
      kev.preventDefault();
      buttons[0].focus();
    } else if (kev.key === 'End') {
      kev.preventDefault();
      buttons[buttons.length - 1].focus();
    } else if (kev.key === 'Escape') {
      kev.preventDefault();
      hideContextMenu();
    } else if (kev.key === 'Tab') {
      hideContextMenu();
    }
  });
}

/**
 * Open the same menu from a click on a button, anchored under it.
 *
 * attachContextMenu covers right-click, long-press and Shift+F10, which is
 * right for a row. A toolbar's overflow button is a different thing: it is a
 * control whose whole purpose is to open the menu, so it answers a plain
 * click. Everything else — placement, keyboard handling, the read-only filter
 * — is the same code.
 */
export function openMenuFrom(el, getItems) {
  if (!el) return;
  const items = typeof getItems === 'function' ? getItems() : getItems;
  if (!items || !items.length) return;
  // No pointer coordinates, so showContextMenu anchors under the trigger.
  showContextMenu(null, items, el);
  el.setAttribute('aria-expanded', 'true');
  // The menu closes by several routes (Escape, a click elsewhere, an item), so
  // the button watches for its disappearance rather than each of them.
  const sync = setInterval(() => {
    if (!activeContextMenu) {
      el.setAttribute('aria-expanded', 'false');
      clearInterval(sync);
    }
  }, 150);
}

export function attachContextMenu(el, getItems) {
  if (!el) return;
  if (!el.hasAttribute('tabindex')) el.setAttribute('tabindex', '0');

  el.addEventListener('contextmenu', (e) => {
    e.preventDefault();
    const items = typeof getItems === 'function' ? getItems() : getItems;
    if (items && items.length) showContextMenu(e, items, el);
  });

  el.addEventListener('keydown', (e) => {
    if (e.key === 'ContextMenu' || (e.shiftKey && e.key === 'F10')) {
      e.preventDefault();
      const items = typeof getItems === 'function' ? getItems() : getItems;
      if (items && items.length) showContextMenu(e, items, el);
    }
  });
}

document.addEventListener('pointerdown', (e) => {
  if (activeContextMenu && !activeContextMenu.el.contains(e.target)) {
    hideContextMenu();
  }
});
window.addEventListener('resize', hideContextMenu);
// Close on a scroll that moved the row the menu belongs to; a scroll that
// leaves it in place (a widget body, a late scroll-into-view) must not
// close the menu under the pointer.
window.addEventListener('scroll', (e) => {
  if (!activeContextMenu || activeContextMenu.el.contains(e.target)) return;
  const t = activeContextMenu.trigger;
  if (!t || !t.isConnected || Math.abs(t.getBoundingClientRect().top - activeContextMenu.top) > 4) hideContextMenu();
}, true);