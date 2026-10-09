// Breadcrumb: where the thing on screen sits, and a way back up.
//
// The sidebar shows the hierarchy, but a detail screen does not: it opens with
// a name and a status pill, and nothing says which node or namespace the guest
// is on. A pop-out console, a palette jump or a link lands on that screen with
// no tree context at all.
//
// vSphere's object navigator and VS Code's breadcrumbs both answer this the
// same way: name the path above the current object, and make each step a way
// back to it. The trail is markup only. It carries no state, so a poll can
// rebuild it with the rest of the heading.

import { esc } from './dom.js';

/**
 * Render a breadcrumb trail.
 *
 * @param {{label: string, go?: () => void}[]} parts  Ancestors first, the
 *        current object last. A part with no `go` is not a link, which is
 *        right for the last one: it is the page you are on.
 * @returns {string} markup for the caller's template
 */
export function breadcrumb(parts) {
  const steps = parts.filter(Boolean);
  if (steps.length < 2) return ''; // a trail of one is just the title again
  return `<nav class="breadcrumb" aria-label="Breadcrumb"><ol>${steps.map((p, i) => {
    const last = i === steps.length - 1;
    // aria-current marks the page you are on, so a screen reader does not
    // announce the trail as a list of equal choices.
    const inner = p.go && !last
      ? `<button type="button" class="crumb" data-crumb="${i}">${esc(p.label)}</button>`
      : `<span class="crumb"${last ? ' aria-current="page"' : ''}>${esc(p.label)}</span>`;
    return `<li>${inner}</li>`;
  }).join('')}</ol></nav>`;
}

/**
 * Wire the links that `breadcrumb()` rendered into `root`.
 * Called after the markup lands, like every other handler in content/.
 */
export function bindBreadcrumb(root, parts) {
  const steps = parts.filter(Boolean);
  root.querySelectorAll('[data-crumb]').forEach((b) => {
    const part = steps[Number(b.dataset.crumb)];
    if (part?.go) b.onclick = () => part.go();
  });
}
