// Unit tests for theme and density preference helpers (pkg/web/static/ui/theme.js).
//
//   just test-js        (node --test pkg/web/jstest/*.test.mjs)

import './dom-stub.mjs';

import assert from 'node:assert/strict';
import { beforeEach, describe, test } from 'node:test';

// Mock localStorage
const store = new Map();
globalThis.localStorage = {
  getItem: (k) => store.get(k) ?? null,
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k),
  clear: () => store.clear(),
};

globalThis.window = {
  matchMedia: (query) => ({
    matches: false,
  }),
};

const docAttrs = new Map();
const docStyles = new Map();
globalThis.document.documentElement = {
  setAttribute: (k, v) => docAttrs.set(k, String(v)),
  removeAttribute: (k) => docAttrs.delete(k),
  getAttribute: (k) => docAttrs.get(k) ?? null,
  style: {
    setProperty: (k, v) => docStyles.set(k, String(v)),
    removeProperty: (k) => docStyles.delete(k),
    getPropertyValue: (k) => docStyles.get(k) ?? '',
  },
};
globalThis.document.dispatchEvent = () => true;
globalThis.CustomEvent = class CustomEvent {
  constructor(type, init = {}) {
    this.type = type;
    this.detail = init.detail;
  }
};

import {
  customAccent,
  cycleDensityMode,
  cycleThemeMode,
  DENSITIES,
  densityMode,
  initThemePreferences,
  MODES,
  setCustomAccent,
  setDensityMode,
  setThemeMode,
  themeMode,
} from '../static/ui/theme.js';

describe('theme and density', () => {
  beforeEach(() => {
    store.clear();
    docAttrs.clear();
    docStyles.clear();
  });

  test('themeMode defaults to system and validates input', () => {
    assert.equal(themeMode(), 'system');
    setThemeMode('dark');
    assert.equal(themeMode(), 'dark');
    assert.equal(docAttrs.get('data-theme'), 'dark');

    setThemeMode('invalid');
    assert.equal(themeMode(), 'system');
    assert.equal(docAttrs.get('data-theme'), undefined);
  });

  test('cycleThemeMode rotates through system -> light -> dark', () => {
    setThemeMode('system');
    assert.equal(cycleThemeMode(), 'light');
    assert.equal(cycleThemeMode(), 'dark');
    assert.equal(cycleThemeMode(), 'system');
  });

  test('densityMode defaults to cosy and validates input', () => {
    assert.equal(densityMode(), 'cosy');
    assert.equal(docAttrs.get('data-density'), undefined);

    setDensityMode('compact');
    assert.equal(densityMode(), 'compact');
    assert.equal(docAttrs.get('data-density'), 'compact');

    setDensityMode('roomy');
    assert.equal(densityMode(), 'roomy');
    assert.equal(docAttrs.get('data-density'), 'roomy');

    setDensityMode('cosy');
    assert.equal(densityMode(), 'cosy');
    assert.equal(docAttrs.get('data-density'), undefined);
  });

  test('cycleDensityMode rotates through compact -> cosy -> roomy', () => {
    setDensityMode('compact');
    assert.equal(cycleDensityMode(), 'cosy');
    assert.equal(cycleDensityMode(), 'roomy');
    assert.equal(cycleDensityMode(), 'compact');
  });

  test('customAccent sets and resets accent and accent-2', () => {
    assert.equal(customAccent(), '');
    setCustomAccent('#3b82f6');
    assert.equal(customAccent(), '#3b82f6');
    assert.equal(docStyles.get('--accent'), '#3b82f6');
    assert.ok(docStyles.get('--accent-2'));

    setCustomAccent('');
    assert.equal(customAccent(), '');
    assert.equal(docStyles.get('--accent'), undefined);
    assert.equal(docStyles.get('--accent-2'), undefined);
  });

  test('initThemePreferences applies stored density and accent', () => {
    store.set('corral.density', 'compact');
    store.set('corral.accent', '#22c55e');

    initThemePreferences();
    assert.equal(docAttrs.get('data-density'), 'compact');
    assert.equal(docStyles.get('--accent'), '#22c55e');
  });
});
