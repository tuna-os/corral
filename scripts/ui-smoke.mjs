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
await grid.screenshot({ path: `${SHOTS}/grid-columns.png` });
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
await page.screenshot({ path: `${SHOTS}/context-menu.png` }).catch(() => {});

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

// Hand the theme back the way it was found. This check writes a green accent
// and a "SmokeTest" brand, and the default-theme checks above read what the
// server currently holds, so leaving the write in place makes those checks
// fail on a second run against a long-lived demo server.
//
// The whole captured object goes back, not just the fields written: a PUT
// carrying an accent re-derives accent_2 by darkening it, and that derived
// shade is not the default accent_2, so restoring the accent alone would
// leave accent_2 shifted. Sending accent_2 explicitly takes precedence over
// the derived value.
await fetch(`${BASE}api/theme`, {
  method: 'PUT',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(theme),
}).catch(() => {});
const revertedTheme = await (await fetch(`${BASE}api/theme`)).json();
check(
  ['accent', 'accent_2', 'brand_title'].every((k) => revertedTheme[k] === theme[k]),
  'theme is restored so the suite stays re-runnable',
);


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
await page.screenshot({ path: `${SHOTS}/drag-migrate.png` }).catch(() => {});
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

// Put back what the bulk Stop took down. The demo server outlives a single
// run, so a check that mutates power state and walks away makes the suite
// pass once and fail after: the console checks above need a running VM, and
// they would fail on the second run for a reason that has nothing to do with
// the console. Restoring here keeps the suite idempotent against a
// long-lived `corral web --demo`.
for (const vm of stoppedFleet.filter((v) => range.includes(v.name))) {
  await fetch(`${BASE}api/vms/${vm.namespace}/${vm.name}/start`, { method: 'POST' }).catch(() => {});
}
await page.waitForFunction(async (names) => {
  const fleet = await (await fetch('/api/vms')).json();
  return names.every((name) => !fleet.find((vm) => vm.name === name)?.status?.includes('Stopped'));
}, range, { timeout: 10000 }).catch(() => {});
const restoredFleet = await (await fetch(`${BASE}api/vms`)).json();
check(
  range.every((name) => !restoredFleet.find((vm) => vm.name === name)?.status?.includes('Stopped')),
  'bulk-select: the stopped VMs are restarted so the suite stays re-runnable',
);

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

// ── es-modules: app.js split into native ES modules (#340) ─────────
// The UI loads as separate modules with no build step, and the modules share
// one state store and event bus. The page imports the same module URL the app
// does, so it gets the same instance, and a tree click must reach a bus
// listener with the selection the store now holds.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree >> text=web-prod', { timeout: 30000 }).catch(() => {});
  const modules = ['state.js', 'api.js', 'tree.js', 'console.js', 'dock.js', 'ui/menu.js', 'content/vm.js', 'content/datacenter.js'];
  const served = await page.evaluate(async (paths) => {
    const res = await Promise.all(paths.map((p) => fetch(p).then((r) => r.ok && (r.headers.get('content-type') || '').includes('javascript'))));
    return paths.filter((_, i) => !res[i]);
  }, modules);
  check(served.length === 0, `es-modules: every module is served as JavaScript (missing: ${served.join(', ') || 'none'})`);
  const bus = await page.evaluate(async () => {
    try {
      const m = await import('/state.js');
      window.__busEvents = [];
      m.on('select', (d) => window.__busEvents.push({ type: 'select', key: d.selected.key || '', tab: d.tab }));
      m.on('inventory', (d) => window.__busEvents.push({ type: 'inventory', vms: d.vms.length }));
      return typeof m.emit === 'function' && typeof m.state === 'object';
    } catch { return false; }
  });
  check(bus, 'es-modules: state.js exports the shared store and event bus');
  await page.click('#tree >> text=web-prod');
  await page.waitForFunction(() => (window.__busEvents || []).some((e) => e.type === 'select'), null, { timeout: 5000 }).catch(() => {});
  const sel = await page.evaluate(async () => {
    const ev = (window.__busEvents || []).find((e) => e.type === 'select');
    try {
      const { state } = await import('/state.js');
      return { ev, storeKey: state.selected.key || '' };
    } catch { return { ev, storeKey: null }; }
  });
  check(!!sel.ev && sel.ev.key.endsWith('/web-prod') && sel.ev.tab === 'summary', 'es-modules: a tree click emits a select event');
  check(!!sel.ev && sel.ev.key === sel.storeKey, 'es-modules: the select event matches the shared store');
  const inv = await page.waitForFunction(() => (window.__busEvents || []).find((e) => e.type === 'inventory' && e.vms > 0), null, { timeout: 12000 })
    .then((h) => h.jsonValue()).catch(() => null);
  check(!!inv, 'es-modules: the poll emits an inventory event');
  // Module scope keeps the fleet out of window, so no module can lean on a global.
  check(await page.evaluate(() => typeof window.vms === 'undefined' && typeof window.selected === 'undefined'), 'es-modules: no fleet globals on window');
  await page.screenshot({ path: `${SHOTS}/es-modules.png` });
}

// ── workspace-layout (#341) ───────────────────────────────────────
// The shell is viewport-bound: the header and the sidebar stay put and only
// the content pane scrolls. Before this, `body` used min-height, so the
// document grew past the viewport and the window scrolled — which carried the
// header and the whole navigation tree off-screen.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree .tree-item');
  const treeW = () => page.evaluate(() => Math.round(document.querySelector('#tree').getBoundingClientRect().width));
  const dockH = () => page.evaluate(() => Math.round(document.querySelector('#task-panel-body').getBoundingClientRect().height));

  await page.evaluate(() => window.scrollTo(0, 800));
  const pinned = await page.evaluate(() => ({
    windowScrolled: window.scrollY !== 0,
    headerTop: Math.round(document.querySelector('header').getBoundingClientRect().top),
    contentScrolls: (() => { const c = document.querySelector('#content'); return c.scrollHeight > c.clientHeight; })(),
  }));
  check(!pinned.windowScrolled, 'workspace-layout: the window does not scroll');
  check(pinned.headerTop === 0, `workspace-layout: the header stays pinned (top ${pinned.headerTop})`);
  check(pinned.contentScrolls, 'workspace-layout: the content pane scrolls instead');

  // Drag the sidebar wider.
  const before = await treeW();
  const handle = await page.$('#tree-resizer');
  const box = await handle.boundingBox();
  await page.mouse.move(box.x + 3, box.y + 200);
  await page.mouse.down();
  await page.mouse.move(box.x + 123, box.y + 200, { steps: 8 });
  await page.mouse.up();
  const dragged = await treeW();
  check(dragged > before + 80, `workspace-layout: dragging widens the sidebar (${before} → ${dragged})`);

  // Keyboard sizing, with Shift for a bigger step.
  await handle.focus();
  await page.keyboard.press('ArrowLeft');
  const narrowed = await treeW();
  check(narrowed === dragged - 10, `workspace-layout: ArrowLeft narrows by 10 (${dragged} → ${narrowed})`);
  await page.keyboard.down('Shift');
  await page.keyboard.press('ArrowRight');
  await page.keyboard.up('Shift');
  const widened = await treeW();
  check(widened === narrowed + 40, `workspace-layout: Shift+ArrowRight widens by 40 (${narrowed} → ${widened})`);

  // The ARIA window-splitter contract: a separator that publishes its range.
  const aria = await page.evaluate(() => {
    const e = document.querySelector('#tree-resizer');
    return { role: e.getAttribute('role'), now: e.getAttribute('aria-valuenow'),
             min: e.getAttribute('aria-valuemin'), max: e.getAttribute('aria-valuemax') };
  });
  check(aria.role === 'separator' && aria.now && aria.min && aria.max,
    `workspace-layout: the separator publishes its range (${JSON.stringify(aria)})`);

  // Enter collapses and restores. The separator stays visible while collapsed
  // because it owns that key — hiding it would strand a keyboard operator.
  await handle.focus();
  await page.keyboard.press('Enter');
  const collapsed = await page.evaluate(() => document.body.classList.contains('tree-collapsed'));
  const handleVisible = await page.evaluate(() => {
    const e = document.querySelector('#tree-resizer');
    return e.getBoundingClientRect().width > 0;
  });
  check(collapsed, 'workspace-layout: Enter collapses the sidebar');
  check(handleVisible, 'workspace-layout: the separator survives the collapse');
  await page.keyboard.press('Enter');
  check(await page.evaluate(() => !document.body.classList.contains('tree-collapsed')),
    'workspace-layout: Enter again restores the sidebar');

  // The dock is a row of the shell, so making it taller shortens the content
  // pane rather than covering it.
  await page.click('#task-panel-head');
  await page.waitForTimeout(250);
  const d0 = await dockH();
  const dockBox = await (await page.$('#dock-resizer')).boundingBox();
  await page.mouse.move(dockBox.x + 400, dockBox.y + 3);
  await page.mouse.down();
  await page.mouse.move(dockBox.x + 400, dockBox.y - 90, { steps: 8 });
  await page.mouse.up();
  const d1 = await dockH();
  const contentH = await page.evaluate(() => Math.round(document.querySelector('#content').getBoundingClientRect().height));
  const shellH = await page.evaluate(() => window.innerHeight);
  check(d1 > d0 + 50, `workspace-layout: dragging grows the dock (${d0} → ${d1})`);
  check(contentH + d1 < shellH, 'workspace-layout: a taller dock takes height from content, not over it');

  // All three preferences survive a reload.
  const want = { tree: await treeW(), dock: d1 };
  await page.goto(BASE);
  await page.waitForSelector('#tree .tree-item');
  const got = { tree: await treeW(), dock: await dockH() };
  check(Math.abs(got.tree - want.tree) <= 2, `workspace-layout: the sidebar width persists (${want.tree} → ${got.tree})`);
  check(Math.abs(got.dock - want.dock) <= 2, `workspace-layout: the dock height persists (${want.dock} → ${got.dock})`);
  check(await page.evaluate(() => !document.querySelector('#task-panel').classList.contains('collapsed')),
    'workspace-layout: the dock remembers it was open');
  await page.screenshot({ path: `${SHOTS}/workspace-layout.png` });
}

// ── tree-reconcile (#341) ─────────────────────────────────────────
// The 5s poll used to rebuild every sidebar row, which threw away anything
// the browser hangs off node identity rather than markup: keyboard focus, an
// in-progress drag, a text selection. The tree now reconciles its rows, so a
// poll that changes nothing must leave the nodes it already has alone.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  await page.click('#tree >> text=Server View');
  await page.waitForTimeout(500);

  // Stamp the live nodes, then sit through two full poll cycles.
  await page.evaluate(() => document.querySelectorAll('#tree .tree-item').forEach((r, i) => { r.__smoke = i; }));
  const rowsBefore = await page.locator('#tree .tree-item').count();
  await page.waitForTimeout(11000);
  const survivors = await page.evaluate(
    () => [...document.querySelectorAll('#tree .tree-item')].filter((r) => r.__smoke !== undefined).length,
  );
  const rowsAfter = await page.locator('#tree .tree-item').count();
  check(rowsBefore > 0 && rowsAfter === rowsBefore, `tree-reconcile: the row count is stable across polls (${rowsBefore} → ${rowsAfter})`);
  check(survivors === rowsBefore, `tree-reconcile: a poll reuses the existing rows (${survivors}/${rowsBefore} kept their identity)`);

  // The point of keeping the nodes: focus is a property of the node, so a
  // rebuilt row drops it and arrow-key navigation resets to the top mid-poll.
  await page.locator('#tree [data-vm-key]').first().focus();
  const focusBefore = await page.evaluate(() => document.activeElement?.dataset?.vmKey);
  await page.waitForTimeout(6000);
  const focusAfter = await page.evaluate(() => document.activeElement?.dataset?.vmKey);
  check(!!focusBefore && focusBefore === focusAfter, `tree-reconcile: keyboard focus survives a poll (${focusBefore})`);

  // Matching by key means a key that appears twice has to consume two nodes.
  // Keeping one node per key instead left the duplicates unmatched and
  // unremoved, and the sidebar grew a copy of a row on every poll.
  const dupes = await page.evaluate(() => {
    const keys = [...document.querySelectorAll('#tree [data-rkey]')].map((r) => r.dataset.rkey);
    return keys.filter((k, i) => keys.indexOf(k) !== i);
  });
  check(dupes.length === 0, `tree-reconcile: no row is duplicated by the diff (${dupes.join(', ') || 'none'})`);

  // A view switch replaces the groups, so the rows must not survive it.
  await page.click('#tree >> text=Namespace View');
  await page.waitForTimeout(500);
  check(
    await page.locator('#tree .tree-item[data-guest]').count() > 0,
    'tree-reconcile: switching view still rebuilds the groups',
  );
  await page.click('#tree >> text=Server View');
  await page.waitForTimeout(500);
}

// ── palette-reach (#349) ──────────────────────────────────────────
// The command palette was keyboard-only: Ctrl/Cmd+K is undiscoverable without
// a keyboard and untypeable on a phone, so the palette was unreachable there.
// It now also has a header button, and the result count is announced.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });

  check(await page.locator('#btn-palette').count() === 1, 'palette-reach: the header has a palette button');
  await page.click('#btn-palette');
  await page.waitForSelector('#palette[open]', { timeout: 5000 }).catch(() => {});
  check(await page.locator('#palette[open]').count() === 1, 'palette-reach: the button opens the palette');
  check(
    await page.evaluate(() => document.activeElement?.id) === 'palette-input',
    'palette-reach: opening it focuses the input',
  );

  // The count is what a screen reader has to fall back on: arrowing a listbox
  // reads each option but never says how many there are.
  await page.locator('#palette-input').fill('prod');
  await page.waitForTimeout(300);
  const announced = await page.textContent('#palette-count');
  check(/\d+ results?$/.test(announced.trim()), `palette-reach: the result count is announced ("${announced.trim()}")`);
  check(
    await page.getAttribute('#palette-count', 'aria-live') === 'polite',
    'palette-reach: the count is announced politely, not on every keystroke',
  );
  await page.locator('#palette-input').fill('zzzzznope');
  await page.waitForTimeout(300);
  check((await page.textContent('#palette-count')).includes('No matches'), 'palette-reach: an empty result is announced too');
  await page.screenshot({ path: `${SHOTS}/palette-reach.png` });
  await page.keyboard.press('Escape');

  // At drawer width the button has to survive — it is the only way in — so it
  // gives up its label, not its place.
  await page.setViewportSize({ width: 420, height: 800 });
  await page.waitForTimeout(300);
  check(await page.locator('#btn-palette').isVisible(), 'palette-reach: the button survives at phone width');
  check(
    !(await page.locator('#btn-palette .btn-label').isVisible()),
    'palette-reach: it collapses to the icon rather than crowding the header',
  );
  check(
    (await page.getAttribute('#btn-palette', 'aria-label') || '').length > 0,
    'palette-reach: the icon-only button still has an accessible name',
  );
  await page.click('#btn-palette');
  await page.waitForSelector('#palette[open]', { timeout: 5000 }).catch(() => {});
  check(await page.locator('#palette[open]').count() === 1, 'palette-reach: the palette opens at phone width');
  await page.keyboard.press('Escape');
  await page.setViewportSize({ width: 1440, height: 900 });
}

// ── interaction-guard (#341) ──────────────────────────────────────
// The content pane rebuilds from markup, so a poll that lands mid-gesture
// destroys whatever the gesture was working on: a column being dragged to a
// new width, a half-made selection, a row on its way to a node. The poll now
// holds off while a pointer is down and runs the render it skipped as soon as
// the gesture ends, so the change is deferred rather than lost.
{
  await page.goto(BASE);
  await page.waitForSelector('#content .vm-check', { timeout: 30000 });
  const GRID_ROWS = '#content .grid-scroll table tbody tr';
  const stampRows = () => page.evaluate((sel) => {
    const rows = document.querySelectorAll(sel);
    rows.forEach((r, i) => { r.__smokeRow = i; });
    return rows.length;
  }, GRID_ROWS);
  const stillStamped = () => page.evaluate(
    (sel) => [...document.querySelectorAll(sel)].filter((r) => r.__smokeRow !== undefined).length,
    GRID_ROWS,
  );

  const fleet = await (await fetch(`${BASE}api/vms`)).json();
  const victim = fleet.find((v) => v.status?.includes('Running') && v.backend === 'kubevirt');
  const stamped = await stampRows();

  // Hold a pointer down, then change the fleet behind the UI's back. An idle
  // poll renders nothing anyway (the fingerprint is unchanged), so the guard
  // is only exercised when the data really moves mid-gesture.
  const box = await page.locator('#content').boundingBox();
  await page.mouse.move(box.x + box.width - 5, box.y + box.height - 5);
  await page.mouse.down();
  if (victim) {
    await fetch(`${BASE}api/vms/${victim.namespace}/${victim.name}/stop`, { method: 'POST' }).catch(() => {});
  }
  await page.waitForTimeout(8000); // longer than one 5s poll cycle
  const heldThrough = await stillStamped();
  check(
    !!victim && stamped > 0 && heldThrough === stamped,
    `interaction-guard: a poll does not rebuild the grid under a held pointer (${heldThrough}/${stamped} rows kept)`,
  );

  // Letting go must land the change, not drop it. The grid diffs its rows, so
  // the deferred render replaces the row whose data moved and keeps the rest —
  // "some rows were rebuilt", not "all of them were".
  await page.mouse.up();
  await page.waitForFunction(
    (sel) => [...document.querySelectorAll(sel)].some((r) => r.__smokeRow === undefined),
    GRID_ROWS,
    { timeout: 10000 },
  ).catch(() => {});
  const keptAfter = await stillStamped();
  check(
    keptAfter < stamped,
    `interaction-guard: releasing runs the render the poll skipped (${stamped - keptAfter} of ${stamped} rows updated)`,
  );
  check(
    (await page.textContent('#content .grid-scroll table')).includes('Stopped'),
    'interaction-guard: the deferred render shows the change made during the gesture',
  );
  await page.screenshot({ path: `${SHOTS}/interaction-guard.png` });

  // Start it again — this check stops a VM, and the console checks need one
  // running. Same reason the bulk-select check restores its three.
  if (victim) {
    await fetch(`${BASE}api/vms/${victim.namespace}/${victim.name}/start`, { method: 'POST' }).catch(() => {});
    await page.waitForFunction(async (name) => {
      const list = await (await fetch('/api/vms')).json();
      return !list.find((v) => v.name === name)?.status?.includes('Stopped');
    }, victim.name, { timeout: 10000 }).catch(() => {});
    const back = await (await fetch(`${BASE}api/vms`)).json();
    check(
      !back.find((v) => v.name === victim.name)?.status?.includes('Stopped'),
      'interaction-guard: the stopped VM is restarted so the suite stays re-runnable',
    );
  }
}

// ── reset-layout (#341) ───────────────────────────────────────────
// Sidebar width, dock height and both collapse states are remembered per
// browser, which is the point and also the trap: the vSphere Web Client let
// admins close its Recent Tasks pane with no way back, and the vendor's own
// advice was to clear the browser cache. A customisable layout needs an undo,
// so the workspace gets the same "Reset layout" the dashboard widgets have.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  const treeWidth = () => page.evaluate(() => document.querySelector('#tree').getBoundingClientRect().width);
  const collapsed = () => page.evaluate(() => document.body.classList.contains('tree-collapsed'));
  const dockOpen = () => page.evaluate(() => !document.querySelector('#task-panel').classList.contains('collapsed'));

  // The shipped default, not whatever is stored now: earlier checks in this
  // same run leave the sidebar at a width they chose, and the whole point of a
  // reset is to get back past that.
  const TREE_DEFAULT = 270;
  const started = await treeWidth();
  // Customise all three, including collapsing the pane — the state that is
  // hardest to get back out of.
  await page.locator('#tree-resizer').focus();
  for (let i = 0; i < 4; i++) await page.keyboard.press('Shift+ArrowRight');
  // Open the dock if it is not already — an earlier check leaves it open, and
  // a blind toggle would close it and quietly invert this setup.
  if (!(await dockOpen())) {
    await page.click('#task-panel-head');
    await page.waitForTimeout(400);
  }
  const wide = await treeWidth();
  await page.locator('#tree-resizer').focus();
  await page.keyboard.press('Enter');
  await page.waitForTimeout(300);
  check(
    wide > started && wide !== TREE_DEFAULT && await dockOpen() && await collapsed(),
    `reset-layout: the layout is customised first (${started} → ${wide}, dock open, sidebar collapsed)`,
  );

  // Reachable by name, not just by a button someone has to already know about.
  await page.keyboard.press('Control+k');
  await page.waitForSelector('#palette[open]', { timeout: 5000 });
  await page.locator('#palette-input').fill('reset layout');
  await page.waitForTimeout(400);
  check(
    (await page.textContent('#palette-list li')).includes('Reset layout'),
    'reset-layout: the palette finds it by name',
  );
  await page.keyboard.press('Enter');
  await page.waitForTimeout(600);

  check(await treeWidth() === TREE_DEFAULT, `reset-layout: the sidebar is back to its default width (${TREE_DEFAULT})`);
  check(!(await collapsed()), 'reset-layout: a collapsed sidebar is restored');
  check(!(await dockOpen()), 'reset-layout: the dock is back to shipping closed');

  // The reset has to be written, not just applied: a reset that a reload undoes
  // is no way out of a bad stored layout.
  await page.reload();
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  check(await treeWidth() === TREE_DEFAULT, 'reset-layout: the reset survives a reload');
  await page.screenshot({ path: `${SHOTS}/reset-layout.png` });
}

// ── grid-reconcile (#346) ─────────────────────────────────────────
// The grid re-renders on sort, on every keystroke in a column filter, on a
// column reorder and on virtual scroll. It used to replace every row each
// time, which threw away the focused row, a checkbox mid-click and any text
// selection in a cell — the same reason the column filter needs a
// requestAnimationFrame to put focus back. Rows are now diffed by key.
{
  await page.goto(BASE);
  await page.waitForSelector('#content .vm-check', { timeout: 30000 });
  const ROWS = '#content .grid-scroll table tbody tr';
  const stampRows = () => page.evaluate((sel) => {
    const rows = document.querySelectorAll(sel);
    rows.forEach((r, i) => { r.__smokeGrid = i; });
    return rows.length;
  }, ROWS);
  const kept = () => page.evaluate(
    (sel) => [...document.querySelectorAll(sel)].filter((r) => r.__smokeGrid !== undefined).length,
    ROWS,
  );

  // Sorting reorders the same rows; none of them need rebuilding.
  const before = await stampRows();
  await page.locator('#content .grid-sort').first().click();
  await page.waitForTimeout(400);
  check(before > 0 && await kept() === before, `grid-reconcile: sorting reuses the rows (${await kept()}/${before})`);

  // Filtering removes the rows that no longer match and keeps the ones that do
  // — it must not rebuild the survivors.
  await stampRows();
  // The term comes from the data actually on screen, not a guessed name: an
  // earlier check reorders and hides columns and that choice persists, so the
  // first column here is whatever that check left behind.
  const firstCell = (await page.evaluate((sel) => {
    const cells = document.querySelectorAll(`${sel}:first-child td`);
    return cells[1]?.textContent?.trim() || '';
  }, ROWS)).slice(0, 4);
  await page.locator('#content .grid-filters input').first().fill(firstCell);
  await page.waitForTimeout(500);
  const shown = await page.locator(ROWS).count();
  check(
    !!firstCell && shown > 0 && shown < before,
    `grid-reconcile: the filter narrows the grid on "${firstCell}" (${before} → ${shown})`,
  );
  check(await kept() === shown, `grid-reconcile: the rows that still match are kept, not rebuilt (${await kept()}/${shown})`);

  await page.locator('#content .grid-filters input').first().fill('');
  await page.waitForTimeout(400);
  check(await page.locator(ROWS).count() === before, 'grid-reconcile: clearing the filter brings the rows back');
  check(await page.locator('#content .vm-check').count() > 0, 'grid-reconcile: the row checkboxes still work after a diff');
  await page.screenshot({ path: `${SHOTS}/grid-reconcile.png` });
}

// ── grid-survives-poll (#341) ─────────────────────────────────────
// The views rebuild their markup on every poll that changes anything, which
// used to hand the inventory grid a fresh host and mount a second grid over
// the first. The mounted grid is now moved into the new markup and fed the
// new rows, so only the rows that actually changed are rebuilt — and the
// focused row is still the focused row afterwards.
{
  await page.goto(BASE);
  await page.waitForSelector('#content .vm-check', { timeout: 30000 });
  const ROWS = '#content .grid-scroll table tbody tr';
  const stampRows = () => page.evaluate((sel) => {
    const rows = document.querySelectorAll(sel);
    rows.forEach((r, i) => { r.__smokePoll = i; });
    return rows.length;
  }, ROWS);
  const kept = () => page.evaluate(
    (sel) => [...document.querySelectorAll(sel)].filter((r) => r.__smokePoll !== undefined).length,
    ROWS,
  );

  const fleet = await (await fetch(`${BASE}api/vms`)).json();
  const victim = fleet.find((v) => v.status?.includes('Running') && v.backend === 'kubevirt');
  const total = await stampRows();
  await page.locator(ROWS).first().focus();
  const focusBefore = await page.evaluate(() => document.activeElement?.dataset?.key);
  // The demo fleet already contains stopped VMs, so waiting for the word
  // "Stopped" to appear anywhere would return before the poll had run. Wait
  // for one *more* row to say it than said it before.
  const stoppedRows = () => page.evaluate(
    (sel) => [...document.querySelectorAll(sel)].filter((r) => r.textContent.includes('Stopped')).length,
    ROWS,
  );
  const stoppedBefore = await stoppedRows();

  // Change the fleet so the poll has something to render, then let it run with
  // no gesture in the way.
  if (victim) {
    await fetch(`${BASE}api/vms/${victim.namespace}/${victim.name}/stop`, { method: 'POST' }).catch(() => {});
  }
  await page.waitForFunction(
    ({ sel, was }) => [...document.querySelectorAll(sel)].filter((r) => r.textContent.includes('Stopped')).length > was,
    { sel: ROWS, was: stoppedBefore },
    { timeout: 15000 },
  ).catch(() => {});

  const survivors = await kept();
  check(
    !!victim && total > 1 && survivors >= total - 2 && survivors < total,
    `grid-survives-poll: only the changed rows are rebuilt (${survivors}/${total} kept)`,
  );
  check(
    !!focusBefore && await page.evaluate(() => document.activeElement?.dataset?.key) === focusBefore,
    `grid-survives-poll: the focused row keeps focus through the poll (${focusBefore})`,
  );
  await page.screenshot({ path: `${SHOTS}/grid-survives-poll.png` });

  if (victim) {
    await fetch(`${BASE}api/vms/${victim.namespace}/${victim.name}/start`, { method: 'POST' }).catch(() => {});
    await page.waitForFunction(async (name) => {
      const list = await (await fetch('/api/vms')).json();
      return !list.find((v) => v.name === name)?.status?.includes('Stopped');
    }, victim.name, { timeout: 10000 }).catch(() => {});
    const back = await (await fetch(`${BASE}api/vms`)).json();
    check(
      !back.find((v) => v.name === victim.name)?.status?.includes('Stopped'),
      'grid-survives-poll: the stopped VM is restarted so the suite stays re-runnable',
    );
  }
}

// ── extensions-filter ─────────────────────────────────────────────
// The marketplace list only scrolled, on the one screen you arrive at already
// knowing the name of what you came for. The tree and every data grid here
// filter; this one now does too, and stays out of the way when there is
// nothing to sift through.
{
  await page.goto(BASE);
  await page.click('#tree >> text=Extensions');
  await page.waitForSelector('.ext-card', { timeout: 30000 });
  const cards = await page.locator('.ext-card').count();
  check(cards > 0, `extensions-filter: the marketplace lists plugins (${cards})`);
  check(await page.locator('#ext-filter').isVisible(), 'extensions-filter: a filter box is offered');

  const name = (await page.locator('.ext-card strong').first().textContent()).trim().slice(0, 4);
  await page.locator('#ext-filter').fill(name);
  await page.waitForTimeout(300);
  const hits = await page.locator('.ext-card:visible').count();
  check(hits > 0 && hits < cards, `extensions-filter: filtering on "${name}" narrows the list (${cards} → ${hits})`);

  // An empty result has to say so: a blank grid reads as a failed load.
  await page.locator('#ext-filter').fill('zzzznotaplugin');
  await page.waitForTimeout(300);
  check(await page.locator('.ext-card:visible').count() === 0, 'extensions-filter: a term matching nothing hides every card');
  check(await page.locator('.ext-empty').isVisible(), 'extensions-filter: an empty result says so rather than looking broken');

  await page.locator('#ext-filter').fill('');
  await page.waitForTimeout(300);
  check(await page.locator('.ext-card:visible').count() === cards, 'extensions-filter: clearing it brings every card back');
  await page.screenshot({ path: `${SHOTS}/extensions-filter.png` });
}

// ── storage-view (#350) ───────────────────────────────────────────
// Proxmox offers four groupings of the same objects — Server, Storage, Pool,
// Folder — and Storage was the one corral had no answer to. It groups images
// by the source they come from, and imported disks by namespace: the same
// fleet, grouped by where the bits live rather than by what is running them.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  check(await page.locator('[data-view="storage"]').count() === 1, 'storage-view: the tree offers a Storage View');

  await page.click('[data-view="storage"]');
  // Switching in fetches the catalogue rather than waiting out the 5s poll.
  await page.waitForFunction(
    () => [...document.querySelectorAll('#tree .tree-item')].some((r) => /image/.test(r.textContent)),
    null,
    { timeout: 15000 },
  ).catch(() => {});
  const sourceRows = await page.locator('#tree .tree-item').filter({ hasText: /\d+ images?/ }).count();
  check(sourceRows > 0, `storage-view: images are grouped by source (${sourceRows} sources)`);

  // The tree has room for a name and nothing else, so the source gets a screen.
  const source = page.locator('#tree .tree-item').filter({ hasText: /\d+ images?/ }).first();
  const sourceName = (await source.locator('.tree-label').textContent()).trim();
  await source.click();
  await page.waitForTimeout(800);
  check(
    (await page.textContent('#content h1')).includes(sourceName),
    `storage-view: picking a source opens it (${sourceName})`,
  );
  const listed = await page.locator('#content .template-table tbody tr').count();
  check(listed > 0, `storage-view: the source lists what it offers (${listed} images)`);
  await page.screenshot({ path: `${SHOTS}/storage-view.png` });

  // Back to Server View: the choice of view is remembered, so leaving the
  // suite in Storage View would change what every later run starts from.
  await page.click('[data-view="server"]');
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 15000 });
  check(
    await page.locator('#tree [data-vm-key]').count() > 0,
    'storage-view: switching back to Server View restores the fleet',
  );
}

// ── dock-tabs (#342) ──────────────────────────────────────────────
// The dock held one hard-coded Tasks panel. It is now a tab strip built from a
// list of panels, so adding one is a declaration rather than a rewrite of the
// head, and it carries the cluster events for whatever is selected. Tabs follow
// the APG pattern: one stop in the tab order, arrow keys between them, focus
// following selection.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  const tabs = await page.locator('.dock-tab').allTextContents();
  check(tabs.length >= 2, `dock-tabs: the dock offers more than one panel (${tabs.join(', ')})`);

  // Clicking the head still toggles the dock, which is how it always worked.
  if (await page.evaluate(() => document.querySelector('#task-panel').classList.contains('collapsed'))) {
    await page.click('#task-panel-head');
    await page.waitForTimeout(400);
  }
  check(await page.locator('#dock-panel-tasks').isVisible(), 'dock-tabs: Tasks is the panel it opens on');

  await page.click('.dock-tab[data-panel="events"]');
  await page.waitForTimeout(600);
  check(await page.locator('#dock-panel-events').isVisible(), 'dock-tabs: picking Events shows that panel');
  check(!(await page.locator('#dock-panel-tasks').isVisible()), 'dock-tabs: and hides the one it replaced');
  check(
    await page.getAttribute('.dock-tab[data-panel="events"]', 'aria-selected') === 'true',
    'dock-tabs: the selected tab says so',
  );

  // Events are per-VM, so with nothing selected the panel asks rather than
  // showing an empty table that looks like a failed load.
  check(
    await page.locator('#dock-events-hint').isVisible(),
    'dock-tabs: with no VM selected, Events asks for one',
  );
  await page.locator('#tree [data-vm-key]').first().click();
  await page.waitForTimeout(1200);
  check(
    !(await page.locator('#dock-events-hint').isVisible()),
    'dock-tabs: selecting a VM scopes Events to it',
  );
  await page.screenshot({ path: `${SHOTS}/dock-tabs.png` });

  // Arrow keys move along the strip and focus goes with the selection.
  await page.locator('.dock-tab[data-panel="events"]').focus();
  await page.keyboard.press('ArrowLeft');
  await page.waitForTimeout(400);
  check(
    await page.evaluate(() => document.querySelector('.dock-tab.active')?.dataset.panel) === 'tasks',
    'dock-tabs: ArrowLeft moves to the previous panel',
  );
  check(
    await page.evaluate(() => document.activeElement?.dataset?.panel) === 'tasks',
    'dock-tabs: focus follows the selection',
  );

  // Which panel you left open is a preference, like the dock height.
  await page.click('.dock-tab[data-panel="events"]');
  await page.waitForTimeout(300);
  await page.reload();
  await page.waitForSelector('.dock-tab', { timeout: 30000 });
  await page.waitForTimeout(800);
  check(
    await page.evaluate(() => document.querySelector('.dock-tab.active')?.dataset.panel) === 'events',
    'dock-tabs: the dock remembers which panel was showing',
  );

  // Back to Tasks so a later run starts where it used to.
  await page.click('.dock-tab[data-panel="tasks"]');
  await page.waitForTimeout(300);
}

// ── console-stays-live (#341) ─────────────────────────────────────
// A console tab used to freeze the whole content pane: rebuilding it would
// have dropped the WebSocket and reconnected every five seconds, so the poll
// skipped the pane entirely — and a VM could stop while the page still said it
// was running. The element the console is mounted in is now carried across the
// render, so the connection survives and everything around it stays live.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  const fleet = await (await fetch(`${BASE}api/vms`)).json();
  const victim = fleet.find((v) => v.running && v.backend === 'kubevirt');
  if (victim) {
    await page.locator('#tree .tree-item').filter({ hasText: victim.name }).first().click();
    await page.waitForTimeout(600);
    await page.click('[data-tab="console"]');
    await page.waitForSelector('#vnc-popout', { timeout: 30000 }).catch(() => {});

    // Stamp the console body and the tab strip beside it: one must survive, the
    // other must be rebuilt, which together prove the pane rendered *around*
    // the live connection.
    await page.evaluate(() => {
      const body = document.querySelector('#tab-body');
      const strip = document.querySelector('#content .tabs');
      if (body) body.__smokeBody = 1;
      if (strip) strip.__smokeStrip = 1;
    });
    const headBefore = (await page.textContent('#content .page-head')).replace(/\s+/g, ' ').trim();

    await fetch(`${BASE}api/vms/${victim.namespace}/${victim.name}/stop`, { method: 'POST' }).catch(() => {});
    await page.waitForFunction(
      () => document.querySelector('#content .page-head')?.textContent?.includes('Stopped'),
      null,
      { timeout: 20000 },
    ).catch(() => {});

    check(
      await page.evaluate(() => document.querySelector('#tab-body')?.__smokeBody === 1),
      'console-stays-live: the console keeps its element through a poll',
    );
    check(
      await page.evaluate(() => document.querySelector('#content .tabs')?.__smokeStrip === undefined),
      'console-stays-live: the page around it really did re-render',
    );
    const headAfter = (await page.textContent('#content .page-head')).replace(/\s+/g, ' ').trim();
    check(
      headAfter.includes('Stopped') && headAfter !== headBefore,
      'console-stays-live: the VM header follows the fleet while the console tab is open',
    );
    check(await page.locator('#vnc-popout').count() === 1, 'console-stays-live: the console controls are still there');
    await page.screenshot({ path: `${SHOTS}/console-stays-live.png` });

    // Start it again and leave the console, so later checks find the fleet as
    // they expect it.
    await fetch(`${BASE}api/vms/${victim.namespace}/${victim.name}/start`, { method: 'POST' }).catch(() => {});
    await page.waitForFunction(async (name) => {
      const list = await (await fetch('/api/vms')).json();
      return !list.find((v) => v.name === name)?.status?.includes('Stopped');
    }, victim.name, { timeout: 15000 }).catch(() => {});
    await page.click('#tree >> text=Datacenter');
    await page.waitForTimeout(500);
    const back = await (await fetch(`${BASE}api/vms`)).json();
    check(
      !back.find((v) => v.name === victim.name)?.status?.includes('Stopped'),
      'console-stays-live: the stopped VM is restarted so the suite stays re-runnable',
    );
  }
}

// ── multiview-stays-live (#341) ───────────────────────────────────
// Multiview was the last screen the poll refused to render, because rebuilding
// it would disconnect every tile and dial all six again. The tile grid is now
// carried across the render while the heading — which counts running VMs, and
// so changes when the six on screen do not — is rebuilt around it. With this
// there is no screen left that the poll has to skip.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  await page.click('#tree >> text=Multiview');
  await page.waitForSelector('#mv-grid .mv-tile', { timeout: 30000 }).catch(() => {});
  const tiles = await page.locator('#mv-grid .mv-tile').count();
  if (tiles > 0) {
    await page.evaluate(() => {
      document.querySelector('#mv-grid').__smokeGrid = 1;
      document.querySelector('#content .page-head').__smokeHead = 1;
    });

    // Tag a VM so the fleet really changes and the poll really renders.
    const fleet = await (await fetch(`${BASE}api/vms`)).json();
    const subject = fleet.find((v) => v.running && v.backend === 'kubevirt');
    if (subject) {
      await fetch(`${BASE}api/vms/${subject.namespace}/${subject.name}/tags`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tag: 'smoke-mv', on: true }),
      }).catch(() => {});
    }
    await page.waitForFunction(
      () => document.querySelector('#content .page-head')?.__smokeHead === undefined,
      null,
      { timeout: 20000 },
    ).catch(() => {});

    check(
      await page.evaluate(() => document.querySelector('#mv-grid')?.__smokeGrid === 1),
      `multiview-stays-live: the tile grid keeps its element through a poll (${tiles} tiles)`,
    );
    check(
      await page.evaluate(() => document.querySelector('#content .page-head')?.__smokeHead === undefined),
      'multiview-stays-live: the heading around it re-rendered',
    );
    check(
      await page.locator('#mv-grid .mv-tile').count() === tiles,
      'multiview-stays-live: every tile is still there',
    );
    await page.screenshot({ path: `${SHOTS}/multiview-stays-live.png` });

    // Take the tag back off, and leave Multiview so its consoles are dropped.
    if (subject) {
      await fetch(`${BASE}api/vms/${subject.namespace}/${subject.name}/tags`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tag: 'smoke-mv', on: false }),
      }).catch(() => {});
    }
    await page.click('#tree >> text=Datacenter');
    await page.waitForTimeout(500);
    const after = await (await fetch(`${BASE}api/vms`)).json();
    check(
      !(after.find((v) => v.name === subject?.name)?.tags || []).includes('smoke-mv'),
      'multiview-stays-live: the tag is removed so the suite stays re-runnable',
    );
  }
}

// ── grid-density-and-keys (#346) ──────────────────────────────────
// Two patterns the data-grid was missing. Density as three named modes
// remembered per grid, because how many rows fit is a judgement about the work
// rather than about the data. And arrow-key movement between rows: Tab belongs
// to the controls inside a row, so without arrows there was no key equivalent
// for picking a different one.
{
  await page.goto(BASE);
  await page.waitForSelector('#content .vm-check', { timeout: 30000 });
  const ROWS = '#content .grid-scroll table tbody tr';
  const rowHeight = () => page.evaluate(
    (sel) => Math.round(document.querySelector(sel)?.getBoundingClientRect().height || 0), ROWS,
  );

  check(await page.locator('#content .grid-density-select').count() === 1, 'grid-density: the grid offers a density control');
  await page.selectOption('#content .grid-density-select', 'compact');
  await page.waitForTimeout(400);
  const compact = await rowHeight();
  await page.selectOption('#content .grid-density-select', 'roomy');
  await page.waitForTimeout(400);
  const roomy = await rowHeight();
  check(compact > 0 && compact < roomy, `grid-density: compact really is shorter than roomy (${compact}px vs ${roomy}px)`);

  // Remembered per grid, like the column widths and saved views beside it.
  await page.reload();
  await page.waitForSelector('#content .vm-check', { timeout: 30000 });
  check(
    await page.evaluate(() => document.querySelector('#content .data-grid')?.dataset.density) === 'roomy',
    'grid-density: the choice survives a reload',
  );

  await page.selectOption('#content .grid-density-select', 'cosy');
  await page.waitForTimeout(400);

  // Arrow keys, Home and End over the rows.
  await page.locator(ROWS).first().focus();
  const first = await page.evaluate(() => document.activeElement?.dataset?.key);
  await page.keyboard.press('ArrowDown');
  const second = await page.evaluate(() => document.activeElement?.dataset?.key);
  check(!!first && !!second && first !== second, `grid-keys: ArrowDown moves to the next row (${second})`);
  await page.keyboard.press('ArrowUp');
  check(
    await page.evaluate(() => document.activeElement?.dataset?.key) === first,
    'grid-keys: ArrowUp comes back',
  );
  await page.keyboard.press('End');
  const last = await page.evaluate(() => document.activeElement?.dataset?.key);
  check(!!last && last !== first, `grid-keys: End jumps to the last row (${last})`);
  await page.keyboard.press('Home');
  check(
    await page.evaluate(() => document.activeElement?.dataset?.key) === first,
    'grid-keys: Home jumps back to the first',
  );
  check(
    await page.evaluate(() => {
      const tr = document.querySelector('#content .grid-scroll table tbody tr');
      return getComputedStyle(tr).getPropertyValue('cursor') !== '';
    }),
    'grid-keys: rows remain focusable targets',
  );
  await page.screenshot({ path: `${SHOTS}/grid-density.png` });
}

// ── palette-scope-and-keys (#349) ─────────────────────────────────
// Two patterns the palette was missing. A kind prefix narrows the list, the
// way k9s takes ":po" and VS Code's Quick Open takes a leading ">": one input,
// and a prefix says which list you mean. And a row shows the key that would
// also run it, which is how VS Code and Linear teach their own shortcuts — a
// shortcut nobody is shown is a shortcut nobody uses.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  await page.keyboard.press('Control+k');
  await page.waitForSelector('#palette[open]', { timeout: 5000 });
  const unscoped = await page.locator('#palette-list li').count();
  check(unscoped > 1, `palette-scope: the palette lists everything by default (${unscoped})`);

  const kindsFor = async (q) => {
    await page.locator('#palette-input').fill(q);
    await page.waitForTimeout(400);
    return page.evaluate(
      () => [...new Set([...document.querySelectorAll('#palette-list li')].map((li) => li.dataset.id?.split(':')[0]))],
    );
  };

  const vmKinds = await kindsFor('vm:');
  check(
    vmKinds.length === 1 && vmKinds[0] === 'vm',
    `palette-scope: "vm:" narrows to guests (${vmKinds.join(', ')})`,
  );
  const nodeKinds = await kindsFor('node:');
  check(
    nodeKinds.length === 1 && nodeKinds[0] === 'node',
    `palette-scope: "node:" narrows to nodes (${nodeKinds.join(', ')})`,
  );
  // "do:" keeps the things that act, and no plain guests or views.
  const doKinds = await kindsFor('do:');
  check(
    doKinds.length > 0 && !doKinds.includes('vm') && !doKinds.includes('view'),
    `palette-scope: "do:" keeps only the actions (${doKinds.slice(0, 5).join(', ')})`,
  );

  // An unknown prefix is a search term, not a scope: a VM called "db:1" must
  // still be findable.
  await page.locator('#palette-input').fill('zzz:');
  await page.waitForTimeout(400);
  check(
    (await page.textContent('#palette-count')).includes('No matches'),
    'palette-scope: an unknown prefix stays a search term',
  );

  await page.locator('#palette-input').fill('datacenter');
  await page.waitForTimeout(400);
  const hint = await page.evaluate(() => document.querySelector('#palette-list li .palette-keys')?.textContent || '');
  check(hint.includes('g') && hint.includes('d'), `palette-keys: the row shows its own shortcut (${hint})`);
  check(
    await page.evaluate(() => document.querySelector('#palette-list li .palette-keys')?.getAttribute('aria-hidden')) === 'true',
    'palette-keys: the hint is hidden from the accessibility tree, since the key lives in code',
  );
  await page.screenshot({ path: `${SHOTS}/palette-scope.png` });
  await page.keyboard.press('Escape');
}

// ── breadcrumb (#350) ─────────────────────────────────────────────
// A detail screen opened with a name and a status pill and nothing saying
// which node or namespace the guest sits on. Reached from the palette, a
// pop-out or a link, it carried no tree context at all. vSphere's object
// navigator and VS Code's breadcrumbs both answer that the same way: name the
// path above the object, and make each step a way back.
{
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  await page.locator('#tree [data-vm-key]').first().click();
  await page.waitForSelector('#content .breadcrumb', { timeout: 10000 }).catch(() => {});
  const crumbs = await page.locator('#content .breadcrumb .crumb').allTextContents();
  check(crumbs.length >= 2, `breadcrumb: a guest page shows its path (${crumbs.join(' > ')})`);
  check(crumbs[0].trim() === 'Datacenter', 'breadcrumb: the trail starts at the Datacenter');

  // The last step is the page you are on, so it is not a link and it says so.
  check(
    (await page.textContent('#content .breadcrumb [aria-current="page"]')).trim() === crumbs[crumbs.length - 1].trim(),
    'breadcrumb: the last step is marked as the current page',
  );
  const links = await page.locator('#content .breadcrumb button.crumb').count();
  check(links === crumbs.length - 1, `breadcrumb: every step above the page is a link (${links})`);

  // A trail nobody can follow is just decoration.
  await page.locator('#content .breadcrumb button.crumb').first().click();
  await page.waitForTimeout(700);
  check(
    (await page.textContent('#content h1')).includes('Datacenter'),
    'breadcrumb: a step navigates to that ancestor',
  );
  await page.screenshot({ path: `${SHOTS}/breadcrumb.png` });
}

// ── dashboard-on-a-phone (#348) ───────────────────────────────────
// A 12-column widget grid scaled to a phone gave each widget about a third of
// the screen: capacity figures wrapped and clipped, charts became a sliver,
// and the task table showed timestamps and nothing else. One column at drawer
// width, and the items stack at the height their content needs.
//
// The narrow layout is derived and must never be saved: collapsing to one
// column moves every node, so persisting it would replace the arrangement
// someone built on a big screen with a single stack.
{
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(BASE);
  await page.waitForSelector('#content .grid-stack-item', { timeout: 30000 });
  await page.waitForTimeout(1200);

  // Arrange something so there is a saved layout worth protecting.
  await page.locator('#content .widget-head').first().focus();
  for (let i = 0; i < 3; i++) await page.keyboard.press('ArrowRight');
  await page.waitForTimeout(900);
  const KEY = 'corral.dashboard.datacenter';
  const savedWide = await page.evaluate((k) => localStorage.getItem(k), KEY);
  const wideX = await page.evaluate(
    () => [...document.querySelectorAll('#content .grid-stack-item')].map((i) => i.getAttribute('gs-x')).join(','),
  );
  check(!!savedWide, 'dashboard-on-a-phone: the wide layout is saved first');

  await page.setViewportSize({ width: 420, height: 820 });
  await page.waitForTimeout(1500);
  const widths = await page.evaluate(
    () => [...new Set([...document.querySelectorAll('#content .grid-stack-item')]
      .map((i) => Math.round(i.getBoundingClientRect().width)))],
  );
  check(widths.length === 1, `dashboard-on-a-phone: every widget is full width (${widths.join(', ')}px)`);
  // Content height, not row height: a widget of four lines must not be a 240px box.
  const tallest = await page.evaluate(
    () => Math.max(...[...document.querySelectorAll('#content .grid-stack-item')]
      .map((i) => Math.round(i.getBoundingClientRect().height))),
  );
  check(tallest > 0 && tallest < 600, `dashboard-on-a-phone: widgets stack at content height (tallest ${tallest}px)`);

  check(
    await page.evaluate((k) => localStorage.getItem(k), KEY) === savedWide,
    'dashboard-on-a-phone: the visit does not overwrite the saved wide layout',
  );

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.waitForTimeout(1800);
  check(
    await page.evaluate(
      () => [...document.querySelectorAll('#content .grid-stack-item')].map((i) => i.getAttribute('gs-x')).join(','),
    ) === wideX,
    'dashboard-on-a-phone: the wide arrangement comes back on a wide screen',
  );
  await page.screenshot({ path: `${SHOTS}/dashboard-on-a-phone.png` });
}

// ── colour-scheme ─────────────────────────────────────────────────
// Light mode, with three states rather than two: the console follows the
// desktop by default, and an explicit choice overrides it. The accent belongs
// to whoever branded this corral, so no scheme may touch it.
{
  const bgOf = (pg) => pg.evaluate(
    () => getComputedStyle(document.documentElement).getPropertyValue('--bg').trim(),
  );
  const accentOf = (pg) => pg.evaluate(
    () => getComputedStyle(document.documentElement).getPropertyValue('--accent').trim(),
  );

  // A separate context per system preference: colorScheme is fixed per context.
  const darkCtx = await browser.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: 'dark' });
  const darkPage = await darkCtx.newPage();
  await darkPage.goto(BASE);
  await darkPage.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  const darkBg = await bgOf(darkPage);
  const darkAccent = await accentOf(darkPage);

  const lightCtx = await browser.newContext({ viewport: { width: 1280, height: 800 }, colorScheme: 'light' });
  const lightPage = await lightCtx.newPage();
  await lightPage.goto(BASE);
  await lightPage.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  const lightBg = await bgOf(lightPage);
  const lightAccent = await accentOf(lightPage);

  check(!!darkBg && !!lightBg && darkBg !== lightBg, `colour-scheme: the UI follows the system (${darkBg} vs ${lightBg})`);
  check(
    await lightPage.evaluate(() => getComputedStyle(document.documentElement).colorScheme) === 'light',
    'colour-scheme: color-scheme is declared, so native controls follow too',
  );
  // The accent comes from /api/theme. A scheme block that set it would outrank
  // the server and silently discard an operator's branding.
  check(
    darkAccent === lightAccent && !!darkAccent,
    `colour-scheme: the configured accent survives both schemes (${darkAccent})`,
  );

  // An explicit choice beats the system, and outlives a reload.
  await lightPage.click('#tree >> text=Settings');
  await lightPage.waitForSelector('[data-theme-mode]', { timeout: 15000 });
  check(
    (await lightPage.locator('[data-theme-mode]').count()) === 3,
    'colour-scheme: Settings offers System, Light and Dark',
  );
  await lightPage.click('[data-theme-mode="dark"]');
  await lightPage.waitForTimeout(600);
  check(await bgOf(lightPage) === darkBg, 'colour-scheme: Dark overrides a light desktop');
  await lightPage.reload();
  await lightPage.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  check(await bgOf(lightPage) === darkBg, 'colour-scheme: the choice survives a reload');

  // Back to following the system, which is the default state.
  await lightPage.click('#tree >> text=Settings');
  await lightPage.waitForSelector('[data-theme-mode]', { timeout: 15000 });
  await lightPage.click('[data-theme-mode="system"]');
  await lightPage.waitForTimeout(600);
  check(await bgOf(lightPage) === lightBg, 'colour-scheme: System hands the choice back to the desktop');
  await lightPage.screenshot({ path: `${SHOTS}/colour-scheme-light.png` });

  await darkCtx.close();
  await lightCtx.close();
}

// ── reachable (WCAG 2.4.1, 4.1.3, 2.3.3) ──────────────────────────
// Three things a console with a long sidebar, live toasts and animation owes
// anyone not using a mouse. All three were missing, and the first got worse
// when the tree rows became focusable.
{
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  await page.waitForTimeout(600);

  // Bypass blocks: the first stop must skip the fleet, not start it.
  await page.keyboard.press('Tab');
  await page.waitForTimeout(350);
  check(
    await page.evaluate(() => document.activeElement?.classList.contains('skip-link')) === true,
    'reachable: the first Tab lands on a skip link',
  );
  check(
    await page.evaluate(() => Math.round(document.querySelector('.skip-link').getBoundingClientRect().top)) >= 0,
    'reachable: the skip link shows itself once focused',
  );
  await page.keyboard.press('Enter');
  await page.waitForTimeout(300);
  // Focus has to move, not just the scroll, or the next Tab resumes in the tree.
  check(
    await page.evaluate(() => document.activeElement?.id) === 'content',
    'reachable: it moves focus into the content, not just the scroll',
  );

  // Status messages: a live region has to be present and empty before the
  // message arrives, or the change is read as ordinary content, if at all.
  const region = await page.evaluate(() => {
    const r = document.getElementById('toast-region');
    return r ? { role: r.getAttribute('role'), live: r.getAttribute('aria-live'), empty: r.children.length === 0 } : null;
  });
  check(!!region && region.live === 'polite' && region.empty,
    `reachable: a polite status region waits empty for toasts (${JSON.stringify(region)})`);

  await page.click('#btn-palette');
  await page.waitForSelector('#palette[open]', { timeout: 5000 });
  await page.locator('#palette-input').fill('reset layout');
  await page.waitForTimeout(400);
  await page.keyboard.press('Enter');
  await page.waitForTimeout(700);
  check(
    await page.evaluate(() => !!document.querySelector('#toast-region .toast')),
    'reachable: a real toast is announced from inside that region',
  );
  await page.screenshot({ path: `${SHOTS}/reachable.png` });
}

// Reduced motion is a separate context: the preference is fixed per context.
{
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, reducedMotion: 'reduce' });
  const pg = await ctx.newPage();
  await pg.goto(BASE);
  await pg.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  const durations = await pg.evaluate(() => [...document.querySelectorAll('.skip-link, #tree')]
    .map((el) => getComputedStyle(el).transitionDuration));
  check(
    durations.length > 0 && durations.every((d) => parseFloat(d) < 0.01),
    `reachable: motion is off when the system asks for it (${durations.join(', ')})`,
  );
  await ctx.close();
}

// ── toolbar-on-a-phone ────────────────────────────────────────────
// A guest's toolbar is eleven buttons. With their labels they wrapped onto
// three rows and filled most of the first screen before any detail appeared.
// The lifecycle verbs keep their place and give up their text; the rarer ones
// move behind "More", where they keep their words.
{
  await page.setViewportSize({ width: 420, height: 820 });
  await page.goto(BASE);
  await page.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
  await page.click('#btn-menu');
  await page.waitForTimeout(400);
  await page.locator('#tree [data-vm-key]').first().click();
  await page.waitForSelector('#content .toolbar .btn', { timeout: 15000 });
  await page.waitForTimeout(600);

  const bar = () => page.evaluate(() => {
    const tb = document.querySelector('#content .toolbar');
    const vis = [...tb.querySelectorAll('.btn')].filter((b) => b.offsetParent !== null);
    return {
      count: vis.length,
      rows: new Set(vis.map((b) => Math.round(b.getBoundingClientRect().top))).size,
      named: vis.every((b) => (b.getAttribute('aria-label') || '').length > 0),
      textShown: vis.filter((b) => b.querySelector('.btn-label')?.offsetParent !== null).length,
      minTarget: Math.min(...vis.map((b) => Math.round(b.getBoundingClientRect().height))),
    };
  });
  const phone = await bar();
  check(phone.rows === 1, `toolbar-on-a-phone: the toolbar is one row (${phone.rows})`);
  // Hiding the text would strip the accessible name if the aria-label went too.
  check(phone.named, 'toolbar-on-a-phone: every icon button still has a name');
  check(phone.textShown === 0, 'toolbar-on-a-phone: the labels are the thing that gives way');
  check(phone.minTarget >= 36, `toolbar-on-a-phone: the targets stay finger-sized (${phone.minTarget}px)`);

  // The actions that left the bar are still reachable, with their words.
  await page.click('[data-overflow]');
  await page.waitForTimeout(600);
  const items = await page.evaluate(() => [...document.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent.trim()));
  check(items.length >= 4, `toolbar-on-a-phone: More holds the rest (${items.join(', ')})`);
  check(items.some((t) => /Delete/.test(t)), 'toolbar-on-a-phone: the destructive action is in the menu, not a bare icon');
  await page.screenshot({ path: `${SHOTS}/toolbar-on-a-phone.png` });
  await page.keyboard.press('Escape');

  // Pause and Resume are one button in two states. Both use the play/pause
  // pair, so offering both put two identical triangles on an icon-only bar.
  // Pause one here rather than trust the demo fleet: earlier checks stop and
  // restart guests, so whichever one ships paused may not be paused by now.
  const fleetNow = await (await fetch(`${BASE}api/vms`)).json();
  const victim = fleetNow.find((v) => v.ready && v.backend === 'kubevirt');
  if (victim) {
    await fetch(`${BASE}api/vms/${victim.namespace}/${victim.name}/pause`, { method: 'POST' }).catch(() => {});
    await page.click('#btn-menu');
    await page.waitForTimeout(350);
    await page.locator('#tree .tree-item').filter({ hasText: victim.name }).first().click();
    // Wait for the page to show the pause, not merely for the server to report
    // it: the UI learns from its own 5s poll, so the toolbar renders from the
    // status it last saw. Watching the API here reads a state the page has not
    // caught up with yet, which is what made this check fail the first time.
    await page.waitForFunction(
      () => (document.querySelector('#content .page-head .pill')?.textContent || '').includes('Paused'),
      null,
      { timeout: 20000 },
    ).catch(() => {});
    const labels = await page.evaluate(() => [...document.querySelectorAll('#content .toolbar .btn')]
      .filter((b) => b.offsetParent !== null).map((b) => b.getAttribute('aria-label')));
    check(
      labels.includes('Resume') && !labels.includes('Pause'),
      `toolbar-on-a-phone: a paused guest offers Resume, not Pause (${labels.join(', ')})`,
    );

    await fetch(`${BASE}api/vms/${victim.namespace}/${victim.name}/unpause`, { method: 'POST' }).catch(() => {});
    await page.waitForFunction(async (n) => {
      const list = await (await fetch('/api/vms')).json();
      return !(list.find((v) => v.name === n)?.status || '').includes('Paused');
    }, victim.name, { timeout: 15000 }).catch(() => {});
    const back = await (await fetch(`${BASE}api/vms`)).json();
    check(
      !(back.find((v) => v.name === victim.name)?.status || '').includes('Paused'),
      'toolbar-on-a-phone: the paused guest is resumed so the suite stays re-runnable',
    );
  }

  // A wide screen keeps every action and every word.
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.waitForTimeout(900);
  const wide = await bar();
  check(
    wide.count > phone.count && wide.textShown === wide.count,
    `toolbar-on-a-phone: a wide screen keeps the labels and the buttons (${wide.count} shown, ${wide.textShown} labelled)`,
  );
  check(
    await page.evaluate(() => !document.querySelector('#content .toolbar [data-overflow]')?.offsetParent),
    'toolbar-on-a-phone: and needs no More button',
  );
}

// ── grid-row-actions ────────────────────────────────────
// Every action on a guest used to need a checkbox and the bulk bar, or opening
// the guest. Cockpit's machine list carries one button per row and swaps it
// between Run and Shut down with the guest's state. If this check fails, either
// the button stopped following the state, which offers an action the guest
// cannot do, or the row swallowed the keys of the controls inside it.
{
  await page.goto(BASE);
  await page.waitForSelector('#content .vm-check', { timeout: 30000 });
  const ROWS = '#content .grid-scroll table tbody tr';

  check(
    await page.locator(`${ROWS} .row-acts [data-row-action]`).count() > 0,
    'grid-row-actions: every row carries its own actions',
  );

  // The button has to agree with the status the same row shows. Read both off
  // the screen rather than naming a guest, because the demo fleet and the
  // column order both move.
  const pairs = await page.evaluate((sel) => {
    const head = [...document.querySelectorAll('#content .grid-scroll thead tr:first-child th')]
      .map((th) => th.textContent.trim().toLowerCase());
    const at = head.findIndex((h) => h.startsWith('status'));
    return [...document.querySelectorAll(sel)].map((tr) => ({
      status: at >= 0 ? (tr.children[at]?.textContent || '').trim() : '',
      action: tr.querySelector('.row-acts [data-row-action]')?.dataset.rowAction || '',
    }));
  }, ROWS);
  const wrong = pairs.filter(({ status, action }) => {
    if (!status || !action) return false;
    const up = /Running|Starting|Creating|Paused/i.test(status);
    return up ? action !== 'stop' : action !== 'start';
  });
  check(
    pairs.length > 0 && wrong.length === 0,
    `grid-row-actions: the button matches the row's state (${pairs.length} rows, ${wrong.length} disagree)`,
  );

  // A column of buttons must not claim a sort or a filter it cannot honour.
  check(
    await page.evaluate(() => {
      const head = [...document.querySelectorAll('#content .grid-scroll thead tr:first-child th')];
      const th = head.find((x) => x.textContent.trim().toLowerCase().startsWith('actions'));
      if (!th) return false;
      const index = head.indexOf(th);
      const filter = document.querySelectorAll('#content .grid-scroll thead tr.grid-filters th')[index];
      return !th.querySelector('.grid-sort') && !filter?.querySelector('input');
    }),
    'grid-row-actions: the actions column offers no sort and no filter',
  );

  // The overflow button reaches the same set the row's right-click offers.
  await page.click(`${ROWS}:first-child .row-acts [data-row-action="more"]`);
  await page.waitForTimeout(250);
  check(
    await page.locator('.context-menu').count() > 0,
    'grid-row-actions: the overflow button opens the action menu',
  );
  await page.keyboard.press('Escape');
  await page.waitForTimeout(150);

  // The row owns Enter and Space only when the focus is on the row itself. The
  // row opens a guest on both keys, so a checkbox that let the row see Space
  // would navigate away instead of selecting.
  const check1 = `${ROWS}:first-child .vm-check`;
  await page.focus(check1);
  await page.keyboard.press('Space');
  await page.waitForTimeout(250);
  check(
    await page.isChecked(check1) && await page.locator('#content .vm-check').count() > 0,
    'grid-row-actions: Space on a row checkbox selects it and does not open the guest',
  );
  // Put the selection back: the demo server outlives this run.
  await page.keyboard.press('Space');
  await page.waitForTimeout(200);
  check(!await page.isChecked(check1), 'grid-row-actions: and Space clears it again');

  await page.screenshot({ path: `${SHOTS}/grid-row-actions.png` });
}

check(pageErrors.length === 0, `no JS page errors (${pageErrors.join('; ').slice(0, 200)})`);

await browser.close();
if (failures > 0) {
  console.error(`\n${failures} smoke check(s) failed`);
  process.exit(1);
}
console.log('\nUI smoke: all checks passed');
