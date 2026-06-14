// @ts-check
// Corral web UI end-to-end suite. Runs against a LIVE cluster (production is
// fine): every resource it creates is prefixed "e2e-" and torn down in
// afterEach, verified gone. Point it anywhere with CORRAL_URL.
const { test, expect } = require('@playwright/test');
const { execSync, spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const CORRAL_URL = process.env.CORRAL_URL || 'http://localhost:8006';
const NS = process.env.CORRAL_NS || 'corral';

// Small, fast-importing disk images for CDI flows.
const CIRROS_QCOW = 'https://download.cirros-cloud.net/0.6.2/cirros-0.6.2-x86_64-disk.img';
const ALPINE_ISO = 'https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/x86_64/alpine-standard-3.21.0-x86_64.iso';
// Boot image for lifecycle tests. CI (KubeVirt software emulation) overrides
// this with the tiny cirros demo containerdisk; real clusters get fedora.
const CTR_IMAGE = process.env.E2E_CONTAINERDISK || 'quay.io/containerdisks/fedora:42';

// Tests tagged @live-only need a real cluster (KVM boots at speed, longhorn
// snapshots, virtctl/ssh from the runner, bootc builder). CI runs the rest:
//   npx playwright test --grep-invert @live-only

// ── shell + API helpers ─────────────────────────────────────────────

function kubectl(cmd, timeout = 30000) {
  try { return execSync(cmd, { timeout, encoding: 'utf8' }).trim(); }
  catch { return ''; }
}
const vmExists = (n) => kubectl(`kubectl get vm ${n} -n ${NS} -o name --ignore-not-found`) !== '';
const dvExists = (n) => kubectl(`kubectl get dv ${n} -n ${NS} -o name --ignore-not-found`) !== '';
const vmStatus = (n) => kubectl(`kubectl get vm ${n} -n ${NS} -o jsonpath='{.status.printableStatus}'`).replace(/'/g, '');
const dvPhase = (n) => kubectl(`kubectl get dv ${n} -n ${NS} -o jsonpath='{.status.phase}'`).replace(/'/g, '');
const vmiPhase = (n) => kubectl(`kubectl get vmi ${n} -n ${NS} -o jsonpath='{.status.phase}' --ignore-not-found`).replace(/'/g, '');

// assertVMHealthy cross-checks a "Running" VM from the cluster side: the VMI
// is in phase Running, the virt-launcher pod is Running, and the qemu process
// is actually alive in the pod (compute container logs exist). The UI saying
// so is not enough — this is the independent angle.
function assertVMHealthy(expectFn, name) {
  expectFn(vmiPhase(name), `VMI ${name} phase`).toBe('Running');
  const pod = kubectl(`kubectl get pod -n ${NS} -l vm.kubevirt.io/name=${name} -o jsonpath='{.items[0].metadata.name}'`).replace(/'/g, '');
  expectFn(pod, `virt-launcher pod for ${name}`).not.toBe('');
  const podPhase = kubectl(`kubectl get pod ${pod} -n ${NS} -o jsonpath='{.status.phase}'`).replace(/'/g, '');
  expectFn(podPhase, `pod ${pod} phase`).toBe('Running');
  const logs = kubectl(`kubectl logs ${pod} -n ${NS} -c compute --tail=5`);
  expectFn(logs.length > 0, `pod ${pod} compute container produces logs`).toBe(true);
}

const delay = (ms) => new Promise((r) => setTimeout(r, ms));
async function waitFor(fn, ms, step = 3000, what = 'condition') {
  const t = Date.now();
  while (Date.now() - t < ms) {
    if (await fn()) return true;
    await delay(step);
  }
  console.log(`timed out after ${ms}ms waiting for ${what}`);
  return false;
}

async function api(p, opts) {
  const res = await fetch(`${CORRAL_URL}${p}`, opts);
  let body = null;
  try { body = await res.json(); } catch { /* non-JSON */ }
  return { status: res.status, body };
}

const uid = () => Date.now().toString(36) + Math.floor(Math.random() * 36).toString(36);

// ── cleanup tracking ────────────────────────────────────────────────
// Every VM/DV a test creates is registered here and deleted (and verified
// gone) in afterEach, pass or fail. Names must carry the e2e- prefix; the
// guard refuses to delete anything else.

let createdVMs = [];
let createdDVs = [];
const trackVM = (n) => { createdVMs.push(n); return n; };
const trackDV = (n) => { createdDVs.push(n); return n; };

test.beforeEach(() => { createdVMs = []; createdDVs = []; });

test.afterEach(async () => {
  test.setTimeout(180_000);
  for (const vm of createdVMs) {
    if (!vm.startsWith('e2e-')) continue; // safety: only test resources
    // A bootc build interrupted mid-flight leaves its builder job running.
    kubectl(`kubectl delete job ${vm}-bootc-builder -n ${NS} --ignore-not-found --wait=false`);
    await api(`/api/vms/${NS}/${vm}`, { method: 'DELETE' }).catch(() => {});
    await waitFor(() => !vmExists(vm), 90_000, 3000, `vm ${vm} gone`);
    // belt and braces — DeleteVM already removes the VM's disks/snapshots
    kubectl(`kubectl delete vm ${vm} -n ${NS} --ignore-not-found --wait=false`);
  }
  for (const dv of createdDVs) {
    if (!dv.startsWith('e2e-')) continue;
    await api(`/api/datavolumes/${NS}/${dv}`, { method: 'DELETE' }).catch(() => {});
    kubectl(`kubectl delete dv ${dv} -n ${NS} --ignore-not-found --wait=false`);
    await waitFor(() => !dvExists(dv), 60_000, 3000, `dv ${dv} gone`);
  }
  for (const vm of createdVMs) {
    if (vm.startsWith('e2e-')) expect(vmExists(vm), `cleanup: vm ${vm} still exists`).toBe(false);
  }
});

// ── UI helpers ──────────────────────────────────────────────────────

async function openCreateDialog(page) {
  await page.click('#btn-create');
  await page.waitForSelector('#create-dialog[open]', { timeout: 10_000 });
  // The dialog opens in simple-wizard mode; tests drive the full form.
  await page.click('#wiz-advanced');
  await page.waitForSelector('#create-form:not([hidden])', { timeout: 5_000 });
}

async function createVM(page, opts) {
  await openCreateDialog(page);
  await page.fill('#create-form [name="name"]', opts.name);
  if (opts.sourceType) await page.selectOption('#create-form [name="sourceType"]', opts.sourceType);
  if (opts.source) await page.fill('#create-form [name="source"]', opts.source);
  if (opts.catalogImage) { await delay(500); await page.selectOption('#create-form [name="catalogImage"]', opts.catalogImage); }
  if (opts.cpu) await page.fill('#create-form [name="cpu"]', String(opts.cpu));
  if (opts.mem) await page.fill('#create-form [name="mem"]', opts.mem);
  if (opts.disk) await page.fill('#create-form [name="disk"]', opts.disk);
  if (opts.sshKey) await page.fill('#create-form [name="sshKey"]', opts.sshKey);
  if (opts.cloudInit) {
    // The cloud-init textarea lives in a collapsed <details> — expand it first.
    await page.click('#create-form details summary');
    await page.fill('#create-form [name="cloudInit"]', opts.cloudInit);
  }
  await page.click('#create-form button[type="submit"]');
  await page.waitForFunction(() => !document.querySelector('#create-dialog[open]'), null, { timeout: 20_000 });
}

// Open a VM's detail panel by clicking its row in the tree. The tree refreshes
// every 5s, so reload and retry until the row shows up.
async function openVM(page, name, ms = 60_000) {
  const t = Date.now();
  while (Date.now() - t < ms) {
    await page.goto(CORRAL_URL);
    await page.waitForSelector('#tree .tree-item', { timeout: 15_000 });
    const row = page.locator('#tree .tree-item').filter({ hasText: name }).first();
    if (await row.isVisible({ timeout: 4000 }).catch(() => false)) {
      await row.click();
      await page.waitForSelector('#content .toolbar [data-act="start"]', { timeout: 10_000 });
      return;
    }
    await delay(2000);
  }
  throw new Error(`VM ${name} never appeared in the tree`);
}

async function clickTab(page, tabId) {
  await page.click(`#content .tab[data-tab="${tabId}"]`);
  await delay(800);
}

// ── 0. Smoke ────────────────────────────────────────────────────────

test.describe('Corral web UI', () => {

  test.beforeEach(async ({ page }) => {
    await page.goto(CORRAL_URL);
    await page.waitForSelector('#tree', { timeout: 15_000 });
    await delay(1500);
  });

  test('page loads', async ({ page }) => {
    await expect(page.locator('.brand')).toContainText('Corral');
    await expect(page.locator('#btn-create')).toBeVisible();
  });

  // ── 1. Create wizard: every source type toggles its fields ────────

  test('create wizard fields toggle for every source type', async ({ page }) => {
    await openCreateDialog(page);

    // The simple path is the default: catalog selected, image list visible.
    expect(await page.locator('#create-form [name="sourceType"]').inputValue()).toBe('catalog');
    await expect(page.locator('#catalog-field')).toBeVisible();
    expect(await page.locator('#source-field').getAttribute('hidden')).toBe('');
    await expect(page.locator('#sshkey-field')).toBeVisible(); // SSH access is universal

    for (const type of ['containerDisk', 'import', 'iso', 'pvc']) {
      await page.selectOption('#create-form [name="sourceType"]', type);
      await delay(300);
      await expect(page.locator('#source-field')).toBeVisible();
      await expect(page.locator('#sshkey-field')).toBeVisible();
    }

    // bootc (when the plugin is enabled): catalog datalist + bootc hint
    const bootcOpt = await page.locator('#create-form [name="sourceType"] option[value="bootc"]').count();
    if (bootcOpt > 0) {
      await page.selectOption('#create-form [name="sourceType"]', 'bootc');
      await delay(300);
      expect(await page.locator('#create-form [name="source"]').getAttribute('list')).toBe('bootc-catalog');
      await expect(page.locator('#create-hint')).toContainText('Bootc builds');
    }
    // The hint never mentions bootc for non-bootc sources.
    await page.selectOption('#create-form [name="sourceType"]', 'iso');
    await delay(300);
    await expect(page.locator('#create-hint')).not.toContainText('ootc');

    // Instance type / preference / namespace are tucked behind Advanced.
    expect(await page.locator('#create-form [name="instancetype"]').isVisible()).toBe(false);
    await page.click('#create-form details summary');
    await expect(page.locator('#create-form [name="instancetype"]')).toBeVisible();

    await page.click('#btn-cancel');
    await page.waitForFunction(() => !document.querySelector('#create-dialog[open]'));
  });

  // ── 1b. Simple wizard: cards → questions → VM ──────────────────────

  test('simple wizard: pick a distro card, size it, create', async ({ page }) => {
    test.setTimeout(180_000);
    const vm = trackVM('e2e-wiz-' + uid());

    await page.click('#btn-create');
    await page.waitForSelector('#create-dialog[open]');
    // Cards render with the catalog (and bootc chip if the plugin is on).
    await expect(page.locator('#wiz-cards .wiz-card').first()).toBeVisible({ timeout: 10_000 });

    // Filter to servers, pick fedora.
    await page.click('#wiz-filters [data-filter="server"]');
    await page.locator('#wiz-cards .wiz-card').filter({ hasText: 'Fedora 42 cloud' }).first().click();
    await expect(page.locator('#wiz-step-2')).toBeVisible();

    await page.fill('#wiz-name', vm);
    await page.click('#wiz-sizes [data-size="s"]');
    await page.click('#wiz-create');
    await page.waitForFunction(() => !document.querySelector('#create-dialog[open]'), null, { timeout: 20_000 });

    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);
    // Small preset → 1 vCPU.
    const cpu = kubectl(`kubectl get vm ${vm} -n ${NS} -o jsonpath='{.spec.template.spec.domain.cpu.sockets}'`).replace(/'/g, '');
    expect(parseInt(cpu, 10)).toBe(1);
  });

  // ── 2. Catalog content: reputable sources present ──────────────────

  test('image catalog lists official distro sources and TurnKey', async ({ page }) => {
    const { status, body: imgs } = await api('/api/images');
    expect(status).toBe(200);
    const names = imgs.map((i) => i.name);
    for (const want of ['debian-12-official', 'fedora-42-official',
      'centos-stream9-official', 'almalinux-9-official', 'turnkey-core']) {
      expect(names, `catalog should include ${want}`).toContain(want);
    }
    // Official entries carry their publisher and a URL/ISO source.
    const deb = imgs.find((i) => i.name === 'debian-12-official');
    expect(deb.source).toBe('debian.org');
    expect(deb.url).toContain('cloud.debian.org');

    // The wizard's catalog select shows them too.
    await openCreateDialog(page);
    await page.selectOption('#create-form [name="sourceType"]', 'catalog');
    await delay(800);
    const options = await page.locator('#create-form [name="catalogImage"] option').allTextContents();
    expect(options.join('\n')).toContain('debian-12-official');
    expect(options.join('\n')).toContain('turnkey-core');
    await page.click('#btn-cancel');

    // Bootc catalog rides the same endpoint behind ?type=bootc.
    const { body: caps } = await api('/api/capabilities');
    if (caps.bootc) {
      const { status: bs, body: bimgs } = await api('/api/images?type=bootc');
      expect(bs).toBe(200);
      const brefs = bimgs.map((b) => b.image).join('\n');
      expect(brefs).toContain('quay.io/fedora/fedora-bootc');
      expect(brefs).toContain('quay.io/centos-bootc/centos-bootc');
    }
  });

  // ── 3. Container disk: full lifecycle driven through the UI ───────

  test('containerDisk VM: create, start, stop, delete via UI buttons', async ({ page }) => {
    test.setTimeout(420_000);
    const vm = trackVM('e2e-ctr-' + uid());

    await createVM(page, { name: vm, sourceType: 'containerDisk',
      source: CTR_IMAGE, cpu: 1, mem: '2G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);

    await openVM(page, vm);
    await page.click('#content .toolbar [data-act="start"]');
    expect(await waitFor(() => vmStatus(vm) === 'Running', 240_000, 4000, `${vm} Running`)).toBe(true);

    // Independent cluster-side verification: VMI phase, launcher pod, qemu logs.
    assertVMHealthy(expect, vm);

    await openVM(page, vm); // re-render so Stop is enabled
    await page.click('#content .toolbar [data-act="stop"]');
    expect(await waitFor(() => vmStatus(vm) === 'Stopped', 120_000, 4000, `${vm} Stopped`)).toBe(true);
    expect(await waitFor(() => vmiPhase(vm) === '', 60_000, 3000, `VMI ${vm} gone after stop`)).toBe(true);

    await openVM(page, vm);
    page.once('dialog', (d) => d.accept());
    await page.click('#content .toolbar [data-act="delete"]');
    expect(await waitFor(() => !vmExists(vm), 60_000, 3000, `${vm} deleted`)).toBe(true);
  });

  // ── 4. Catalog create (containerdisk entry) ────────────────────────

  test('catalog containerdisk image create', async ({ page }) => {
    test.setTimeout(180_000);
    const vm = trackVM('e2e-cat-' + uid());

    await createVM(page, { name: vm, sourceType: 'catalog', catalogImage: 'ubuntu', cpu: 2, mem: '4G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);

    // The Proxmox-style task panel records the create.
    const { body: tasks } = await api('/api/tasklog');
    expect(tasks.some((t) => t.action === 'create' && t.target === `${NS}/${vm}`),
      'task log records the create').toBe(true);
    await page.click('#task-panel-head');
    await expect(page.locator('#task-rows')).toContainText(vm, { timeout: 10_000 });
    await page.click('#task-panel-head'); // collapse again
    // afterEach deletes it
  });

  // ── 5. Catalog create (official-source qcow2 entry → CDI import) ──

  test('catalog official-source image creates a CDI-imported VM', async ({ page }) => {
    test.setTimeout(180_000);
    const vm = trackVM('e2e-off-' + uid());

    await createVM(page, { name: vm, sourceType: 'catalog',
      catalogImage: 'debian-12-official', cpu: 1, mem: '2G', disk: '15G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);

    // The official entry must come in over CDI: the VM gets a DataVolume disk
    // importing from cloud.debian.org.
    expect(await waitFor(() => dvExists(`${vm}-disk`), 30_000, 2000, `dv ${vm}-disk`)).toBe(true);
    const src = kubectl(`kubectl get dv ${vm}-disk -n ${NS} -o jsonpath='{.spec.source.http.url}'`);
    expect(src).toContain('cloud.debian.org');
  });

  // ── 6. Import qcow2 by URL (CDI completes end to end) ─────────────

  test('import qcow2 URL: CDI import succeeds and VM boots @live-only', async ({ page }) => {
    test.setTimeout(600_000);
    const vm = trackVM('e2e-imp-' + uid());

    await createVM(page, { name: vm, sourceType: 'import',
      source: CIRROS_QCOW, cpu: 1, mem: '1G', disk: '5G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);

    // cirros is ~21 MB — the import should finish quickly.
    expect(await waitFor(() => dvPhase(`${vm}-disk`) === 'Succeeded', 300_000, 5000,
      `dv ${vm}-disk Succeeded (phase: ${dvPhase(`${vm}-disk`)})`)).toBe(true);

    await api(`/api/vms/${NS}/${vm}/start`, { method: 'POST' });
    expect(await waitFor(() => vmStatus(vm) === 'Running', 180_000, 4000, `${vm} Running`)).toBe(true);
    assertVMHealthy(expect, vm);
    await api(`/api/vms/${NS}/${vm}/stop`, { method: 'POST' });
    await waitFor(() => vmStatus(vm) === 'Stopped', 120_000, 4000, `${vm} Stopped`);
  });

  // ── 7. Installer ISO ───────────────────────────────────────────────

  test('ISO source creates VM with installer media', async ({ page }) => {
    test.setTimeout(180_000);
    const vm = trackVM('e2e-iso-' + uid());

    await createVM(page, { name: vm, sourceType: 'iso',
      source: ALPINE_ISO, cpu: 1, mem: '2G', disk: '10G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);
    expect(await waitFor(() => dvExists(`${vm}-iso`), 30_000, 2000, `dv ${vm}-iso`)).toBe(true);
  });

  // ── 8. Existing PVC ────────────────────────────────────────────────

  test('PVC source: import an image to the library, boot a VM from it', async ({ page }) => {
    test.setTimeout(600_000);
    const dv = trackDV('e2e-lib-' + uid());
    const vm = trackVM('e2e-pvc-' + uid());

    // Import into the image library (same API the Images panel uses).
    const { status } = await api('/api/datavolumes', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: dv, namespace: NS, url: CIRROS_QCOW, size: '5G' }),
    });
    expect(status).toBe(200);
    expect(await waitFor(() => dvPhase(dv) === 'Succeeded', 300_000, 5000, `dv ${dv} Succeeded`)).toBe(true);

    await createVM(page, { name: vm, sourceType: 'pvc', source: dv, cpu: 1, mem: '1G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);
    const claim = kubectl(`kubectl get vm ${vm} -n ${NS} -o jsonpath='{.spec.template.spec.volumes[*].persistentVolumeClaim.claimName}'`);
    expect(claim).toContain(dv);
  });

  // ── 9. Settings: scale CPU/memory from the Hardware tab ───────────

  test('Hardware tab: scale CPU and memory', async ({ page }) => {
    test.setTimeout(300_000);
    const vm = trackVM('e2e-scl-' + uid());

    await createVM(page, { name: vm, sourceType: 'containerDisk',
      source: 'quay.io/containerdisks/fedora:42', cpu: 1, mem: '2G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);

    await openVM(page, vm);
    await clickTab(page, 'hardware');
    await page.waitForSelector('#hw-cpu', { timeout: 10_000 });
    await page.fill('#hw-cpu', '2');
    await page.fill('#hw-mem', '3G');
    await page.click('#hw-apply');

    // Scale writes sockets-based CPU and normalizes memory to Mi ("3G" → "3072Mi").
    const toMib = (q) => {
      const m = /^([\d.]+)(Mi|Gi|M|G)?$/.exec(q || '');
      return m ? parseFloat(m[1]) * (m[2]?.startsWith('G') ? 1024 : 1) : 0;
    };
    const scaled = await waitFor(() => {
      const out = kubectl(`kubectl get vm ${vm} -n ${NS} -o jsonpath='{.spec.template.spec.domain.cpu.sockets} {.spec.template.spec.domain.cpu.cores} {.spec.template.spec.domain.memory.guest}'`).replace(/'/g, '');
      const [sockets, cores, mem] = out.split(' ');
      const vcpus = (parseInt(sockets, 10) || 1) * (parseInt(cores, 10) || 1);
      return vcpus === 2 && toMib(mem) === 3072;
    }, 60_000, 3000, `${vm} scaled to 2 vCPU / 3072Mi`);
    expect(scaled).toBe(true);
  });

  // ── 10. Settings: snapshots tab ────────────────────────────────────

  test('Snapshots tab: take and delete a snapshot @live-only', async ({ page }) => {
    test.setTimeout(420_000);
    const { body: caps } = await api('/api/capabilities');
    test.skip(!caps.canSnapshot, 'cluster has no VolumeSnapshotClass');

    const vm = trackVM('e2e-snp-' + uid());
    // Snapshots need a PVC-backed disk — use the catalog ubuntu (containerdisk
    // has no PVC), so import cirros instead.
    await createVM(page, { name: vm, sourceType: 'import',
      source: CIRROS_QCOW, cpu: 1, mem: '1G', disk: '5G' });
    expect(await waitFor(() => dvPhase(`${vm}-disk`) === 'Succeeded', 300_000, 5000, `dv import`)).toBe(true);

    await openVM(page, vm);
    await clickTab(page, 'snapshots');
    await page.waitForSelector('#snap-new', { timeout: 10_000 });
    await page.click('#snap-new');

    // A snapshot row appears and becomes ready.
    const snapName = await (async () => {
      const ok = await waitFor(() =>
        kubectl(`kubectl get vmsnapshot -n ${NS} -o jsonpath='{range .items[?(@.spec.source.name=="${vm}")]}{.metadata.name}{end}'`) !== '',
        60_000, 3000, 'snapshot CR');
      if (!ok) return '';
      return kubectl(`kubectl get vmsnapshot -n ${NS} -o jsonpath='{range .items[?(@.spec.source.name=="${vm}")]}{.metadata.name}{end}'`).replace(/'/g, '');
    })();
    expect(snapName, 'snapshot CR created').not.toBe('');

    await waitFor(() =>
      kubectl(`kubectl get vmsnapshot ${snapName} -n ${NS} -o jsonpath='{.status.readyToUse}'`).includes('true'),
      180_000, 5000, 'snapshot ready');

    // Delete it from the UI.
    await clickTab(page, 'snapshots');
    await page.waitForSelector(`[data-delsnap="${snapName}"]`, { timeout: 15_000 });
    await page.click(`[data-delsnap="${snapName}"]`);
    expect(await waitFor(() =>
      kubectl(`kubectl get vmsnapshot ${snapName} -n ${NS} -o name --ignore-not-found`) === '',
      60_000, 3000, 'snapshot deleted')).toBe(true);
  });

  // ── 11. Consoles: VNC (RFB) and serial terminal over websockets ────

  test('VNC and serial TTY consoles connect to a running VM @live-only', async ({ page }) => {
    test.setTimeout(600_000);
    const vm = trackVM('e2e-con-' + uid());

    await createVM(page, { name: vm, sourceType: 'containerDisk',
      source: 'quay.io/containerdisks/fedora:42', cpu: 1, mem: '2G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);
    await api(`/api/vms/${NS}/${vm}/start`, { method: 'POST' });
    expect(await waitFor(() => vmStatus(vm) === 'Running', 240_000, 4000, `${vm} Running`)).toBe(true);
    assertVMHealthy(expect, vm);
    await delay(10_000); // let qemu bring up the VNC/serial sockets

    // Raw protocol checks straight at the websocket bridges, from the browser.
    const rfb = await page.evaluate(({ ns, name }) => new Promise((resolve) => {
      const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/vnc/${ns}/${name}`);
      ws.binaryType = 'arraybuffer';
      const timer = setTimeout(() => { ws.close(); resolve('timeout'); }, 30_000);
      ws.onmessage = (e) => {
        clearTimeout(timer);
        const head = new TextDecoder().decode(new Uint8Array(e.data).slice(0, 3));
        ws.close();
        resolve(head);
      };
      ws.onerror = () => { clearTimeout(timer); resolve('error'); };
    }), { ns: NS, name: vm });
    expect(rfb, 'VNC bridge should speak RFB').toBe('RFB');

    const tty = await page.evaluate(({ ns, name }) => new Promise((resolve) => {
      const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/tty/${ns}/${name}`);
      ws.binaryType = 'arraybuffer';
      const timer = setTimeout(() => { ws.close(); resolve('timeout'); }, 30_000);
      ws.onopen = () => ws.send(new TextEncoder().encode('\n'));
      ws.onmessage = (e) => {
        clearTimeout(timer);
        ws.close();
        resolve(e.data.byteLength > 0 ? 'data' : 'empty');
      };
      ws.onerror = () => { clearTimeout(timer); resolve('error'); };
    }), { ns: NS, name: vm });
    expect(tty, 'serial bridge should return console output').toBe('data');

    // And the actual UI tabs: noVNC canvas + xterm screen render.
    await openVM(page, vm);
    await clickTab(page, 'console');
    await expect(page.locator('#vnc-screen canvas')).toBeVisible({ timeout: 30_000 });
    await expect(page.locator('#tab-body')).not.toContainText('VNC failed');

    // Console controls: scaling toggles present, fullscreen actually engages.
    await expect(page.locator('#vnc-scale')).toBeChecked();
    await expect(page.locator('#vnc-resize')).not.toBeChecked();
    await page.click('#vnc-fullscreen');
    await delay(800);
    expect(await page.evaluate(() => document.fullscreenElement?.id || '')).toBe('vnc-screen');
    // Headless Chromium ignores the Escape shortcut — exit via the API and
    // confirm the canvas no longer covers the page.
    await page.evaluate(() => document.exitFullscreen());
    await delay(500);
    expect(await page.evaluate(() => document.fullscreenElement?.id || '')).toBe('');

    await clickTab(page, 'terminal');
    await expect(page.locator('#tty-screen .xterm')).toBeVisible({ timeout: 30_000 });
    await page.click('#tty-fullscreen');
    await delay(800);
    expect(await page.evaluate(() => document.fullscreenElement?.id || '')).toBe('tty-screen');
    await page.evaluate(() => document.exitFullscreen());
    await delay(500);

    // Multiview: the running VM appears as a live view-only tile.
    await page.locator('#tree .tree-item').filter({ hasText: 'Multiview' }).first().click();
    const tile = page.locator('.mv-tile').filter({ hasText: vm }).first();
    await expect(tile).toBeVisible({ timeout: 15_000 });
    await expect(tile.locator('canvas')).toBeVisible({ timeout: 30_000 });

    await api(`/api/vms/${NS}/${vm}/stop`, { method: 'POST' });
    await waitFor(() => vmStatus(vm) === 'Stopped', 120_000, 4000, `${vm} Stopped`);
  });

  // ── 11b. RDP detection ─────────────────────────────────────────────
  // Linux cloud images don't run an RDP server, so the probe must say
  // closed — and the Summary panel must render the RDP row accordingly.
  // (A true-positive needs a Windows VM or a Linux guest with
  // gnome-remote-desktop/xrdp — out of e2e budget; covered by unit tests.)

  test('RDP probe: closed for a Linux VM, Summary row renders @live-only', async ({ page }) => {
    test.setTimeout(420_000);
    const vm = trackVM('e2e-rdp-' + uid());

    await createVM(page, { name: vm, sourceType: 'containerDisk',
      source: 'quay.io/containerdisks/fedora:42', cpu: 1, mem: '2G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);
    await api(`/api/vms/${NS}/${vm}/start`, { method: 'POST' });
    expect(await waitFor(() => vmStatus(vm) === 'Running', 240_000, 4000, `${vm} Running`)).toBe(true);

    const { status, body } = await api(`/api/vms/${NS}/${vm}/rdp`);
    expect(status).toBe(200);
    expect(body.open).toBe(false);

    await openVM(page, vm);
    await expect(page.locator('#vm-rdp')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('#vm-rdp')).toContainText('—', { timeout: 15_000 });

    await api(`/api/vms/${NS}/${vm}/stop`, { method: 'POST' });
    await waitFor(() => vmStatus(vm) === 'Stopped', 120_000, 4000, `${vm} Stopped`);
  });

  // ── 11c. Multus secondary NIC ──────────────────────────────────────
  // Runs when the cluster has a NetworkAttachmentDefinition (Multus
  // installed — see deploy/multus/); skips cleanly otherwise.

  test('Add NIC: attach a Multus network and the guest gets the interface @live-only', async ({ page }) => {
    test.setTimeout(420_000);
    const { body: nads } = await api('/api/nads');
    test.skip(!nads || !nads.length, 'no NetworkAttachmentDefinitions (Multus not installed)');
    const nad = nads[0].split('/').pop();

    const vm = trackVM('e2e-nic-' + uid());
    await createVM(page, { name: vm, sourceType: 'containerDisk',
      source: CTR_IMAGE, cpu: 1, mem: '1G' });
    expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);

    const { status } = await api(`/api/vms/${NS}/${vm}/nics`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ nad }),
    });
    expect(status).toBe(200);
    const nets = kubectl(`kubectl get vm ${vm} -n ${NS} -o jsonpath='{.spec.template.spec.networks[*].multus.networkName}'`);
    expect(nets).toContain(nad);

    // The guest actually receives the second interface.
    await api(`/api/vms/${NS}/${vm}/start`, { method: 'POST' });
    expect(await waitFor(() => vmStatus(vm) === 'Running', 240_000, 5000, `${vm} Running`)).toBe(true);
    const ifaces = await waitFor(() =>
      kubectl(`kubectl get vmi ${vm} -n ${NS} -o jsonpath='{.status.interfaces[*].name}'`).includes('net1'),
      120_000, 5000, 'net1 interface on the VMI');
    expect(ifaces, 'VMI reports the multus interface').toBe(true);

    await api(`/api/vms/${NS}/${vm}/stop`, { method: 'POST' });
    await waitFor(() => vmStatus(vm) === 'Stopped', 120_000, 4000, `${vm} Stopped`);
  });

  // ── 12. SSH access ─────────────────────────────────────────────────

  test('SSH: key injected at create, login works @live-only', async ({ page }) => {
    test.setTimeout(600_000);
    const vm = trackVM('e2e-ssh-' + uid());

    // Throwaway keypair; public half goes in via the wizard's cloud-init box.
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corral-e2e-'));
    const key = path.join(dir, 'id_ed25519');
    execSync(`ssh-keygen -t ed25519 -N "" -f ${key} -q`);
    const pub = fs.readFileSync(`${key}.pub`, 'utf8').trim();

    try {
      // The wizard's universal SSH field (a literal key here; gh:<user> works too).
      await createVM(page, { name: vm, sourceType: 'containerDisk',
        source: 'quay.io/containerdisks/fedora:42', cpu: 1, mem: '2G',
        sshKey: pub });
      expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);

      await api(`/api/vms/${NS}/${vm}/start`, { method: 'POST' });
      expect(await waitFor(() => vmStatus(vm) === 'Running', 240_000, 4000, `${vm} Running`)).toBe(true);
      assertVMHealthy(expect, vm);

      // Cross-check the key actually landed in the cloud-init user-data.
      const userData = kubectl(`kubectl get vm ${vm} -n ${NS} -o jsonpath='{.spec.template.spec.volumes[?(@.name=="cloudinitdisk")].cloudInitNoCloud.userData}'`);
      expect(userData, 'cloud-init carries the injected key').toContain(pub.split(' ')[1].slice(0, 20));

      // Log in through virtctl port-forward. virtctl's local listener dies
      // after its first failed tunnel dial (e.g. sshd not up yet), so use a
      // FRESH port-forward for every attempt instead of one long-lived one.
      let lastErr = '';
      const tryLogin = () => new Promise((resolve) => {
        const port = 20000 + Math.floor(Math.random() * 10000);
        const fwd = spawn('virtctl', ['port-forward', `vm/${vm}`, `${port}:22`, '-n', NS],
          { stdio: ['ignore', 'pipe', 'pipe'], detached: false });
        let fwdLog = '';
        fwd.stdout.on('data', (d) => { fwdLog += d; });
        fwd.stderr.on('data', (d) => { fwdLog += d; });
        setTimeout(() => {
          try {
            const out = execSync(
              `ssh -p ${port} -i ${key} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ` +
              `-o ConnectTimeout=8 -o BatchMode=yes fedora@127.0.0.1 echo SSH_OK 2>&1`,
              { timeout: 25000, encoding: 'utf8' });
            if (out.includes('SSH_OK')) { fwd.kill('SIGKILL'); return resolve(true); }
            lastErr = `${out.trim().split('\n').pop()} | pf: ${fwdLog.slice(-200)}`;
          } catch (e) {
            lastErr = `${String(e.stderr || e.stdout || e.message).trim().split('\n').pop()} | pf: ${fwdLog.slice(-200)}`;
          }
          fwd.kill('SIGKILL');
          resolve(false);
        }, 4000); // let the forward bind before dialing
      });
      const ok = await waitFor(tryLogin, 300_000, 10_000, 'ssh login');
      if (!ok) console.log(`ssh diagnostics — last error: ${lastErr}`);
      expect(ok, `ssh login with the injected key (last: ${lastErr})`).toBe(true);

      await api(`/api/vms/${NS}/${vm}/stop`, { method: 'POST' });
      await waitFor(() => vmStatus(vm) === 'Stopped', 120_000, 4000, `${vm} Stopped`);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });

  // ── 12b. Tailscale connectivity ─────────────────────────────────────

  test('Tailscale: VM joins tailnet and is reachable via SSH @live-only', async ({ page }) => {
    test.setTimeout(600_000);
    const vm = trackVM('e2e-ts-' + uid());

    // Throwaway keypair for SSH auth.
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corral-e2e-'));
    const key = path.join(dir, 'id_ed25519');
    execSync(`ssh-keygen -t ed25519 -N "" -f ${key} -q`);
    const pub = fs.readFileSync(`${key}.pub`, 'utf8').trim();

    try {
      // The server's TS_AUTHKEY env var bakes Tailscale into cloud-init.
      // Create via API so the server-side key resolution kicks in.
      await createVM(page, { name: vm, sourceType: 'containerDisk',
        source: CTR_IMAGE, cpu: 1, mem: '2G', sshKey: pub });
      expect(await waitFor(() => vmExists(vm), 30_000, 2000, `vm ${vm}`)).toBe(true);

      await api(`/api/vms/${NS}/${vm}/start`, { method: 'POST' });
      expect(await waitFor(() => vmStatus(vm) === 'Running', 300_000, 4000, `${vm} Running`)).toBe(true);
      assertVMHealthy(expect, vm);

      // Tailscale hostname is the VM name (set by cloud-init --hostname).
      const tsHost = `${vm}.manatee-basking.ts.net`;

      // Wait for the VM to appear on the tailnet (may take a couple of
      // minutes — cloud-init installs Tailscale, then it registers).
      // `tailscale status` lists hostnames without the MagicDNS suffix.
      const tsOnline = () => {
        try {
          const out = execSync(`tailscale status`, { timeout: 5000, encoding: 'utf8' });
          // Match the hostname as a whole word at the start of a line.
          return new RegExp(`^\\S+\\s+${vm}\\s`, 'm').test(out);
        } catch { return false; }
      };
      const onTailnet = await waitFor(tsOnline, 300_000, 10_000, `tailscale ${tsHost}`);
      if (!onTailnet) {
        // Diagnostics: dump tailscale status to understand why it didn't appear.
        try { console.log('tailscale status:', execSync('tailscale status', { timeout: 5000, encoding: 'utf8' })); }
        catch (e) { console.log('tailscale status failed:', e.message); }
      }
      expect(onTailnet, `${tsHost} appears on the tailnet`).toBe(true);

      // Resolve the VM's Tailscale IP from the hostname (not FQDN).
      let vmIP = '';
      try {
        const status = execSync('tailscale status', { timeout: 5000, encoding: 'utf8' });
        const m = status.match(new RegExp(`^(\\S+)\\s+${vm}\\s`, 'm'));
        if (m) vmIP = m[1];
      } catch {}
      expect(vmIP, `tailscale IP for ${vm}`).toBeTruthy();

      // SSH directly over Tailscale — no port-forward, no proxy, using the
      // Tailscale IP (avoids MagicDNS resolver issues on the runner).
      let lastErr = '';
      const trySSH = () => {
        try {
          const out = execSync(
            `ssh -i ${key} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ` +
            `-o ConnectTimeout=10 -o BatchMode=yes fedora@${vmIP} echo SSH_OK 2>&1`,
            { timeout: 15000, encoding: 'utf8' });
          if (out.includes('SSH_OK')) return true;
          lastErr = out.trim().split('\n').pop();
        } catch (e) {
          lastErr = String(e.stderr || e.stdout || e.message).trim().split('\n').pop();
        }
        return false;
      };
      const sshOk = await waitFor(trySSH, 120_000, 10_000, `ssh fedora@${vmIP}`);
      if (!sshOk) console.log(`tailscale ssh diagnostics — last error: ${lastErr}`);
      expect(sshOk, `ssh login over Tailscale to ${tsHost} (${vmIP}) (last: ${lastErr})`).toBe(true);

      await api(`/api/vms/${NS}/${vm}/stop`, { method: 'POST' });
      await waitFor(() => vmStatus(vm) === 'Stopped', 120_000, 4000, `${vm} Stopped`);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }
  });

  // ── 13. Bootc: build from the curated bootc catalog ────────────────

  test('bootc VM from catalog, boots and accepts SSH @live-only', async ({ page }) => {
    // Budget reality: the builder pod pull + bootc's own in-pod image pull
    // (~1.3 GB via the registry, not the node cache) + install + first boot
    // + the ~10-14 min status-reporting lag (see below).
    test.setTimeout(2_700_000);

    const { body: caps } = await api('/api/capabilities');
    test.skip(!caps.bootc, 'bootc plugin not enabled on this server');

    // Throwaway keypair — the public half is baked into the disk during
    // the bootc build so SSH works after first boot (no cloud-init needed).
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corral-e2e-'));
    const key = path.join(dir, 'id_ed25519');
    execSync(`ssh-keygen -t ed25519 -N "" -f ${key} -q`);
    const pub = fs.readFileSync(`${key}.pub`, 'utf8').trim();

    const vm = trackVM('e2e-btc-' + uid());
    // Bootc builds are async — use the API directly so we get the task ID
    // and can poll for progress (the UI form's sourceType= bootc field is
    // unreliable through Playwright's selectOption).
    const { body: createRes } = await api('/api/vms', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: vm, bootc: 'quay.io/centos-bootc/centos-bootc:stream9',
        cpu: 2, mem: '4G', disk: '20G', sshKey: pub }),
    });
    expect(createRes.task, 'bootc API returned task ID').toBeTruthy();

    // The bootc build runs as a Kubernetes Job.  Wait for it to appear and
    // complete before checking for the VM (kubectl gives us the exit status
    // whether the task API or build dialog work or not).
    const builderJob = `${vm}-bootc-builder`;
    const jobDone = () => {
      const status = kubectl(`kubectl get job ${builderJob} -n ${NS} -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' --ignore-not-found`).replace(/'/g, '');
      if (status === 'True') return 'done';
      const failed = kubectl(`kubectl get job ${builderJob} -n ${NS} -o jsonpath='{.status.conditions[?(@.type=="Failed")].status}' --ignore-not-found`).replace(/'/g, '');
      if (failed === 'True') return 'failed';
      return '';
    };
    let jobResult = '';
    let waited = 0;
    while (!jobResult && waited < 600) {
      jobResult = jobDone();
      if (!jobResult) { await delay(5000); waited += 5; }
    }
    // If the job completed but the VM hasn't materialised yet, wait for it.
    if (jobResult === 'done') {
      const built = await waitFor(() => vmExists(vm), 120_000, 3000, `vm ${vm}`);
      expect(built, `bootc build produced VM ${vm}`).toBe(true);
    } else if (jobResult === 'failed') {
      // Grab the builder pod logs for diagnostics.
      const pod = kubectl(`kubectl get pod -n ${NS} -l job-name=${builderJob} -o jsonpath='{.items[0].metadata.name}' --ignore-not-found`).replace(/'/g, '');
      const log = pod ? kubectl(`kubectl logs ${pod} -n ${NS} --tail=50`) : '(no pod)';
      console.log(`bootc builder failed — log tail:\n${log}`);
      throw new Error(`bootc build job ${builderJob} failed`);
    } else {
      throw new Error(`bootc builder job ${builderJob} did not complete within 10 minutes`);
    }

    // The manifest must carry the bootc signature: kernel boot + the built disk PVC.
    const manifest = kubectl(`kubectl get vm ${vm} -n ${NS} -o json`);
    expect(manifest, 'VM boots from the built bootc disk').toContain(`${vm}-bootc-disk`);
    expect(manifest, 'VM uses kernel boot').toContain('kernelBoot');

    // The real proof the user cares about: the built disk actually boots.
    // NOTE: stock KubeVirt ≤1.8.x + k8s 1.36 flaps the reported status forever
    // (kernelBootStatus.kernelInfo.checksum is a uint32 the CRD schema validates
    // as int32, so virt-handler's status updates get rejected). Fixed upstream
    // in v1.9.0-beta.0 (Format:=int64, kubevirt commit e78c814e9); this cluster
    // runs ≥v1.9.0-beta.0.
    await api(`/api/vms/${NS}/${vm}/start`, { method: 'POST' });
    expect(await waitFor(() => vmStatus(vm) === 'Running', 600_000, 5_000, `${vm} Running`)).toBe(true);
    assertVMHealthy(expect, vm);

    // And the guest talks: serial console produces output (kernel/systemd).
    const ttyCheck = () => page.evaluate(({ ns, name }) => new Promise((resolve) => {
      const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/tty/${ns}/${name}`);
      ws.binaryType = 'arraybuffer';
      const timer = setTimeout(() => { ws.close(); resolve(false); }, 20_000);
      ws.onopen = () => ws.send(new TextEncoder().encode('\n'));
      ws.onmessage = (e) => { clearTimeout(timer); ws.close(); resolve(e.data.byteLength > 0); };
      ws.onerror = () => { clearTimeout(timer); resolve(false); };
    }), { ns: NS, name: vm });
    expect(await waitFor(ttyCheck, 120_000, 15_000, 'bootc guest serial output'),
      'bootc guest answers on the serial console').toBe(true);

    // SSH into the bootc VM via virtctl port-forward. Bootc images bake the
    // key into the disk during the build — no cloud-init wait needed.
    try {
      let lastErr = '';
      const tryLogin = () => new Promise((resolve) => {
        const port = 20000 + Math.floor(Math.random() * 10000);
        const fwd = spawn('virtctl', ['port-forward', `vm/${vm}`, `${port}:22`, '-n', NS],
          { stdio: ['ignore', 'pipe', 'pipe'], detached: false });
        let fwdLog = '';
        fwd.stdout.on('data', (d) => { fwdLog += d; });
        fwd.stderr.on('data', (d) => { fwdLog += d; });
        setTimeout(() => {
          try {
            const out = execSync(
              `ssh -p ${port} -i ${key} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ` +
              `-o ConnectTimeout=8 -o BatchMode=yes root@127.0.0.1 echo BOOTC_SSH_OK 2>&1`,
              { timeout: 25000, encoding: 'utf8' });
            if (out.includes('BOOTC_SSH_OK')) { fwd.kill('SIGKILL'); return resolve(true); }
            lastErr = `${out.trim().split('\n').pop()} | pf: ${fwdLog.slice(-200)}`;
          } catch (e) {
            lastErr = `${String(e.stderr || e.stdout || e.message).trim().split('\n').pop()} | pf: ${fwdLog.slice(-200)}`;
          }
          fwd.kill('SIGKILL');
          resolve(false);
        }, 4000);
      });
      const sshOk = await waitFor(tryLogin, 180_000, 10_000, 'bootc SSH login');
      if (!sshOk) console.log(`bootc ssh diagnostics — last error: ${lastErr}`);
      expect(sshOk, `bootc SSH login with the injected key (last: ${lastErr})`).toBe(true);

      // The bootc VM should also be on the tailnet — the TS_AUTHKEY on the
      // server bakes Tailscale into the cloud-init. Wait for it and SSH.
      const tsOnline = () => {
        try {
          const out = execSync('tailscale status', { timeout: 5000, encoding: 'utf8' });
          return new RegExp(`^\\S+\\s+${vm}\\s`, 'm').test(out);
        } catch { return false; }
      };
      const onTailnet = await waitFor(tsOnline, 300_000, 10_000, `tailscale ${vm}`);
      if (!onTailnet) {
        try { console.log('tailscale status:', execSync('tailscale status', { timeout: 5000, encoding: 'utf8' })); }
        catch (e) { console.log('tailscale status failed:', e.message); }
      }
      expect(onTailnet, `${vm} appears on the tailnet`).toBe(true);

      let vmIP = '';
      try {
        const status = execSync('tailscale status', { timeout: 5000, encoding: 'utf8' });
        const m = status.match(new RegExp(`^(\\S+)\\s+${vm}\\s`, 'm'));
        if (m) vmIP = m[1];
      } catch {}
      expect(vmIP, `tailscale IP for ${vm}`).toBeTruthy();

      // SSH over Tailscale (root user — bootc images bake the key for root).
      let tsLastErr = '';
      const tryTsSSH = () => {
        try {
          const out = execSync(
            `ssh -i ${key} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ` +
            `-o ConnectTimeout=10 -o BatchMode=yes root@${vmIP} echo BOOTC_TS_SSH_OK 2>&1`,
            { timeout: 15000, encoding: 'utf8' });
          if (out.includes('BOOTC_TS_SSH_OK')) return true;
          tsLastErr = out.trim().split('\n').pop();
        } catch (e) {
          tsLastErr = String(e.stderr || e.stdout || e.message).trim().split('\n').pop();
        }
        return false;
      };
      const tsSshOk = await waitFor(tryTsSSH, 120_000, 10_000, `ssh root@${vmIP}`);
      if (!tsSshOk) console.log(`bootc tailscale ssh diagnostics — last error: ${tsLastErr}`);
      expect(tsSshOk, `bootc SSH over Tailscale to ${vm} (${vmIP}) (last: ${tsLastErr})`).toBe(true);
    } finally {
      fs.rmSync(dir, { recursive: true, force: true });
    }

    await api(`/api/vms/${NS}/${vm}/stop`, { method: 'POST' });
    await waitFor(() => vmStatus(vm) === 'Stopped', 120_000, 4000, `${vm} Stopped`);
  });

});
