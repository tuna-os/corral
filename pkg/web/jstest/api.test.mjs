// Unit tests for the web client's API transport (pkg/web/static/api.js).
//
// Plain `node --test`, no npm install: the client is native ES modules with no
// build step (ADR-0004), so Node imports the shipped files exactly as the
// browser does. Tests live outside static/ so go:embed does not ship them.
//
//   just test-js        (node --test pkg/web/jstest/*.test.mjs)

import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { api, ctKey, ctURL, findCT, findVM, rdpURL, sendJSON, vmKey, vmURL, wsURL } from '../static/api.js';
import { state } from '../static/state.js';

const realFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = realFetch; });

// Answer every fetch with `response`, and record what was asked for.
function stubFetch(response) {
  const calls = [];
  globalThis.fetch = async (path, opts) => {
    calls.push({ path, opts });
    if (response instanceof Error) throw response;
    return response;
  };
  return calls;
}

const json = (body, init = {}) => new Response(JSON.stringify(body), {
  status: 200, headers: { 'Content-Type': 'application/json' }, ...init,
});

describe('api', () => {
  test('passes the path and options to fetch unchanged', async () => {
    const calls = stubFetch(json({ ok: true }));
    const opts = { method: 'POST', body: '{}' };
    await api('/api/vms', opts);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].path, '/api/vms');
    assert.equal(calls[0].opts, opts);
  });

  test('defaults the options to an empty object', async () => {
    const calls = stubFetch(json([]));
    await api('/api/vms');
    assert.deepEqual(calls[0].opts, {});
  });

  test('decodes a JSON response', async () => {
    stubFetch(json([{ name: 'web-prod' }]));
    assert.deepEqual(await api('/api/vms'), [{ name: 'web-prod' }]);
  });

  test('decodes JSON whatever the media type parameters', async () => {
    stubFetch(json({ a: 1 }, { headers: { 'Content-Type': 'application/json; charset=utf-8' } }));
    assert.deepEqual(await api('/x'), { a: 1 });
  });

  test('returns any other response as text', async () => {
    stubFetch(new Response('plain words', { status: 200, headers: { 'Content-Type': 'text/plain' } }));
    assert.equal(await api('/x'), 'plain words');
  });

  test('returns text when there is no content type', async () => {
    const r = new Response('');
    r.headers.delete('content-type');
    stubFetch(r);
    assert.equal(await api('/x'), '');
  });

  test('a request that never reached the server says so', async () => {
    stubFetch(new TypeError('Failed to fetch'));
    await assert.rejects(api('/api/vms'), (err) => {
      assert.equal(err.message, 'Cannot reach the server — check that corral is still running');
      assert.equal(err.status, undefined);
      return true;
    });
  });

  test('a refusal carries the server error field and the status', async () => {
    stubFetch(json({ error: 'forbidden for viewers' }, { status: 403, statusText: 'Forbidden' }));
    await assert.rejects(api('/api/vms'), (err) => {
      assert.equal(err.message, 'forbidden for viewers (403)');
      assert.equal(err.status, 403);
      return true;
    });
  });

  test('the error field wins over the message field', async () => {
    stubFetch(json({ error: 'from error', message: 'from message' }, { status: 400 }));
    await assert.rejects(api('/x'), { message: 'from error (400)', status: 400 });
  });

  test('the message field is used when there is no error field', async () => {
    stubFetch(json({ message: 'from message' }, { status: 422 }));
    await assert.rejects(api('/x'), { message: 'from message (422)', status: 422 });
  });

  test('a body that is not JSON falls back to the reason phrase', async () => {
    stubFetch(new Response('<html>bad gateway</html>', { status: 502, statusText: 'Bad Gateway' }));
    await assert.rejects(api('/x'), { message: 'Bad Gateway (502)', status: 502 });
  });

  test('JSON without a message falls back to the reason phrase', async () => {
    stubFetch(json({}, { status: 500, statusText: 'Internal Server Error' }));
    await assert.rejects(api('/x'), { message: 'Internal Server Error (500)', status: 500 });
  });

  // HTTP/2 has no reason phrase, so behind most proxies statusText is empty.
  test('never an empty message when there is no reason phrase', async () => {
    stubFetch(new Response('', { status: 503, statusText: '' }));
    await assert.rejects(api('/x'), { message: 'Request failed with status 503', status: 503 });
  });

  test('a 401 keeps its status for the identity screen', async () => {
    stubFetch(json({ error: 'no tailnet identity' }, { status: 401 }));
    await assert.rejects(api('/api/vms'), { status: 401 });
  });
});

describe('inventory keys and URLs', () => {
  const vm = { namespace: 'corral-vms', name: 'web-prod' };

  test('vmKey names peer, context, namespace and name', () => {
    assert.equal(vmKey(vm), '//corral-vms/web-prod');
    assert.equal(vmKey({ ...vm, peer: 'east', context: 'lab' }), 'east/lab/corral-vms/web-prod');
  });

  test('vmURL addresses the VM, with an optional suffix', () => {
    assert.equal(vmURL(vm), '/api/vms/corral-vms/web-prod');
    assert.equal(vmURL(vm, '/start'), '/api/vms/corral-vms/web-prod/start');
  });

  test('vmURL carries the context as an encoded query parameter', () => {
    assert.equal(vmURL({ ...vm, context: 'a b&c' }, '/gpus'), '/api/vms/corral-vms/web-prod/gpus?context=a%20b%26c');
  });

  test('ctKey is namespace and name', () => {
    assert.equal(ctKey({ namespace: 'pets', name: 'db' }), 'pets/db');
  });

  test('findVM and findCT look the key up in the shared state', () => {
    state.vms = [vm, { namespace: 'other', name: 'web-prod', context: 'lab' }];
    state.cts = [{ namespace: 'pets', name: 'db' }];
    assert.equal(findVM('/lab/other/web-prod'), state.vms[1]);
    assert.equal(findVM('//nope/web-prod'), undefined);
    assert.equal(findCT('pets/db'), state.cts[0]);
    assert.equal(findCT('pets/none'), undefined);
  });
});

describe('sendJSON', () => {
  test('POSTs the body as a JSON document by default', async () => {
    const calls = stubFetch(json({ task: 7 }));
    const res = await sendJSON('/api/folders', { path: 'lab/a' });
    assert.deepEqual(res, { task: 7 });
    assert.equal(calls[0].path, '/api/folders');
    assert.deepEqual(calls[0].opts, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{"path":"lab/a"}',
    });
  });

  test('takes another method', async () => {
    const calls = stubFetch(json({}));
    await sendJSON('/api/theme', { accent: '#fff' }, 'PUT');
    assert.equal(calls[0].opts.method, 'PUT');
    assert.equal(calls[0].opts.body, '{"accent":"#fff"}');
  });

  test('fails exactly as api() does', async () => {
    stubFetch(json({ error: 'pool exists' }, { status: 409 }));
    await assert.rejects(sendJSON('/api/folders', {}), { message: 'pool exists (409)', status: 409 });
  });
});

describe('console WebSocket URLs', () => {
  const vm = { namespace: 'corral-vms', name: 'web-prod' };
  const realLocation = globalThis.location;
  afterEach(() => { globalThis.location = realLocation; });

  test('ws: on an http page, on the page host', () => {
    globalThis.location = { protocol: 'http:', host: '127.0.0.1:8006' };
    assert.equal(wsURL('vnc', vm), 'ws://127.0.0.1:8006/api/vnc/corral-vms/web-prod');
    assert.equal(wsURL('tty', { ...vm, context: 'lab one' }), 'ws://127.0.0.1:8006/api/tty/corral-vms/web-prod?context=lab%20one');
  });

  test('wss: on an https page', () => {
    globalThis.location = { protocol: 'https:', host: 'corral.example.ts.net' };
    assert.equal(wsURL('vnc', vm), 'wss://corral.example.ts.net/api/vnc/corral-vms/web-prod');
  });

  test('the RDP proxy asks for RDCleanPath and carries no context', () => {
    globalThis.location = { protocol: 'https:', host: 'corral.example.ts.net' };
    assert.equal(rdpURL({ ...vm, context: 'lab' }), 'wss://corral.example.ts.net/api/rdp/corral-vms/web-prod?rdcleanpath=1');
  });
});

test('ctURL addresses the container, with an optional suffix', () => {
  const c = { namespace: 'pets', name: 'db' };
  assert.equal(ctURL(c), '/api/cts/pets/db');
  assert.equal(ctURL(c, '/scale'), '/api/cts/pets/db/scale');
});
