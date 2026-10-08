// VM tags: chips with remove buttons and the inline add control.

import { refresh } from './app.js';
import { post } from './content/vm.js';
import { icon } from './icons.js';
import { $, esc, toast } from './ui/dom.js';

// Add (on=true) or remove (on=false) a tag on a VM, then refresh.
export async function setTag(vm, tag, on) {
  try { await post(vm, '/tags', { tag, on }); }
  catch (e) { toast(e.message); return; }
  setTimeout(() => refresh(), 500);
}

// Removable tag chips for a VM, plus an "+ add" affordance (bound by bindTags).
export function tagChips(vm) {
  const tags = vm.tags || [];
  const chips = tags.map((t) =>
    `<span class="chip">${esc(t)}<button class="chip-x" data-untag="${esc(t)}" title="Remove tag">×</button></span>`).join('');
  return `${chips || '<span class="muted">none</span>'}
    <button class="btn sm" id="tag-add">${icon('plus')} Tag</button>`;
}

export function bindTags(vm) {
  const el = $('#vm-tags');
  if (!el) return;
  el.querySelectorAll('[data-untag]').forEach((b) => {
    b.onclick = () => setTag(vm, b.dataset.untag, false);
  });
  const add = el.querySelector('#tag-add');
  if (add) add.onclick = () => {
    const t = prompt('Add tag (letters, digits, -_.):', '');
    if (t && t.trim()) setTag(vm, t.trim(), true);
  };
}