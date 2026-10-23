// The least DOM that lets a client module load and run under Node.
//
// ui/dom.js appends its toast region to document.body when it is imported,
// so any module that imports it needs a `document` first. Import this module
// before the module under test: ES modules evaluate their imports in order.
// Tests register the elements a query should find in `elements`.

export const elements = new Map();

function element() {
  const elClasses = new Set();
  return {
    innerHTML: '',
    textContent: '',
    attrs: {},
    dataset: {},
    setAttribute(k, v) { this.attrs[k] = v; },
    removeAttribute(k) { delete this.attrs[k]; },
    getAttribute(k) { return this.attrs[k] ?? null; },
    hasAttribute(k) { return k in this.attrs; },
    appendChild() {},
    insertBefore() {},
    firstChild: null,
    children: [],
    contains: () => false,
    closest: () => element(),
    remove() { this.removed = true; },
    querySelector: (sel) => elements.get(sel) ?? null,
    querySelectorAll: (sel) => {
      const el = elements.get(sel);
      return el ? [el] : [];
    },
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() {},
    style: {
      setProperty() {},
      getPropertyValue() { return ''; },
    },
    classList: {
      add(name) { elClasses.add(name); },
      remove(name) { elClasses.delete(name); },
      toggle(name, on) { if (on) elClasses.add(name); else elClasses.delete(name); },
      contains(name) { return elClasses.has(name); },
    },
    selectedOptions: [],
    options: [],
    showModal() {},
    close() {},
  };
}

const classes = new Set();
globalThis.document = {
  createElement: element,
  querySelector: (sel) => elements.get(sel) ?? element(),
  querySelectorAll: (sel) => {
    const el = elements.get(sel);
    return el ? [el] : [];
  },
  addEventListener() {},
  removeEventListener() {},
  body: {
    appendChild() {},
    classList: {
      toggle: (name, on) => { if (on) classes.add(name); else classes.delete(name); },
      contains: (name) => classes.has(name),
    },
  },
  documentElement: {
    style: {
      setProperty() {},
      getPropertyValue() { return ''; },
    },
    classList: {
      toggle: (name, on) => { if (on) classes.add(name); else classes.delete(name); },
      contains: (name) => classes.has(name),
    },
  },
};

globalThis.window = {
  addEventListener() {},
  removeEventListener() {},
  dispatchEvent() {},
};
globalThis.addEventListener = globalThis.addEventListener || ((...args) => globalThis.window.addEventListener(...args));
globalThis.removeEventListener = globalThis.removeEventListener || ((...args) => globalThis.window.removeEventListener(...args));
globalThis.dispatchEvent = globalThis.dispatchEvent || ((...args) => globalThis.window.dispatchEvent(...args));

globalThis.location = globalThis.location || { hash: '', search: '', pathname: '/' };
globalThis.history = globalThis.history || {
  pushState() {},
  replaceState() {},
  back() {},
  forward() {},
};

function createStorage() {
  const map = new Map();
  return {
    getItem: (k) => map.get(k) ?? null,
    setItem: (k, v) => map.set(k, String(v)),
    removeItem: (k) => map.delete(k),
    clear: () => map.clear(),
  };
}

globalThis.localStorage = globalThis.localStorage || createStorage();
globalThis.sessionStorage = globalThis.sessionStorage || createStorage();

export const makeElement = element;
