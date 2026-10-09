// Storage screen: what a source offers, and the disks imported from it.
//
// The counterpart to Storage View in the sidebar. Proxmox puts the contents of
// a storage target on its own screen rather than only in the tree, because the
// tree has room for a name and nothing else — the same reason this exists.

import { icon } from '../icons.js';
import { state } from '../state.js';
import { esc } from '../ui/dom.js';

export function renderStorage(main, name) {
  // A "dv:<namespace>" selection is the imported-disk group rather than an
  // image source; they share a screen because they are the same question
  // ("what is in this storage?") asked of two kinds of thing.
  if (name && name.startsWith('dv:')) return renderDataVolumes(main, name.slice(3));

  const images = (state.images || []).filter((i) => (i.source || '(built in)') === name);
  if (!name || !images.length) {
    main.innerHTML = `<div class="page-head"><h1>${icon('disk')} Storage</h1></div>
      <p class="muted">${name ? `No images from ${esc(name)}.` : 'Pick a source in the sidebar.'}</p>`;
    return;
  }

  const custom = images.filter((i) => i.custom).length;
  main.innerHTML = `<div class="page-head"><h1>${icon('disk')} ${esc(name)}</h1></div>
    <p class="muted" style="margin-bottom:14px">${images.length} image${images.length === 1 ? '' : 's'}${custom ? ` · ${custom} added here` : ''}</p>
    <table class="template-table">
      <thead><tr><th>Image</th><th>Description</th><th>Disk</th><th>Variant</th><th>Default user</th></tr></thead>
      <tbody>${images.map((i) => `<tr>
        <td>${icon(i.custom ? 'template' : 'disk')} ${esc(i.name)}${i.custom ? ' <span class="chip mini">custom</span>' : ''}</td>
        <td class="muted">${esc(i.description || '')}</td>
        <td><code>${esc(i.containerDisk || '')}</code></td>
        <td class="muted">${esc(i.variant || '')}</td>
        <td class="muted">${esc(i.defaultUser || '')}</td>
      </tr>`).join('')}</tbody>
    </table>`;
}

function renderDataVolumes(main, ns) {
  const dvs = (state.dataVolumes || []).filter((d) => (d.namespace || '(none)') === ns);
  main.innerHTML = `<div class="page-head"><h1>${icon('disk')} ${esc(ns)} disks</h1></div>
    ${dvs.length ? `<table class="template-table">
      <thead><tr><th>Disk</th><th>Phase</th><th>Size</th><th>Source</th></tr></thead>
      <tbody>${dvs.map((d) => `<tr>
        <td>${esc(d.name)}</td>
        <td>${esc(d.phase || '')}</td>
        <td class="muted">${esc(d.size || '')}</td>
        <td class="muted">${esc(d.source || d.url || '')}</td>
      </tr>`).join('')}</tbody>
    </table>` : '<p class="muted">No imported disks in this namespace.</p>'}`;
}
