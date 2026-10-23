import { api } from '../api.js';
import { refresh } from '../app.js';
import { icon } from '../icons.js';
import { dataVolumeMenuItems, imageSourceMenuItems } from '../menus.js';
import { state } from '../state.js';
import { confirmDestroy } from '../ui/confirm.js';
import { esc, toast } from '../ui/dom.js';
import { attachContextMenu } from '../ui/menu.js';

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

  const removeSource = async (img) => {
    if (!confirm(`Remove custom source "${img.name}"?`)) return;
    try {
      await api(`/api/sources/${encodeURIComponent(img.name)}`, { method: 'DELETE' });
      toast(`Removed source ${img.name}`);
      refresh(true);
    } catch (err) { toast(err.message); }
  };

  main.querySelectorAll('tbody tr').forEach((tr, idx) => {
    const img = images[idx];
    if (img) attachContextMenu(tr, () => imageSourceMenuItems(img, { onRemove: removeSource }));
  });
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

  const deleteDV = async (d) => {
    if (!await confirmDestroy({
      title: `Delete image ${d.name}?`,
      identifier: d.name,
      label: `Type ${d.name} to confirm`,
      note: 'Any guest built from it keeps its disk; the image itself is gone.',
    })) return;
    try {
      await api(`/api/datavolumes/${d.namespace}/${d.name}`, { method: 'DELETE' });
      toast('Deleted');
      refresh(true);
    } catch (e) { toast(e.message); }
  };

  main.querySelectorAll('tbody tr').forEach((tr, idx) => {
    const dv = dvs[idx];
    if (dv) attachContextMenu(tr, () => dataVolumeMenuItems(dv, { onDelete: deleteDV }));
  });
}
