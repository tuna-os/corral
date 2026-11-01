// Core screen registry. Importing a screen registers its selection type.
const screens = new Map();

export function registerScreen(type, render) {
  if (screens.has(type)) throw new Error(`Screen already registered: ${type}`);
  screens.set(type, render);
}

export function screenFor(type) {
  return screens.get(type);
}
