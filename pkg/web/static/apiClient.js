/**
 * API Client for Corral Web Dashboard
 * 
 * Handles all HTTP communication with the Corral backend API,
 * centralizing request construction, response handling, and error management.
 * 
 * Phase 1: Extract API transport and endpoint construction
 * Phase 2: Add request caching and optimistic updates
 * Phase 3: Add batch request support
 * Phase 4: Add WebSocket transport for real-time updates
 */

/**
 * Execute an API request and handle responses.
 * 
 * @param {string} path - API endpoint path
 * @param {Object} [opts={}] - Fetch options
 * @returns {Promise<Object|string>} Parsed JSON response or plain text
 * @throws {Error} If the request fails or returns an error status
 */
export async function api(path, opts = {}) {
  const r = await fetch(path, opts);
  if (!r.ok) {
    let msg = r.statusText;
    try { msg = (await r.json()).error || msg; } catch { /* not json */ }
    throw new Error(msg);
  }
  return r.headers.get('content-type')?.includes('json') ? r.json() : r.text();
}

/**
 * Construct the API URL for a specific VM.
 * 
 * @param {Object} vm - VM object with namespace, name, and optional context
 * @param {string} [suffix=''] - Additional path suffix
 * @returns {string} Complete API URL for the VM
 * 
 * @example
 * // Returns: /api/vms/default/my-vm?context=cluster-2
 * vmURL({ namespace: 'default', name: 'my-vm', context: 'cluster-2' })
 */
export function vmURL(vm, suffix = '') {
  return `/api/vms/${vm.namespace}/${vm.name}${suffix}${vm.context ? `?context=${encodeURIComponent(vm.context)}` : ''}`;
}

/**
 * Construct a unique key for a container.
 * 
 * @param {Object} c - Container object with namespace and name
 * @returns {string} Composite key in format "namespace/name"
 */
export function ctKey(c) {
  return `${c.namespace}/${c.name}`;
}

/**
 * Construct a unique key for a VM including peer and context.
 * 
 * @param {Object} vm - VM object with peer, context, namespace, and name
 * @returns {string} Composite key in format "peer/context/namespace/name"
 */
export function vmKey(vm) {
  return `${vm.peer || ''}/${vm.context || ''}/${vm.namespace}/${vm.name}`;
}

/**
 * Fetch a list of VMs from the API.
 * 
 * @returns {Promise<Array>} Array of VM objects
 * @throws {Error} If the request fails
 */
export async function listVMs() {
  const r = await api('/api/vms');
  return r.vms || [];
}

/**
 * Fetch a list of contexts (Kubernetes clusters, local hosts, etc.) from the API.
 * 
 * @returns {Promise<Array>} Array of context objects
 * @throws {Error} If the request fails
 */
export async function listContexts() {
  const r = await api('/api/contexts');
  return r.contexts || [];
}

/**
 * Fetch the current user's identity and capabilities.
 * 
 * @returns {Promise<Object>} User identity object
 * @throws {Error} If the request fails
 */
export async function getWhoami() {
  return api('/api/whoami');
}

/**
 * Send a POST request to the API.
 * 
 * @param {string} path - API endpoint path
 * @param {Object} body - Request body object (will be JSON-stringified)
 * @returns {Promise<Object|string>} API response
 * @throws {Error} If the request fails
 */
export async function post(path, body) {
  return api(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}

/**
 * Send a PATCH request to the API.
 * 
 * @param {string} path - API endpoint path
 * @param {Object} body - Request body object (will be JSON-stringified)
 * @returns {Promise<Object|string>} API response
 * @throws {Error} If the request fails
 */
export async function patch(path, body) {
  return api(path, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}

/**
 * Send a DELETE request to the API.
 * 
 * @param {string} path - API endpoint path
 * @returns {Promise<void>}
 * @throws {Error} If the request fails
 */
export async function deleteRequest(path) {
  return api(path, { method: 'DELETE' });
}
