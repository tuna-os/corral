// Context menu items for each inventory object (VM, container, node, host,
// namespace, pool). Item lists only; ui/menu.js draws them.

import { api, ctKey, vmKey, vmURL } from './api.js';
import { markRendered, refresh, renderContent, select } from './app.js';
import { ctAction } from './content/ct.js';
import { cloneVM, exportVM, migrateVM, post, vmAction } from './content/vm.js';
import { loadPools, poolState, showMoveDialog, summariseOutcomes } from './pools.js';
import { state } from './state.js';
import { setTag } from './tags.js';
import { capabilityMenuItems } from './ui/capabilities.js';
import { toast } from './ui/dom.js';

export function vmMenuItems(vm) {
  const capability = vm.capabilities || {};
  const isKubeVirt = vm.backend === 'kubevirt';
  const items = [];

  // Console / Terminal / RDP options
  if (capability.vnc) {
    items.push({
      icon: 'desktop',
      label: 'Console (VNC)',
      action: () => {
        select({ type: 'vm', key: vmKey(vm) });
        state.tab = 'console';
        renderContent();
        markRendered();
      },
    });
  }
  if (capability.tty) {
    items.push({
      icon: 'terminal',
      label: 'Terminal (Serial)',
      action: () => {
        select({ type: 'vm', key: vmKey(vm) });
        state.tab = 'terminal';
        renderContent();
        markRendered();
      },
    });
  }
  if (capability.rdp) {
    items.push({
      icon: 'desktop',
      label: 'RDP Console',
      action: () => {
        select({ type: 'vm', key: vmKey(vm) });
        state.tab = 'rdp';
        renderContent();
        markRendered();
      },
    });
  }
  if (!capability.vnc && !capability.tty && !capability.rdp) {
    items.push({
      icon: 'info',
      label: 'Open summary',
      action: () => select({ type: 'vm', key: vmKey(vm) }),
    });
  }

  items.push({ separator: true });

  // Power actions
  if (capability.start !== false) {
    items.push({
      icon: 'play',
      label: 'Start',
      mutate: true,
      disabled: !!vm.running,
      action: () => vmAction(vm, 'start'),
    });
  }
  if (capability.stop !== false) {
    items.push({
      icon: 'stop',
      label: 'Stop',
      mutate: true,
      disabled: !vm.running,
      action: () => vmAction(vm, 'stop'),
    });
  }
  if (capability.start !== false && capability.stop !== false) {
    items.push({
      icon: 'restart',
      label: 'Restart',
      mutate: true,
      disabled: !vm.running,
      action: () => vmAction(vm, 'restart'),
    });
  }

  // Cluster actions
  if (isKubeVirt) {
    items.push({ separator: true });
    items.push({
      icon: 'migrate',
      label: 'Migrate…',
      mutate: true,
      disabled: !(vm.ready && vm.liveMigratable),
      title: vm.liveMigratable ? 'Live-migrate to another node' : 'Not live-migratable (persistent RWO disk)',
      action: () => migrateVM(vm),
    });

    if (capability.snapshots) {
      items.push({
        icon: 'camera',
        label: 'Take snapshot',
        mutate: true,
        action: async () => {
          try {
            await post(vm, '/snapshots', {});
            toast('Snapshot started');
            refresh(true);
          } catch (e) {
            toast(e.message);
          }
        },
      });
    }

    items.push({
      icon: 'clone',
      label: 'Clone…',
      mutate: true,
      action: () => cloneVM(vm),
    });

    items.push({
      icon: 'template',
      label: vm.isTemplate ? 'Unmark template' : 'Convert to template',
      mutate: true,
      action: () => vmAction(vm, 'template'),
    });

    items.push({
      icon: 'plus',
      label: 'Add tag…',
      mutate: true,
      action: () => {
        const t = prompt('Add tag (letters, digits, -_.):', '');
        if (t && t.trim()) setTag(vm, t.trim(), true);
      },
    });
  }

  // Drag-equivalent actions: Assign to Pool and Move to Backend
  items.push({ separator: true });

  items.push({
    icon: 'folder',
    label: 'Assign to pool…',
    mutate: true,
    action: async () => {
      const state = poolState();
      const paths = (state.folders || []).map((f) => f.path);
      const promptMsg = paths.length
        ? `Assign ${vm.name} to pool (leave empty to unassign):\nAvailable pools: ${paths.join(', ')}`
        : `Assign ${vm.name} to pool path (leave empty to unassign):`;
      const chosen = prompt(promptMsg, '');
      if (chosen === null) return;
      const ref = vm.id || vmKey(vm);
      try {
        if (!chosen.trim()) {
          await api(`/api/folders/members?ref=${encodeURIComponent(ref)}`, { method: 'DELETE' });
          toast('Removed from its pool');
        } else {
          await api('/api/folders/members', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path: chosen.trim(), ref }),
          });
          toast(`Added to ${chosen.trim()}`);
        }
        await loadPools();
        refresh(true);
      } catch (err) {
        toast(err.message);
      }
    },
  });

  items.push({
    icon: 'server',
    label: 'Move to backend…',
    mutate: true,
    action: async () => {
      let dests = [];
      try { dests = (await api('/api/move/destinations')).destinations || []; } catch {}
      const available = dests.filter((d) => d.can && d.backend !== vm.backend).map((d) => d.backend);
      if (!available.length) {
        toast('No destination backend available for move.');
        return;
      }
      const chosen = prompt(`Move ${vm.name} to which backend? (${available.join(', ')})`, available[0]);
      if (!chosen || !chosen.trim()) return;
      const ref = vm.id || vmKey(vm);
      let plan;
      try {
        plan = await api('/api/move/preflight', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ ref, toBackend: chosen.trim() }),
        });
      } catch (e) {
        toast(`Could not plan move: ${e.message}`);
        return;
      }
      showMoveDialog(ref, chosen.trim(), plan);
    },
  });

  if (isKubeVirt) {
    items.push({
      icon: 'download',
      label: 'Export…',
      mutate: true,
      disabled: !!vm.running,
      title: vm.running ? 'Stop the VM to export its disk' : 'Download a disk backup',
      action: () => exportVM(vm),
    });
  }

  // Delete action
  if (capability.delete !== false) {
    items.push({ separator: true });
    items.push({
      icon: 'trash',
      label: 'Delete',
      danger: true,
      mutate: true,
      action: () => vmAction(vm, 'delete'),
    });
  }

  return items;
}

export function ctMenuItems(c) {
  const running = c.phase === 'Running';
  return [
    {
      icon: 'terminal',
      label: 'Terminal',
      action: () => {
        select({ type: 'ct', key: ctKey(c) });
        state.ctTab = 'terminal';
        renderContent();
        markRendered();
      },
    },
    { separator: true },
    {
      icon: 'play',
      label: 'Start',
      mutate: true,
      disabled: running,
      action: () => ctAction(c, 'start'),
    },
    {
      icon: 'stop',
      label: 'Stop',
      mutate: true,
      disabled: !running,
      action: () => ctAction(c, 'stop'),
    },
    { separator: true },
    {
      icon: 'trash',
      label: 'Delete',
      danger: true,
      mutate: true,
      action: () => ctAction(c, 'delete'),
    },
  ];
}

export function nodeMenuItems(nodeName) {
  const items = [
    {
      icon: 'server',
      label: 'View node',
      action: () => select({ type: 'node', name: nodeName }),
    },
  ];

  // Entries a capability adds for this node, for example the power actions of
  // the machine that carries it.
  const extra = capabilityMenuItems('node', nodeName);
  if (extra.length) {
    items.push({ separator: true }, ...extra);
  }

  items.push({ separator: true });
  items.push({
    icon: 'pause',
    label: 'Cordon',
    mutate: true,
    disabled: true,
    title: 'Cordon not supported on this backend',
    action: () => {},
  });
  items.push({
    icon: 'migrate',
    label: 'Drain',
    mutate: true,
    disabled: true,
    title: 'Drain not supported on this backend',
    action: () => {},
  });

  return items;
}


export function namespaceMenuItems(ns) {
  const nsVMs = state.vms.filter((v) => (v.namespace || '(none)') === ns);
  const running = nsVMs.filter((v) => v.running);
  const stopped = nsVMs.filter((v) => !v.running);

  return [
    {
      icon: 'folder',
      label: 'View namespace',
      action: () => select({ type: 'namespace', name: ns }),
    },
    { separator: true },
    {
      icon: 'play',
      label: 'Start all VMs',
      mutate: true,
      disabled: stopped.length === 0,
      action: async () => {
        if (!confirm(`Start ${stopped.length} stopped VM(s) in ${ns}?`)) return;
        let ok = 0; let fail = 0;
        await Promise.all(stopped.map(async (v) => {
          try { await api(vmURL(v, '/start'), { method: 'POST' }); ok++; }
          catch { fail++; }
        }));
        toast(`Start: ${ok} ok${fail ? `, ${fail} failed` : ''}`);
        setTimeout(() => refresh(true), 800);
      },
    },
    {
      icon: 'stop',
      label: 'Stop all VMs',
      mutate: true,
      disabled: running.length === 0,
      action: async () => {
        if (!confirm(`Stop ${running.length} running VM(s) in ${ns}?`)) return;
        let ok = 0; let fail = 0;
        await Promise.all(running.map(async (v) => {
          try { await api(vmURL(v, '/stop'), { method: 'POST' }); ok++; }
          catch { fail++; }
        }));
        toast(`Stop: ${ok} ok${fail ? `, ${fail} failed` : ''}`);
        setTimeout(() => refresh(true), 800);
      },
    },
  ];
}

export function poolMenuItems(folder) {
  const n = (folder.members || []).length;
  return [
    {
      icon: 'play',
      label: 'Start all',
      mutate: true,
      disabled: n === 0,
      action: async () => {
        if (!confirm(`Start every instance in pool ${folder.path}? (${n} instances)`)) return;
        try {
          const res = await api('/api/folders/action', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path: folder.path, action: 'start' }),
          });
          toast(summariseOutcomes('start', res.members || []));
          refresh(true);
        } catch (err) { toast(err.message); }
      },
    },
    {
      icon: 'stop',
      label: 'Stop all',
      mutate: true,
      disabled: n === 0,
      action: async () => {
        if (!confirm(`Stop every instance in pool ${folder.path}? (${n} instances)`)) return;
        try {
          const res = await api('/api/folders/action', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path: folder.path, action: 'stop' }),
          });
          toast(summariseOutcomes('stop', res.members || []));
          refresh(true);
        } catch (err) { toast(err.message); }
      },
    },
    {
      icon: 'restart',
      label: 'Restart all',
      mutate: true,
      disabled: n === 0,
      action: async () => {
        if (!confirm(`Restart every instance in pool ${folder.path}? (${n} instances)`)) return;
        try {
          const res = await api('/api/folders/action', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path: folder.path, action: 'restart' }),
          });
          toast(summariseOutcomes('restart', res.members || []));
          refresh(true);
        } catch (err) { toast(err.message); }
      },
    },
    { separator: true },
    {
      icon: 'plus',
      label: 'New subpool…',
      mutate: true,
      action: async () => {
        const p = prompt(`New subpool under ${folder.path} (e.g. ${folder.path}/sub):`, `${folder.path}/`);
        if (!p) return;
        try {
          await api('/api/folders', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path: p.trim() }),
          });
          await loadPools();
          refresh(true);
        } catch (err) { toast(err.message); }
      },
    },
    {
      icon: 'trash',
      label: 'Delete pool',
      danger: true,
      mutate: true,
      action: async () => {
        if (!confirm(`Delete pool ${folder.path}? Members will be unfoldered, not deleted.`)) return;
        try {
          await api(`/api/folders?path=${encodeURIComponent(folder.path)}`, { method: 'DELETE' });
          toast('Pool deleted');
          await loadPools();
          refresh(true);
        } catch (err) { toast(err.message); }
      },
    },
  ];
}

export function unassignedMenuItems() {
  return [
    {
      icon: 'plus',
      label: 'New pool…',
      mutate: true,
      action: async () => {
        const path = prompt('Pool path (nest with /, e.g. prod/web):');
        if (!path) return;
        try {
          await api('/api/folders', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path }),
          });
          await loadPools();
          refresh(true);
        } catch (err) { toast(err.message); }
      },
    },
  ];
}