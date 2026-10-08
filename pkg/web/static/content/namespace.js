// Namespace screen: VMs and containers in one namespace.

import { icon } from '../icons.js';
import { state } from '../state.js';
import { esc } from '../ui/dom.js';
import { bindCTTable, ctTable } from './ct.js';
import { bindVMTable, vmTable } from './vm-table.js';

// Namespace View's namespace detail — same shape as renderNode, grouped by
// namespace instead of node.
export function renderNamespace(main, name) {
  const nsVMs = state.vms.filter((v) => (v.namespace || '(none)') === name);
  const nsCTs = state.cts.filter((c) => (c.namespace || '(none)') === name);
  main.innerHTML = `
    <div class="page-head">
      <h1>${icon('folder')} ${esc(name)}</h1>
    </div>
    <dl class="props">
      <dt>VMs</dt><dd>${nsVMs.length}</dd>
      <dt>CTs</dt><dd>${nsCTs.length}</dd>
    </dl>
    <h2 style="font-size:1rem;margin:18px 0 8px">Virtual machines</h2>
    ${vmTable(nsVMs)}
    ${nsCTs.length ? `<h2 style="font-size:1rem;margin:18px 0 8px">Containers</h2>${ctTable(nsCTs)}` : ''}`;
  bindVMTable(main, nsVMs);
  bindCTTable(main);
}