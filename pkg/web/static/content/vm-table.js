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
import { esc, reportFailures, toast } from '../ui/dom.js';
import { attachContextMenu, openMenuFrom } from '../ui/menu.js';
import { post } from './vm.js';
import { confirmDestroy } from '../ui/confirm.js';

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
  // One action on every row, and the action is the one the row's state allows.
  //
  // Until now every action here needed a checkbox and then the bulk bar above,
  // or opening the guest. Cockpit's machine list puts a single button on each
  // row and swaps it between Run and Shut down with the guest's state, so the
  // common case costs one click and the button never offers something the
  // guest cannot do. The rest of the actions stay one menu away, which is the
  // same set the row's right-click already offers.
  { id: 'actions', label: 'Actions', width: 128, plain: true, render: (vm) => {
    const wrap = document.createElement('span');
    wrap.className = 'row-acts';
    const up = vm.running || (vm.status && (vm.status.includes('Starting') || vm.status.includes('Creating')));
    const act = up ? 'stop' : 'start';
    const primary = document.createElement('button');
    primary.type = 'button';
    primary.className = 'btn sm';
    primary.dataset.rowAction = act;
    primary.title = up ? `Stop ${vm.name}` : `Start ${vm.name}`;
    primary.setAttribute('aria-label', primary.title);
    primary.innerHTML = icon(up ? 'stop' : 'play');
    primary.onclick = async (event) => {
      // The row itself opens the guest, so an action inside it must not also.
      event.stopPropagation();
      primary.disabled = true;
      try {
        await api(vmURL(vm, `/${act}`), { method: 'POST' });
        toast(`${up ? 'Stop' : 'Start'} ${vm.name}: ok`);
      } catch {
        toast(`${up ? 'Stop' : 'Start'} ${vm.name}: failed`);
      }
      setTimeout(() => refresh(), 800);
    };
    const more = document.createElement('button');
    more.type = 'button';
    more.className = 'btn sm ghost';
    more.dataset.rowAction = 'more';
    more.title = `More actions for ${vm.name}`;
    more.setAttribute('aria-label', more.title);
    more.textContent = '\u22ef';
    more.onclick = (event) => {
      event.stopPropagation();
      openMenuFrom(more, () => vmMenuItems(vm));
    };
    wrap.append(primary, more);
    return wrap;
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
        // One name would be the wrong thing to type for a selection of many,
        // so this asks for the word instead, and lists every guest above it.
        // The gate is there to make the operator read the list, which a
        // single default-focused OK button never did.
        if (!await confirmDestroy({
          title: `Delete ${plural} and their disks?`,
          identifier: 'delete',
          label: 'Type delete to confirm',
          items: sel.map((v) => v.name),
          note: 'Every guest listed above loses its disks. There is no undo.',
        })) return;
      } else if (!confirm(`${verb} ${plural}?`)) return;
      // Keep what the API said about each guest, not just a tally. "2 failed"
      // tells the operator nothing they can act on: a lock, a missing disk and
      // a vanished node all need something different done next, and the toast
      // that carried the count was gone before they could ask.
      let ok = 0;
      const failures = [];
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
        } catch (e) { failures.push({ name: vm.name, error: e.message }); }
      }));
      toast(`${verb}: ${ok} ok${failures.length ? `, ${failures.length} failed` : ''}`);
      // Only when something went wrong. A dialog after a clean run would be a
      // box to dismiss for no reason.
      reportFailures(`${verb}: ${failures.length} of ${sel.length} failed`, failures);
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