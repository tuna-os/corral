// VM and template tables: the inventory grid the Datacenter, Node and
// Namespace screens share.

import { api, findVM, vmKey, vmURL } from '../api.js';
import { refresh, select } from '../app.js';
import { mountGrid } from '../grid.js';
import { icon } from '../icons.js';
import { vmMenuItems } from '../menus.js';
import { makeDraggable } from '../pools.js';
import { state } from '../state.js';
import { renderTree } from '../tree.js';
import { esc, toast } from '../ui/dom.js';
import { attachContextMenu } from '../ui/menu.js';
import { post } from './vm.js';

export function vmTable(list) {
  if (!list.length) return `<p class="console-msg">No virtual machines.</p>`;
  return `<div class="bulkbar" hidden>
      <span class="bulkbar-count">0 selected</span>
      <button class="btn sm" data-bulk="start">${icon('play')} Start</button>
      <button class="btn sm" data-bulk="stop">${icon('stop')} Stop</button>
      <button class="btn sm" data-bulk="restart">${icon('restart')} Restart</button>
      <button class="btn sm" data-bulk="snapshot">${icon('camera')} Snapshot</button>
      <button class="btn sm" data-bulk="tag">Tag…</button>
      <button class="btn sm danger" data-bulk="delete">${icon('trash')} Delete</button>
    </div>
    <div class="vm-grid"></div>`;
}

const VM_GRID_COLUMNS = [
  { id: 'name', label: 'Name', width: 220 },
  { id: 'status', label: 'Status', width: 130, render: (vm) => {
    const value = document.createElement('span');
    const active = vm.running || (vm.status && (vm.status.includes('Starting') || vm.status.includes('Creating')));
    value.innerHTML = `<span class="dot ${vm.ready ? 'on' : active ? 'mid' : 'off'}"></span> `;
    value.append(document.createTextNode(vm.status || '—'));
    return value;
  } },
  { id: 'node', label: 'Node', width: 150, value: (vm) => vm.node || '—' },
  { id: 'namespace', label: 'Namespace', width: 150 },
  { id: 'cpu', label: 'CPU', width: 80 },
  { id: 'mem', label: 'Mem', width: 100 },
  { id: 'ip', label: 'IP', width: 150, value: (vm) => vm.ip || '—' },
  { id: 'tags', label: 'Tags', width: 160, value: (vm) => (vm.tags || []).join(', '), render: (vm) => {
    const chips = document.createElement('span');
    for (const t of vm.tags || []) {
      const chip = document.createElement('span');
      chip.className = 'chip mini';
      chip.textContent = t;
      chips.appendChild(chip);
    }
    return chips;
  } },
];

// The mounted grid, kept across renders.
//
// Each poll rebuilds the markup around the grid, which hands us a fresh empty
// .vm-grid placeholder and would mean mounting a second grid and throwing the
// first away — along with the focused row, a checkbox mid-click and a column
// being dragged. Instead the previously mounted grid is moved into the new
// placeholder's position and fed the new rows, so its DOM is never rebuilt.
// This is the same thing renderDatacenter does with its widget dashboard,
// arranged so the three views that share this table all get it without any of
// them restructuring their markup.
//
// One grid is enough because only one .vm-grid is ever on screen: the
// Datacenter, Node and Namespace views each render one, and the template table
// beside it is a plain table, not a grid.
let gridHost = null;
let gridHandle = null;

export function bindVMTable(root, list) {
  const bar = root.querySelector('.bulkbar');
  if (!bar) return;
  const selectedKeys = () => [...state.selectedVMKeys];
  const update = () => {
    const n = state.selectedVMKeys.size;
    bar.hidden = n === 0;
    bar.querySelector('.bulkbar-count').textContent = `${n} selected`;
  };
  const placeholder = root.querySelector('.vm-grid');
  if (gridHost && gridHandle && placeholder && placeholder !== gridHost) {
    // Reuse the live grid: take the placeholder's place, then re-render with
    // the new rows (which diffs them — see grid.js).
    //
    // Focus and the grid's own scroll position are restored by refresh() in
    // app.js, not here: the view has already replaced the markup around this
    // grid by the time we run, and that is what blurs the focused row, so the
    // only place that can still see where focus was is before the render.
    placeholder.replaceWith(gridHost);
    gridHandle.update(list);
  } else if (placeholder) {
    gridHandle = mountGrid(placeholder, {
      id: 'vms', columns: VM_GRID_COLUMNS, rows: list, rowKey: vmKey,
      selected: state.selectedVMKeys, checkClass: 'vm-check', checkAllClass: 'vm-check-all',
      onRowClick: (vm) => select({ type: 'vm', key: vmKey(vm) }),
      onSelectionChange: () => { update(); renderTree(); },
      decorateRow: (tr, vm) => {
        attachContextMenu(tr, () => vmMenuItems(vm));
        makeDraggable(tr, vm);
      },
    });
    gridHost = gridHandle ? placeholder : null;
  }

  bar.querySelectorAll('[data-bulk]').forEach((b) => {
    b.onclick = async (e) => {
      e.stopPropagation();
      const act = b.dataset.bulk;
      const sel = selectedKeys().map(findVM).filter(Boolean);
      if (!sel.length) return;
      const verb = { start: 'Start', stop: 'Stop', restart: 'Restart', snapshot: 'Snapshot', tag: 'Tag', delete: 'Delete' }[act];
      const plural = `${sel.length} VM${sel.length === 1 ? '' : 's'}`;
      let tag = '';
      if (act === 'tag') {
        tag = (prompt(`Tag ${plural} with:`, '') || '').trim();
        if (!tag) return;
      } else if (act === 'delete') {
        if (!confirm(`Delete ${plural} and their disks?\n\n${sel.map((v) => v.name).join('\n')}`)) return;
      } else if (!confirm(`${verb} ${plural}?`)) return;
      let ok = 0;
      let fail = 0;
      await Promise.all(sel.map(async (vm) => {
        try {
          if (act === 'snapshot') await post(vm, '/snapshots', {});
          else if (act === 'tag') await post(vm, '/tags', { tag, on: true });
          else if (act === 'delete') {
            let target = vmURL(vm);
            if (vm.backend === 'libvirt') target += `${target.includes('?') ? '&' : '?'}destroyStorage=true`;
            await api(target, { method: 'DELETE' });
            state.selectedVMKeys.delete(vmKey(vm));
          } else await api(vmURL(vm, `/${act}`), { method: 'POST' });
          ok += 1;
        } catch { fail += 1; }
      }));
      toast(`${verb}: ${ok} ok${fail ? `, ${fail} failed` : ''}`);
      setTimeout(() => refresh(), 800);
    };
  });

  update();
}

// Templates section of the Datacenter/library view (#49) — VMs already
// marked via the "Make template" action (POST .../template) surfaced as a
// managed list, per the issue's "surface those templates here" ask. Reuses
// the same mark-template endpoint to unmark/remove from here.
export function templateTable(list) {
  if (!list.length) return `<p class="muted">No templates. Mark a VM as a template from its detail page.</p>`;
  return `<table class="template-table"><thead><tr><th>Name</th><th>Namespace</th><th>CPU</th><th>Mem</th><th></th></tr></thead><tbody>
    ${list.map((v) => `<tr data-key="${esc(vmKey(v))}">
      <td>${esc(v.name)}</td><td>${esc(v.namespace)}</td><td>${v.cpu}</td><td>${esc(v.mem)}</td>
      <td><button class="btn sm danger" data-untemplate="${esc(vmKey(v))}">Unmark</button></td>
    </tr>`).join('')}
    </tbody></table>`;
}

export function bindTemplateTable(root) {
  root.querySelectorAll('[data-untemplate]').forEach((b) => {
    b.onclick = async (e) => {
      e.stopPropagation();
      const vm = findVM(b.dataset.untemplate);
      if (!vm) return;
      try { await post(vm, '/template', { on: false }); toast('Template mark removed'); }
      catch (err) { toast(err.message); }
      setTimeout(refresh, 600);
    };
  });
  // Scoped to the template table: the inventory grid binds its own rows.
  root.querySelectorAll('.template-table tr[data-key]').forEach((tr) => {
    const vm = findVM(tr.dataset.key);
    if (vm) attachContextMenu(tr, () => vmMenuItems(vm));
    tr.onclick = (e) => {
      if (e.target.closest('button')) return;
      select({ type: 'vm', key: tr.dataset.key });
    };
    tr.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        if (!e.target.closest('button')) {
          e.preventDefault();
          select({ type: 'vm', key: tr.dataset.key });
        }
      }
    });
  });
}