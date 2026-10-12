// Node screen: per-node dashboard and VM list.

import { mountDashboard } from '../dashboard.js';
import { icon } from '../icons.js';
import { state } from '../state.js';
import { capabilityLayout, capabilityWidgets } from '../ui/capabilities.js';
import { $, esc } from '../ui/dom.js';
import { bindCTTable, ctTable } from './ct.js';
import { chartWidget, topVMsWidget } from './datacenter.js';
import { bindVMTable, vmTable } from './vm-table.js';

// The node page shares one saved layout across nodes; the widgets are built
// per node because they filter to it.
const NODE_LAYOUT = [
  { id: 'info', x: 0, y: 0, w: 4, h: 3 },
  { id: 'cpu', x: 4, y: 0, w: 4, h: 3 },
  { id: 'mem', x: 8, y: 0, w: 4, h: 3 },
  { id: 'top-cpu', x: 0, y: 3, w: 4, h: 3 },
  { id: 'top-mem', x: 4, y: 3, w: 4, h: 3 },
];

function nodeWidgets(name) {
  const hist = `/api/nodes/${encodeURIComponent(name)}/metrics/history`;
  return {
    info: {
      title: 'Node', w: 4, h: 3,
      render(body) {
        const n = state.nodes.find((x) => x.name === name);
        body.innerHTML = `<dl class="kv">
          <dt>Status</dt><dd><span class="pill ${n?.ready ? 'on' : 'off'}">${n?.ready ? 'ready' : 'not ready'}</span></dd>
          <dt>Roles</dt><dd>${esc(n?.roles || '—')}</dd>
          <dt>Kubelet</dt><dd>${esc(n?.kubelet || '—')}</dd>
          <dt>Architecture</dt><dd>${esc(n?.arch || '—')}</dd>
          <dt>VMs</dt><dd>${state.vms.filter((v) => v.node === name).length}</dd>
          <dt>CTs</dt><dd>${state.cts.filter((c) => c.node === name).length}</dd>
        </dl>`;
      },
    },
    cpu: chartWidget('CPU usage', hist, 'cpu'),
    mem: chartWidget('Memory usage', hist, 'mem'),
    'top-cpu': topVMsWidget('Top VMs by CPU', 'cpu', name),
    'top-mem': topVMsWidget('Top VMs by memory', 'mem', name),
    ...capabilityWidgets(name),
  };
}

let nodeDash = null;

export function renderNode(main, name) {
  const n = state.nodes.find((x) => x.name === name);
  const nodeVMs = state.vms.filter((v) => v.node === name);
  const nodeCTs = state.cts.filter((c) => c.node === name);
  if (main.querySelector('#node-dash')?.dataset.node !== name) {
    main.innerHTML = `
      <div class="page-head">
        <h1>${icon('server')} ${esc(name)}</h1>
        <span class="pill" id="node-ready"></span>
      </div>
      <div id="node-dash" data-node="${esc(name)}"></div>
      <div id="node-rest"></div>`;
    nodeDash = mountDashboard($('#node-dash'), {
      scope: 'node', widgets: nodeWidgets(name), layout: [...NODE_LAYOUT, ...capabilityLayout(name)],
    });
  } else {
    nodeDash.refresh();
  }
  const pill = $('#node-ready');
  pill.className = `pill ${n?.ready ? 'on' : 'off'}`;
  pill.textContent = n?.ready ? 'ready' : 'not ready';
  const rest = $('#node-rest');
  rest.innerHTML = `
    <h2 style="font-size:1rem;margin:18px 0 8px">Virtual machines</h2>
    ${vmTable(nodeVMs)}
    ${nodeCTs.length ? `<h2 style="font-size:1rem;margin:18px 0 8px">Containers</h2>${ctTable(nodeCTs)}` : ''}`;
  bindVMTable(rest, nodeVMs);
  bindCTTable(rest);
}