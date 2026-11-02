// The screens the poll draws when it cannot show the fleet: the tailnet
// identity is gone (401), or no backend could list any VMs. Each takes the
// retry action from refresh.js, which owns the poll. Unit tests:
// pkg/web/jstest/unavailable.test.mjs.

import { api } from '../api.js';
import { $, esc } from '../ui/dom.js';

// The page lost the identity corral authorises against.
//
// corral has no login of its own. The Tailscale ingress in front of it proves
// who the caller is and passes that on with each request, and with
// CORRAL_AUTH_REQUIRED set, a request without it is refused with a 401. So
// this is not a session that ran out and there is no form to fill in: the fix
// is on the operator's device. Polling continues underneath, so the page
// recovers by itself when the identity returns.
export function renderIdentityLost(onRetry) {
  const content = $('#content');
  if (!content) return;
  content.innerHTML = `
    <div class="empty-state" id="identity-lost">
      <div class="empty-icon">🔒</div>
      <h1>corral can no longer tell who you are</h1>
      <p class="muted">It reads your identity from your tailnet connection, and
        that stopped arriving with this page's requests.</p>
      <ul>
        <li>Check that Tailscale is connected on this device, and that its
          session has not expired.</li>
        <li>Open corral by its tailnet address. A port-forward or a local
          address carries no identity.</li>
      </ul>
      <p class="muted">corral keeps checking, and this page comes back by
        itself once you are reconnected.</p>
      <button class="btn primary" id="identity-retry">Check now</button>
    </div>`;
  content.querySelector('#identity-retry').onclick = onRetry;
}

// First-load failure page: couldn't list any VMs. Tailor the guidance to the
// configured backends — only nag about connecting a KubeVirt cluster when
// kubevirt is actually a configured target. A host that only runs local
// QEMU/Incus/libvirt VMs should never be told to go install KubeVirt.
export async function renderOffline(msg, onRetry) {
  const content = $('#content');
  if (!content) return;
  let backends = [];
  try {
    const r = await api('/api/contexts');
    backends = (r.contexts || []).map((c) => c.backend);
  } catch { /* fall through to the cluster-oriented default */ }
  content.innerHTML = offlineMarkup(backends, msg);
  const b = $('#offline-retry');
  if (b) b.onclick = onRetry;
}

// The offline page for these configured backends. No backends at all (the
// contexts fetch failed too) gets the cluster message, which also carries the
// local-CLI fallback hint.
export function offlineMarkup(backends, msg) {
  const hasKubevirt = backends.length === 0 || backends.includes('kubevirt');
  if (hasKubevirt) {
    return `
      <div class="empty-state">
        <div class="empty-icon">🤠</div>
        <h1>No cluster connected</h1>
        <p class="muted">Corral couldn't reach a Kubernetes cluster:
          <code>${esc(msg)}</code></p>
        <p>This dashboard drives the KubeVirt backend. To get going:</p>
        <ul>
          <li>Point <code>kubectl</code> at a cluster (<code>KUBECONFIG</code> or
            <code>~/.kube/config</code>), then hit Retry.</li>
          <li>Run <code>corral doctor --fix</code> to install anything the
            cluster is missing (KubeVirt, CDI, …).</li>
          <li>No cluster handy? Local QEMU VMs work from the CLI:
            <code>corral create myvm --image fedora</code></li>
        </ul>
        <button class="btn primary" id="offline-retry">Retry</button>
      </div>`;
  }
  const names = [...new Set(backends.filter((b) => b !== 'kubevirt'))].join(', ') || 'local';
  return `
      <div class="empty-state">
        <div class="empty-icon">🤠</div>
        <h1>No VMs yet</h1>
        <p class="muted">Couldn't list VMs from the ${esc(names)} backend:
          <code>${esc(msg)}</code></p>
        <p>Create your first VM from the CLI, then hit Retry:</p>
        <ul>
          <li><code>corral create myvm --image fedora</code></li>
        </ul>
        <button class="btn primary" id="offline-retry">Retry</button>
      </div>`;
}
