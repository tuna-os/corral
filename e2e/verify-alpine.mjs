import { chromium } from '@playwright/test';

const browser = await chromium.launch();
const page = await browser.newPage();
const errors = [];
page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
page.on('console', (msg) => { if (msg.type() === 'error') errors.push('console.error: ' + msg.text()); });

await page.goto('http://127.0.0.1:18006/', { waitUntil: 'networkidle' });

// Alpine loaded and initialized?
const alpineLoaded = await page.evaluate(() => typeof window.Alpine !== 'undefined');
console.log('Alpine loaded:', alpineLoaded);

const panelExists = await page.locator('#task-panel').count();
console.log('#task-panel exists:', panelExists);

// Alpine should have added x-data-driven reactivity — check the collapsed class is present initially
const initiallyCollapsed = await page.locator('#task-panel').evaluate((el) => el.classList.contains('collapsed'));
console.log('initially collapsed:', initiallyCollapsed);

// "No tasks yet" row should render since /api/tasklog returns []
const noTasksText = await page.locator('#task-rows').textContent();
console.log('task-rows text:', JSON.stringify(noTasksText));

// Click the header, verify the collapsed class toggles off
await page.locator('#task-panel-head').click();
await page.waitForTimeout(100);
const afterClickCollapsed = await page.locator('#task-panel').evaluate((el) => el.classList.contains('collapsed'));
console.log('collapsed after click:', afterClickCollapsed);

const chevronText = await page.locator('#task-panel-chevron').textContent();
console.log('chevron after click:', chevronText);

console.log('JS errors:', errors.length ? errors : 'none');

await browser.close();
process.exit(errors.length ? 1 : 0);
