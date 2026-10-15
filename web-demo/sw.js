// Corral demo service worker (#284). It boots the dashboard, compiled to
// WebAssembly (_demo/corral.wasm), and answers the page's requests with it,
// so the whole UI runs in the browser against the in-memory demo cluster.
//
// Routing, for every same-origin request a controlled page makes:
//   <scope>sw.js and <scope>_demo/*  -> the network (this site's own files)
//   <scope><path>                    -> the Go handler, as /<path>
//   any other path, such as /api/vms -> the Go handler, unchanged
// The last rule matters when the site lives under a sub-path, as on GitHub
// Pages: app.js asks for absolute /api/... paths outside the scope.

importScripts('_demo/wasm_exec.js');

const scope = new URL(self.registration.scope).pathname;
let ready = null;

function boot() {
  if (!ready) {
    ready = (async () => {
      const go = new Go();
      const res = await fetch(scope + '_demo/corral.wasm');
      const { instance } = await WebAssembly.instantiate(await res.arrayBuffer(), go.importObject);
      go.run(instance);
      while (typeof self.corralServe !== 'function') {
        await new Promise((r) => setTimeout(r, 10));
      }
    })();
    // A failed boot must not stick: let the next request try again.
    ready.catch(() => { ready = null; });
  }
  return ready;
}

self.addEventListener('install', (event) => {
  event.waitUntil(boot().then(() => self.skipWaiting()));
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  if (url.origin !== self.location.origin) return;
  let path = url.pathname;
  if (path === scope + 'sw.js' || path.startsWith(scope + '_demo/')) return;
  if (path.startsWith(scope)) path = '/' + path.slice(scope.length);
  event.respondWith(handle(event.request, path + url.search));
});

async function handle(request, target) {
  await boot();
  const headers = [...request.headers.entries()];
  const body = ['GET', 'HEAD'].includes(request.method)
    ? null
    : new Uint8Array(await request.arrayBuffer());
  const res = await self.corralServe(request.method, target, headers, body);
  // A null-body status (204, 304) must not carry a body.
  const noBody = [101, 204, 205, 304].includes(res.status) || request.method === 'HEAD';
  return new Response(noBody ? null : res.body, { status: res.status, headers: res.headers });
}
