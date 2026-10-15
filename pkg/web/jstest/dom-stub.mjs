// The least DOM that lets a client module load and run under Node.
//
// ui/dom.js appends its toast region to document.body when it is imported,
// so any module that imports it needs a `document` first. Import this module
// before the module under test: ES modules evaluate their imports in order.
// Tests register the elements a query should find in `elements`.

export const elements = new Map();

function element() {
  return {
    innerHTML: '',
    textContent: '',
    attrs: {},
    setAttribute(k, v) { this.attrs[k] = v; },
    appendChild() {},
    remove() { this.removed = true; },
    querySelector: (sel) => elements.get(sel) ?? null,
  };
}

const classes = new Set();
globalThis.document = {
  createElement: element,
  querySelector: (sel) => elements.get(sel) ?? null,
  body: {
    appendChild() {},
    classList: {
      toggle: (name, on) => { if (on) classes.add(name); else classes.delete(name); },
      contains: (name) => classes.has(name),
    },
  },
};

export const makeElement = element;
