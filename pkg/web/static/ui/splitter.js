// Splitter: a draggable, keyboard-operable boundary between two panels, with
// its size remembered per browser. One primitive for every resizable edge in
// the workspace (tree ↔ content, content ↔ task dock, …), so they all behave
// the same way: drag with a pointer, arrow keys (Shift for bigger steps),
// Home or double-click to reset.
//
// The size lives in a CSS custom property on :root and the stylesheet decides
// how to apply it, so layout stays in CSS and this module only owns input.

const read = (key) => { try { return localStorage.getItem(key); } catch { return null; } };
const write = (key, v) => { try { localStorage.setItem(key, v); } catch { /* private mode */ } };

/**
 * @param {object} o
 * @param {HTMLElement} o.handle   the separator element (role=separator, tabindex=0)
 * @param {'x'|'y'} o.axis         x = vertical bar resizing a width, y = horizontal bar resizing a height
 * @param {string} o.cssVar        e.g. '--tree-w'
 * @param {string} o.storageKey
 * @param {number} o.def           default size in px
 * @param {number} o.min
 * @param {() => number} o.max     evaluated on every change (viewport-relative limits)
 * @param {(e: PointerEvent) => number} o.sizeFromPointer  pointer position → new size
 * @param {() => number} o.current current rendered size
 * @param {{toggle(force?:boolean):boolean, collapsed():boolean}} [o.collapsible]
 *        the panel this separator collapses, if it has one
 * @returns {{ set(px:number, remember?:boolean):number, reset():void }}
 */
export function makeSplitter(o) {
  const set = (px, remember = true) => {
    const max = o.max();
    const size = Math.min(max, Math.max(o.min, Math.round(px)));
    document.documentElement.style.setProperty(o.cssVar, `${size}px`);
    // valuemin/valuemax are re-published on every change because the maximum
    // is viewport-relative: a value written once in the HTML would be a lie
    // the moment the window is resized.
    o.handle.setAttribute('aria-valuenow', String(size));
    o.handle.setAttribute('aria-valuemin', String(o.min));
    o.handle.setAttribute('aria-valuemax', String(max));
    if (remember) write(o.storageKey, String(size));
    return size;
  };
  const reset = () => set(o.def);

  set(Number(read(o.storageKey)) || o.def, false);

  const cursor = o.axis === 'x' ? 'col-resize' : 'row-resize';
  o.handle.addEventListener('pointerdown', (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    o.handle.setPointerCapture(e.pointerId);
    o.handle.classList.add('dragging');
    document.body.classList.add('resizing');
    document.body.style.cursor = cursor;
    const onMove = (ev) => set(o.sizeFromPointer(ev));
    const onUp = () => {
      o.handle.classList.remove('dragging');
      document.body.classList.remove('resizing');
      document.body.style.cursor = '';
      o.handle.removeEventListener('pointermove', onMove);
      o.handle.removeEventListener('pointerup', onUp);
      o.handle.removeEventListener('pointercancel', onUp);
    };
    o.handle.addEventListener('pointermove', onMove);
    o.handle.addEventListener('pointerup', onUp);
    o.handle.addEventListener('pointercancel', onUp);
  });

  o.handle.addEventListener('dblclick', reset);

  // For a width, → grows; for a dock height measured from the bottom, ↑ grows.
  const grow = o.axis === 'x' ? 'ArrowRight' : 'ArrowUp';
  const shrink = o.axis === 'x' ? 'ArrowLeft' : 'ArrowDown';
  o.handle.addEventListener('keydown', (e) => {
    const step = e.shiftKey ? 40 : 10;
    if (e.key === grow) { set(o.current() + step); e.preventDefault(); }
    if (e.key === shrink) { set(o.current() - step); e.preventDefault(); }
    if (e.key === 'Home') { reset(); e.preventDefault(); }
    if (e.key === 'End') { set(o.max()); e.preventDefault(); }
    // Enter collapses the pane and restores it, which the ARIA window-splitter
    // pattern lists as required rather than optional: without it a
    // keyboard-only operator can shrink the pane to its minimum but never get
    // the space back.
    if (e.key === 'Enter' && o.collapsible) { o.collapsible.toggle(); e.preventDefault(); }
  });

  return { set, reset };
}

/**
 * A collapsible panel: toggles `className` on <body>, remembered per browser.
 * @returns {{ toggle(force?:boolean):boolean, collapsed():boolean }}
 */
export function makeCollapsible({ className, storageKey, onChange = () => {} }) {
  const apply = (on, remember = true) => {
    document.body.classList.toggle(className, on);
    if (remember) write(storageKey, on ? '1' : '0');
    onChange(on);
    return on;
  };
  apply(read(storageKey) === '1', false);
  return {
    toggle: (force) => apply(force ?? !document.body.classList.contains(className)),
    collapsed: () => document.body.classList.contains(className),
  };
}
