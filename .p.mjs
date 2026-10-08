import { chromium } from 'playwright';
const b = await chromium.launch({ executablePath: '/opt/pw-browsers/chromium-1194/chrome-linux/chrome' });
const p = await b.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'light', deviceScaleFactor: 2 });
p.on('pageerror', e=>console.log('PAGEERROR:',e.message));
await p.goto('http://127.0.0.1:8899/');
await p.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
await p.waitForTimeout(1000);

// palette in light
await p.keyboard.press('Control+k');
await p.waitForSelector('#palette[open]', { timeout: 5000 });
await p.waitForTimeout(600);
await p.screenshot({ path: 'ui-smoke-screenshots/tour/theme-light-palette.png' });
await p.keyboard.press('Escape');

// create dialog in light
await p.click('#btn-create');
await p.waitForTimeout(900);
await p.screenshot({ path: 'ui-smoke-screenshots/tour/theme-light-dialog.png' });
await p.keyboard.press('Escape');
await p.waitForTimeout(400);

// settings: the new control, and force dark on a light system
await p.click('#tree >> text=Settings');
await p.waitForSelector('[data-theme-mode]', { timeout: 15000 });
await p.waitForTimeout(600);
await p.screenshot({ path: 'ui-smoke-screenshots/tour/theme-settings.png' });
console.log('modes offered:', await p.locator('[data-theme-mode]').allTextContents());
await p.click('[data-theme-mode="dark"]');
await p.waitForTimeout(700);
console.log('forced dark on a light system:', await p.evaluate(()=>({
  attr: document.documentElement.getAttribute('data-theme'),
  bg: getComputedStyle(document.documentElement).getPropertyValue('--bg').trim(),
  stored: localStorage.getItem('corral.theme'),
})));
await p.reload();
await p.waitForSelector('#tree [data-vm-key]', { timeout: 30000 });
await p.waitForTimeout(800);
console.log('after reload still dark:', await p.evaluate(()=>getComputedStyle(document.documentElement).getPropertyValue('--bg').trim()));
await p.screenshot({ path: 'ui-smoke-screenshots/tour/theme-forced-dark.png' });
await b.close();
