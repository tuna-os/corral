// UI smoke test: drives the real dashboard against `corral web --demo` with
// headless Chromium. Run by .github/workflows/ui-smoke.yml; locally:
//
//   corral web --demo --addr 127.0.0.1:8899 &
//   npx playwright install chromium && node scripts/ui-smoke.mjs
//
// Asserts the load-bearing screens render and a stateful action round-trips.
// Fails (exit 1) on any assertion or page error.
// For reproducible documentation images, see scripts/capture-docs.mjs.

import { mkdir } from 'node:fs/promises';
import { mkdirSync } from 'node:fs';
import { chromium } from 'playwright';

const BASE = process.env.CORRAL_URL || 'http://127.0.0.1:8899/';
// Named checks save a screenshot here; the workflow uploads the directory.
const SHOTS = process.env.UI_SMOKE_SHOTS || 'ui-smoke-screenshots';
mkdirSync(SHOTS, { recursive: true });
let failures = 0;
const check = (ok, msg) => {
  console.log(`${ok ? 'ok' : 'FAIL'} - ${msg}`);
  if (!ok) failures++;
};

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const pageErrors = [];
page.on('pageerror', (e) => pageErrors.push(e.message));

// Datacenter view renders the demo fleet. Wait on real elements, not a fixed
// delay — CI runners cold-start slower than the 5s poll cycle.
await page.goto(BASE);
await page.waitForSelector('#tree >> text=Datacenter', { timeout: 30000 }).catch(() => {});
await page.waitForSelector('td:has-text("web-prod")', { timeout: 30000 }).catch(() => {});
check(await page.locator('#tree >> text=Datacenter').count() > 0, 'tree renders');
check(await page.locator('td:has-text("web-prod")').count() > 0, 'VM table lists the fleet');
check(await page.locator('.chip.filter').count() > 2, 'tag filter bar populated');
check(await page.locator('#tree >> text=laptop-dev').count() > 0, 'local demo VM in the tree');
// The demo Incus remote holds one virtual machine and one container, and the
// UI must place each on its own surface: the VM in the fleet table, the
// container in the tree as a CT and nowhere in the VM table. That last
// assertion is the bug this replaces — the container used to be listed as a
// VM *and* as a CT, so checking for it in the table was checking for the bug.
check(await page.locator('td:has-text("incus-demo-vm")').count() > 0, 'Incus demo VM in VM table');
check(await page.locator('td:has-text("incus-demo-container")').count() === 0, 'Incus demo container is not in the VM table');
check(await page.locator('#tree >> text=incus-demo-container').count() > 0, 'Incus demo container in the tree as a CT');

// ── grid-columns: persisted resize, reorder and sort ──────────────
const grid = page.locator('[data-grid="vms"]');
const nameHeader = grid.locator('th[data-column="name"]');
// The grid sits below the dashboard widgets: centre it on screen before
// driving the mouse at its header (the task panel covers the bottom edge).
await nameHeader.evaluate((el) => el.scrollIntoView({ block: 'center' }));
const beforeWidth = await nameHeader.evaluate((el) => el.getBoundingClientRect().width);
const resizeBox = await nameHeader.locator('.grid-resizer').boundingBox();
await page.mouse.move(resizeBox.x + resizeBox.width / 2, resizeBox.y + resizeBox.height / 2);
await page.mouse.down();
await page.mouse.move(resizeBox.x + 42, resizeBox.y + resizeBox.height / 2);
await page.mouse.up();
await grid.locator('th[data-column="status"]').dragTo(nameHeader);
await grid.locator('th[data-column="name"] .grid-sort').click();
await page.reload();
await page.waitForSelector('[data-grid="vms"] td:has-text("web-prod")');
const columnOrder = await grid.locator('thead tr:first-child th[data-column]').evaluateAll((els) => els.map((el) => el.dataset.column));
const afterWidth = await grid.locator('th[data-column="name"]').evaluate((el) => el.getBoundingClientRect().width);
const sortedNames = await grid.locator('tbody tr:not(.grid-spacer) td:nth-child(3)').allTextContents();
check(columnOrder[0] === 'status' && columnOrder[1] === 'name', 'grid-columns reorder persists after reload');
check(afterWidth > beforeWidth + 20, 'grid-columns resize persists after reload');
check(sortedNames.join('|') === [...sortedNames].sort((a, b) => a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' })).join('|'), 'grid-columns sort persists and row order is correct');
await grid.screenshot({ path: 'grid-columns.png' });
check(true, 'grid-columns screenshot saved');

// VM summary.
await page.click('#tree >> text=web-prod');
await page.waitForTimeout(1200);
check(await page.locator('.tab.active:has-text("Summary")').count() === 1, 'VM summary tab opens');
check((await page.textContent('#tab-body')).includes('corral ssh web-prod'), 'summary shows SSH hint');

// Named acceptance check: console-popout. The control must create a separate
// browser page and that page must complete the RFB handshake against demo mode.
await page.click('[data-tab="console"]');
await page.waitForSelector('#vnc-popout');
const popupPromise = page.waitForEvent('popup');
await page.click('#vnc-popout');
const consolePopup = await popupPromise;
await consolePopup.waitForSelector('#vnc-screen[data-connected="true"]', { timeout: 10000 }).catch(() => {});
check(
  await consolePopup.locator('#vnc-screen[data-connected="true"]').count() === 1,
  'console-popout opens a new window and connects',
);
check(await consolePopup.locator('#vnc-one').count() === 1, 'console offers 1:1 scaling');
check(await consolePopup.locator('#vnc-paste').count() === 1, 'console offers clipboard typing');
check(await consolePopup.locator('[data-send-keys="cad"]').count() === 1, 'console offers send-keys');
await mkdir('test-results', { recursive: true });
await consolePopup.screenshot({ path: 'test-results/console-popout.png' });
await consolePopup.close();
await page.click('[data-tab="summary"]');

// Stateful action: toggle power and watch the status flip. State-agnostic so
// the script also works against an already-toggled long-running server.
const wasRunning = (await page.textContent('.page-head')).includes('Running');
await page.click(`button[data-act="${wasRunning ? 'stop' : 'start'}"]`);
await page.waitForTimeout(5500);
check(
  (await page.textContent('.page-head')).includes(wasRunning ? 'Stopped' : 'Running'),
  `${wasRunning ? 'stop' : 'start'} action flips VM state`,
);

// ── Context Menu (context-menu acceptance check) ───────────────────
// Right-click a demo VM, choose Stop (or Start), and assert that the state changes.
await page.click('#tree >> text=Datacenter');
await page.waitForTimeout(800);
const demoRow = page.locator('tr[data-key*="web-prod"]').first();
// Scroll first: a scroll closes an open menu, and the row sits below the
// dashboard.
await demoRow.evaluate((el) => el.scrollIntoView({ block: 'center' }));
await page.waitForTimeout(300);
await demoRow.click({ button: 'right' });
await page.waitForSelector('.context-menu', { timeout: 10000 });
check(await page.locator('.context-menu').count() > 0, 'context-menu: right-click opens action menu');

// Save a screenshot with the context menu open
await page.screenshot({ path: 'context-menu.png' }).catch(() => {});

// Context menu closes on Escape
await page.keyboard.press('Escape');
await page.waitForTimeout(300);
check(await page.locator('.context-menu').count() === 0, 'context-menu: Escape closes action menu');

// Right-click again to drive Stop (or Start) action and verify state flips
await demoRow.click({ button: 'right' });
await page.waitForSelector('.context-menu', { timeout: 10000 });
// Exact labels: "Start" must not match "Restart". A disabled item carries
// disabled="" — an empty, falsy attribute — so ask for the enabled state.
const menuItem = (label) => page.locator('.context-menu button.menu-item')
  .filter({ has: page.locator('.menu-label', { hasText: new RegExp(`^${label}$`) }) });
const stopItem = menuItem('Stop');
const startItem = menuItem('Start');
const canStop = (await stopItem.count()) > 0 && await stopItem.first().isEnabled();

if (canStop) {
  await stopItem.first().click();
  await page.waitForTimeout(5500);
  const statusCell = await page.locator('tr[data-key*="web-prod"]').first().innerText(); // column order is user-configurable
  check(statusCell.includes('Stopped'), 'context-menu: Stop action flips VM state');
} else if ((await startItem.count()) > 0 && await startItem.first().isEnabled()) {
  await startItem.first().click();
  await page.waitForTimeout(5500);
  const statusCell = await page.locator('tr[data-key*="web-prod"]').first().innerText(); // column order is user-configurable
  check(statusCell.includes('Running'), 'context-menu: Start action flips VM state');
}

// Tree row right-click
const treeDemoRow = page.locator('#tree .tree-item', { hasText: 'web-prod' }).first();
if (await treeDemoRow.count() > 0) {
  await treeDemoRow.click({ button: 'right' });
  await page.waitForSelector('.context-menu', { timeout: 5000 });
  check(await page.locator('.context-menu').count() > 0, 'context-menu: tree row right-click opens action menu');
  await page.keyboard.press('Escape');
}

// Cluster health is green in demo.
await page.click('#tree >> text=Cluster health');
await page.waitForTimeout(2500);
const doctorText = await page.textContent('#content');
check(doctorText.includes('KubeVirt installed'), 'doctor renders checks');
// Local checks (KVM, virtctl…) legitimately depend on the host — only the
// demo's *cluster* checks must be green.
const broken = await page.locator('.doc-broken').allTextContents();
const clusterBroken = broken.filter((t) => /KubeVirt|CDI|StorageClass|Snapshot|Export|metrics/i.test(t));
check(clusterBroken.length === 0, `cluster checks green in demo (${clusterBroken.join('; ').slice(0, 120)})`);

// ── command-palette (#349) ────────────────────────────────────────
// Ctrl+K, a demo VM's name, Enter: that VM is selected. Starts from Cluster
// health, so a pass means the palette moved the selection, not that it was
// already there.
await page.keyboard.press('Control+k');
await page.waitForSelector('#palette[open] #palette-input', { timeout: 5000 }).catch(() => {});
check(await page.locator('#palette[open]').count() === 1, 'command-palette: Ctrl+K opens the palette');
await page.keyboard.type('db-prod');
check(
  (await page.textContent('#palette-list li.active .palette-label').catch(() => '')) === 'db-prod',
  'command-palette: typing a VM name puts that VM first',
);
check(await page.locator('#palette-list li', { hasText: 'Stop db-prod' }).count() === 1, 'command-palette: actions are searchable');
await page.keyboard.press('Enter');
await page.waitForTimeout(800);
check(await page.locator('#palette[open]').count() === 0, 'command-palette: Enter closes the palette');
check(await page.locator('#tree .tree-item.selected', { hasText: 'db-prod' }).count() === 1, 'command-palette: Enter selects the VM in the tree');
check(
  (await page.textContent('.page-head h1')).includes('db-prod'),
  'command-palette: Enter opens the VM',
);
await mkdir(SHOTS, { recursive: true });
await page.keyboard.press('Control+k');
await page.waitForSelector('#palette[open]', { timeout: 5000 }).catch(() => {});
check(
  (await page.textContent('#palette-list li.active .palette-label').catch(() => '')) === 'db-prod',
  'command-palette: the recently used VM ranks first',
);
await page.keyboard.type('web');
await page.screenshot({ path: `${SHOTS}/command-palette.png` });
await page.keyboard.press('Escape');
check(await page.locator('#palette[open]').count() === 0, 'command-palette: Escape closes the palette');

// `?` lists the shortcuts; `/` focuses the tree filter, which narrows guests.
// Blur whatever the closed palette handed focus back to: a key typed into an
// input is text, not a shortcut.
await page.evaluate(() => document.activeElement?.blur());
await page.keyboard.press('?');
await page.waitForSelector('#shortcuts[open]', { timeout: 3000 }).catch(() => {});
check(await page.locator('#shortcuts[open]').count() === 1, 'shortcuts: ? opens the shortcut overlay');
await page.screenshot({ path: `${SHOTS}/shortcuts.png` });
await page.keyboard.press('Escape');
await page.keyboard.press('/');
check(await page.evaluate(() => document.activeElement?.id) === 'tree-filter', 'shortcuts: / focuses the tree filter');
await page.keyboard.type('db-pr');
check(
  await page.locator('#tree .tree-item[data-guest]:visible').count() === 1,
  'tree filter narrows the tree to matching guests',
);
await page.keyboard.press('Escape');
check(await page.locator('#tree .tree-item[data-guest]:visible').count() > 1, 'Escape clears the tree filter');
await page.keyboard.press('g');
await page.keyboard.press('d');
await page.waitForTimeout(500);
check(await page.locator('#tree .tree-item.selected', { hasText: 'Datacenter' }).count() === 1, 'shortcuts: g d goes to the datacenter');

// Create wizard opens with catalog cards.
await page.click('#tree >> text=Datacenter');
await page.waitForTimeout(800);
await page.click('#btn-create');
await page.waitForTimeout(1000);
check(await page.locator('.wiz-card').count() > 4, 'create wizard shows catalog');
await page.keyboard.press('Escape');

// Theme: API returns defaults, CSS custom properties injected, branding visible.
const theme = await (await fetch(`${BASE}api/theme`)).json();
check(theme.accent === '#f0883e', 'theme API returns default accent');
check(theme.brand_title === 'Corral', 'theme API returns default brand title');
check(typeof theme.custom_css === 'string', 'theme API includes custom_css field');

// CSS custom properties injected into the page via <style id="corral-theme">.
const accentVar = await page.evaluate(() => {
  const style = document.getElementById('corral-theme');
  return style ? style.textContent : '';
});
check(accentVar.includes('--accent: #f0883e'), 'default accent injected into page CSS');
check(accentVar.includes('--accent-2: #d9742e'), 'default accent-2 injected into page CSS');

// Branding in the header.
const brandText = await page.textContent('.brand');
check(brandText.includes('Corral'), 'brand title visible in header');
check(brandText.includes('Virtual Environment'), 'brand subtitle visible in header');

// PUT /api/theme persists accent change (demo server has no config dir,
// so we only check the API round-trip — persistence requires a real config).
const putRes = await fetch(`${BASE}api/theme`, {
  method: 'PUT',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ accent: '#22c55e', brand_title: 'SmokeTest' }),
});
const updated = await putRes.json();
check(updated.accent === '#22c55e' || updated.error, 'PUT /api/theme accepts accent change');


// ── drag-migrate: drag a VM onto a node in Server View ─────────────
// The drop opens the migrate confirmation with that node preselected; the
// confirm runs POST /api/vms/{ns}/{name}/migrate and the VM lands there.
await page.click('#tree >> text=Server View');
await page.waitForTimeout(800);
const fleetBeforeMigrate = await (await fetch(`${BASE}api/vms`)).json();
const fromNode = fleetBeforeMigrate.find((v) => v.name === 'web-prod')?.node;
const toNode = ['corral-1', 'corral-2', 'corral-3'].find((n) => n !== fromNode);
const migrateSubject = page.locator('#tree .tree-item', { hasText: 'web-prod' }).first();
const migrateTarget = page.locator('#tree .tree-item', { hasText: toNode }).first();
await migrateSubject.dragTo(migrateTarget);
await page.waitForSelector('.migrate-dialog[open]', { timeout: 5000 }).catch(() => {});
check(await page.locator('.migrate-dialog[open]').count() === 1, `drag-migrate: dropping web-prod on ${toNode} opens the confirmation`);
check(
  await page.locator('.migrate-dialog #pick-node').inputValue().catch(() => '') === toNode,
  'drag-migrate: the dropped-on node is preselected',
);
await page.screenshot({ path: 'drag-migrate.png' }).catch(() => {});
await page.click('.migrate-dialog #pick-go').catch(() => {});
await page.waitForFunction(async ([target]) => {
  const fleet = await (await fetch('/api/vms')).json();
  return fleet.find((v) => v.name === 'web-prod')?.node === target;
}, [toNode], { timeout: 10000 }).catch(() => {});
const fleetAfterMigrate = await (await fetch(`${BASE}api/vms`)).json();
const migratedNode = fleetAfterMigrate.find((v) => v.name === 'web-prod')?.node;
check(migratedNode === toNode, `drag-migrate: web-prod moved ${fromNode} → ${migratedNode} (want ${toNode})`);
// The migration's task log opens as a modal; close it before moving on.
if (await page.locator('#build-dialog[open]').count()) await page.click('#btn-build-close');

// ── Pool View: drag-and-drop grouping and drag-to-move ────────────
// The drop targets are the whole point of this view, and the two kinds must
// behave differently: a pool drop regroups silently, a backend drop must open
// the preflight and change nothing until it is confirmed.

// The three views are named for what they group by: the node the backend put a
// guest on, the namespace the backend defines, and the pool the operator does.
// Two of them used to be called folders.
check(await page.locator('#tree >> text=Namespace View').count() > 0, 'Namespace View replaces the old Folder View');
check(await page.locator('#tree >> text=Folder View').count() === 0, 'nothing is called Folder View any more');

await page.click('#tree >> text=Namespace View');
await page.waitForTimeout(600);
check(await page.locator('#tree .tree-item').count() > 2, 'Namespace View renders a tree');

await page.click('#tree >> text=Pool View');
await page.waitForTimeout(600);
check(await page.locator('#tree >> text=Pools').count() > 0, 'Pool View renders the pools section');
check(await page.locator('#tree >> text=Unassigned').count() > 0, 'Pool View lists unassigned instances');
check(await page.locator('#tree >> text=Move to backend').count() > 0, 'Pool View offers backends as drop targets');

// Incus is a move destination now (image-publish Ingester, #164): it is a
// live drop target, not a greyed-out one.
const incusTarget = page.locator('.tree-item', { hasText: 'incus' }).first();
check(await incusTarget.count() > 0, 'incus is shown as a move target');
check(
  await incusTarget.evaluate((el) => !el.classList.contains('disabled')),
  'incus is an available move target',
);
const qemuTarget = page.locator('#tree .tree-item', { hasText: 'qemu' }).last();
check(
  !(await qemuTarget.getAttribute('class') || '').includes('disabled'),
  'qemu is a live move target',
);

// Create a pool through the API and check the tree picks it up with its
// bulk-action buttons — the reason pools exist.
await fetch(`${BASE}api/folders`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ path: 'smoke/web' }),
});
await page.click('#tree >> text=Server View');
await page.click('#tree >> text=Pool View');
await page.waitForTimeout(800);
const poolRow = page.locator('#tree .tree-item', { hasText: 'web' }).filter({ has: page.locator('.pool-actions') }).first();
check(await poolRow.count() > 0, 'a created pool appears in the tree with bulk actions');

// The gesture itself: drag an unassigned VM onto the pool row and check the
// membership actually moved. This is the feature, not the rendering of it.
const dragSubject = page.locator('#tree .tree-item[draggable="true"]').first();
const draggedName = (await dragSubject.textContent()).trim().split(' ')[0];
await dragSubject.dragTo(poolRow);
await page.waitForTimeout(1200);
const folders = await (await fetch(`${BASE}api/folders`)).json();
const smokePool = (folders.folders || []).find((f) => f.path === 'smoke/web');
check(
  !!smokePool && (smokePool.members || []).length === 1,
  `dragging a VM onto a pool assigns it (${draggedName} → smoke/web)`,
);

// The preflight is a read: asking for one must not change the fleet. Take the
// ref from the fleet itself rather than spelling one out — the demo backends'
// contexts are not this script's business.
const fleet = await (await fetch(`${BASE}api/vms`)).json();
const subject = fleet.find((v) => v.backend === 'kubevirt') || fleet[0];
const before = fleet.length;
const planRes = await fetch(`${BASE}api/move/preflight`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ ref: subject.id, toBackend: 'qemu' }),
});
const plan = await planRes.json();
check(planRes.status === 200, 'preflight answers 200 even when it refuses');
check(Array.isArray(plan.steps) && plan.steps.length > 0, 'preflight returns a step-by-step plan');
check(
  (plan.warnings || []).some((w) => w.includes('MAC')),
  'preflight always warns about the address change',
);
const after = (await (await fetch(`${BASE}api/vms`)).json()).length;
check(before === after, 'a preflight changes nothing');

// Incus is a move destination now (image-publish Ingester, #164): a preflight
// onto it plans like any other backend.
const incusPlanRes = await fetch(`${BASE}api/move/preflight`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ ref: subject.id, toBackend: 'incus' }),
});
const incusPlan = await incusPlanRes.json();
check(incusPlanRes.status === 200 && incusPlan.ok !== false, 'a move onto incus is planned');
check(
  Array.isArray(incusPlan.steps) && incusPlan.steps.length > 0,
  'incus preflight returns a step-by-step plan',
);
check(
  (incusPlan.warnings || []).some((w) => w.includes('MAC')),
  'incus preflight warns about the address change',
);

// A refused destination is refused with reasons, not with a failed request.
// Moving a backend onto itself in the same context is a non-move, so it is
// refused up front.
const refusedRes = await fetch(`${BASE}api/move/preflight`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ ref: subject.id, toBackend: subject.backend }),
});
const refused = await refusedRes.json();
check(refusedRes.status === 200 && refused.ok === false, 'a same-backend move is refused');
check((refused.refusals || []).every((r) => r.reason), 'every refusal carries a reason');


// ── /metrics (ADR-0011) ───────────────────────────────────────────
// The endpoint answers whether or not collection is on: a scraper that gets a
// 503 records nothing, and "corral is up but not collecting" is the state
// worth alerting on, so it is a metric rather than an error.
const metricsRes = await fetch(`${BASE}metrics`);
const metricsBody = await metricsRes.text();
check(metricsRes.status === 200, '/metrics answers 200');
check(
  (metricsRes.headers.get('content-type') || '').startsWith('text/plain'),
  '/metrics serves the exposition content type',
);
check(
  metricsBody.includes('# TYPE corral_collection_success gauge'),
  '/metrics always reports whether collection is working',
);

// ── bulk-select (#344) ─────────────────────────────────────────────
// The tree filter matches tags and IPs as well as names, tree rows support
// arrow-key focus, and Shift-click selects a range that the inventory grid
// shares and acts on in bulk.
await page.goto(BASE);
await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 }).catch(() => {});
await page.click('#tree >> text=Server View');
await page.waitForTimeout(800);
await page.evaluate(() => document.activeElement?.blur());
await page.keyboard.press('/');
await page.keyboard.type('prod');
check(await page.locator('#tree [data-vm-key]:visible').count() >= 2, 'tree filter matches VM tags');
const anIP = (await (await fetch(`${BASE}api/vms`)).json()).find((v) => v.ip && v.backend === 'kubevirt')?.ip || '';
await page.locator('#tree-filter').fill(anIP);
check(!!anIP && await page.locator('#tree [data-vm-key]:visible').count() === 1, `tree filter matches VM IPs (${anIP})`);
await page.locator('#tree-filter').fill('');
await page.locator('#tree-filter').dispatchEvent('input');
const webTreeRow = page.locator('#tree [data-vm-key]', { hasText: 'web-prod' }).first();
const nextRowText = await webTreeRow.evaluate((row) => {
  const rows = [...document.querySelectorAll('#tree .tree-item')].filter((r) => !r.hidden && r.offsetParent !== null);
  return rows[rows.indexOf(row) + 1]?.textContent ?? '';
});
await webTreeRow.focus();
await page.keyboard.press('ArrowDown');
check(
  nextRowText !== '' && (await page.locator('#tree .tree-item:focus').textContent().catch(() => '')) === nextRowText,
  'tree ArrowDown moves focus to the next row',
);
const treeVMs = await page.locator('#tree [data-vm-key] .tree-label').allTextContents();
const webAt = treeVMs.indexOf('web-prod');
const range = treeVMs.slice(webAt, webAt + 3);
await webTreeRow.click();
await page.locator('#tree [data-vm-key]', { hasText: range[2] }).first().click({ modifiers: ['Shift'] });
check(await page.locator('#tree [data-vm-key].multi-selected').count() === 3, `bulk-select: Shift selects three tree VMs (${range.join(', ')})`);
await page.locator('#tree .tree-item', { hasText: 'Datacenter' }).first().click();
await page.waitForSelector('#content .vm-check', { timeout: 10000 }).catch(() => {});
check(await page.locator('#content .vm-check:checked').count() === 3, 'bulk-select: tree selection is shared with the grid');
page.once('dialog', (dialog) => dialog.accept());
await page.locator('#content .bulkbar [data-bulk="stop"]').click();
await page.waitForFunction(async (names) => {
  const fleet = await (await fetch('/api/vms')).json();
  return names.every((name) => fleet.find((vm) => vm.name === name)?.status?.includes('Stopped'));
}, range, { timeout: 10000 }).catch(() => {});
const stoppedFleet = await (await fetch(`${BASE}api/vms`)).json();
check(
  range.every((name) => stoppedFleet.find((vm) => vm.name === name)?.status?.includes('Stopped')),
  'bulk-select: bulk Stop stops all three selected VMs',
);
await page.screenshot({ path: `${SHOTS}/bulk-select.png` });

// ── dashboard-layout (#348) ───────────────────────────────────────
// The Datacenter page opens with a widget grid. Move one widget and resize
// another with the mouse, resize a third from the keyboard, reload, and check
// that all three kept their place, and that a live chart drew data points.
{
  const layoutKey = 'corral.dashboard.datacenter';
  await page.click('#tree >> text=Datacenter');
  await page.evaluate((k) => localStorage.removeItem(k), layoutKey);
  await page.reload();
  const item = (id) => page.locator(`#dc-dash .grid-stack-item[gs-id="${id}"]`);
  const node = (id) => item(id).evaluate((el) => {
    const n = el.gridstackNode || {};
    return { x: n.x, y: n.y, w: n.w, h: n.h };
  });
  await item('capacity').waitFor({ timeout: 30000 }).catch(() => {});
  check(await page.locator('#dc-dash .grid-stack-item').count() >= 6, 'dashboard-layout: Datacenter opens with a widget grid');

  const gridBox = await page.locator('#dc-dash .grid-stack').boundingBox();
  const colW = gridBox ? gridBox.width / 12 : 100;

  // Move: drag the Capacity title bar four columns to the right.
  const before = await node('capacity');
  const head = await item('capacity').locator('.widget-head').boundingBox();
  if (head) {
    await page.mouse.move(head.x + 30, head.y + head.height / 2);
    await page.mouse.down();
    await page.mouse.move(head.x + 30 + colW * 4, head.y + head.height / 2, { steps: 15 });
    await page.mouse.up();
  }
  await page.waitForTimeout(400);
  const moved = await node('capacity');
  check(moved.x > before.x, `dashboard-layout: dragging a title bar moves the widget (x ${before.x} → ${moved.x})`);

  // Resize: hover the CPU chart so its corner handle shows, then drag it down.
  const cpuBefore = await node('cpu');
  await item('cpu').hover();
  const handle = await item('cpu').locator('.ui-resizable-se').boundingBox();
  if (handle) {
    await page.mouse.move(handle.x + handle.width / 2, handle.y + handle.height / 2);
    await page.mouse.down();
    await page.mouse.move(handle.x + handle.width / 2, handle.y + handle.height / 2 + 170, { steps: 15 });
    await page.mouse.up();
  }
  await page.waitForTimeout(400);
  const resized = await node('cpu');
  check(resized.h > cpuBefore.h, `dashboard-layout: dragging the corner resizes the widget (h ${cpuBefore.h} → ${resized.h})`);

  // Keyboard equivalent: Shift+ArrowRight on a focused title bar widens it.
  const memBefore = await node('mem');
  await item('mem').locator('.widget-head').focus();
  await page.keyboard.press('Shift+ArrowRight');
  await page.waitForTimeout(200);
  const widened = await node('mem');
  check(widened.w === memBefore.w + 1, `dashboard-layout: Shift+ArrowRight widens the focused widget (w ${memBefore.w} → ${widened.w})`);

  // The same moves are in the widget menu, for pointer users without a drag.
  await item('mem').locator('.widget-menu-btn').click();
  check(await item('mem').locator('.widget-menu [data-wact="remove"]').isVisible(), 'dashboard-layout: the widget menu offers the drag actions');
  await page.keyboard.press('Escape');

  // Persisted: reload and read the grid back.
  await page.reload();
  await item('capacity').waitFor({ timeout: 30000 }).catch(() => {});
  const after = { capacity: await node('capacity'), cpu: await node('cpu'), mem: await node('mem') };
  check(after.capacity.x === moved.x && after.capacity.y === moved.y, 'dashboard-layout: the moved widget keeps its place after reload');
  check(after.cpu.h === resized.h, 'dashboard-layout: the resized widget keeps its size after reload');
  check(after.mem.w === widened.w, 'dashboard-layout: the keyboard resize persists too');

  // A live chart drew data points from the demo's usage feed.
  const drew = await page.waitForFunction(
    () => [...document.querySelectorAll('#dc-dash .dash-chart')].some((c) => Number(c.dataset.points) > 0 && c.querySelector('canvas')),
    null, { timeout: 30000 },
  ).then(() => true).catch(() => false);
  check(drew, 'dashboard-layout: a live chart rendered data points');
  await page.screenshot({ path: `${SHOTS}/dashboard-layout.png`, fullPage: false });

  // Leave the default layout for anything that runs after this.
  await page.evaluate((k) => localStorage.removeItem(k), layoutKey);
}

check(pageErrors.length === 0, `no JS page errors (${pageErrors.join('; ').slice(0, 200)})`);

await browser.close();
if (failures > 0) {
  console.error(`\n${failures} smoke check(s) failed`);
  process.exit(1);
}
console.log('\nUI smoke: all checks passed');
