// Corral web UI — Playwright E2E tests against live cluster.
import { test, expect } from '@playwright/test';
import { waitForPageLoad, openCreateDialog, closeCreateDialog, clickTreeItem } from './helpers.js';

test.describe('Datacenter page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await waitForPageLoad(page);
  });

  test('loads and shows tree sidebar', async ({ page }) => {
    const tree = page.locator('#tree');
    await expect(tree).toBeVisible();
    const items = tree.locator('.tree-item');
    await expect(items.first()).toBeVisible();
    expect(await items.count()).toBeGreaterThan(1);
    await expect(items.first()).toContainText('Datacenter');
  });

  test('shows datacenter view with cards', async ({ page }) => {
    expect(await page.locator('.card').count()).toBeGreaterThanOrEqual(1);
    await expect(page.locator('.page-head h1')).toContainText('Datacenter');
  });

  test('shows VM table or empty-state message', async ({ page }) => {
    const hasTable = (await page.locator('table').count()) > 0;
    const hasMsg = (await page.locator('.console-msg').count()) > 0;
    expect(hasTable || hasMsg).toBeTruthy();
  });

  test('image library section exists', async ({ page }) => {
    await expect(page.locator('h2.section')).toContainText('Image library');
    await expect(page.locator('#dc-images')).toBeVisible();
  });

  test('header shows brand and create button', async ({ page }) => {
    await expect(page.locator('.brand')).toContainText('Corral');
    await expect(page.locator('#btn-create')).toBeVisible();
  });
});

test.describe('Create dialog', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await waitForPageLoad(page);
  });

  test('opens and closes', async ({ page }) => {
    await openCreateDialog(page);
    await expect(page.locator('#create-dialog h2')).toContainText('Create');
    await closeCreateDialog(page);
    await expect(page.locator('#create-dialog')).not.toBeVisible();
  });

  test('has source type selector', async ({ page }) => {
    await openCreateDialog(page);
    await expect(page.locator('[name=sourceType]')).toBeVisible();
    const count = await page.locator('[name=sourceType] option').count();
    expect(count).toBeGreaterThanOrEqual(3);
    await closeCreateDialog(page);
  });

  test('bootc source shows SSH key field', async ({ page }) => {
    await openCreateDialog(page);
    const bootcOpt = page.locator('[name=sourceType] option[value=bootc]');
    if (await bootcOpt.count() === 0) { await closeCreateDialog(page); return; }
    await page.selectOption('[name=sourceType]', 'bootc');
    await expect(page.locator('#sshkey-field')).toBeVisible();
    await closeCreateDialog(page);
  });

  test('required source types are present', async ({ page }) => {
    await openCreateDialog(page);
    const values = await page.locator('[name=sourceType] option').evaluateAll(
      (els) => els.map((e) => e.value)
    );
    expect(values).toContain('containerDisk');
    expect(values).toContain('iso');
    expect(values).toContain('pvc');
    await closeCreateDialog(page);
  });

  test('name field is present and required', async ({ page }) => {
    await openCreateDialog(page);
    const nameInput = page.locator('[name=name]');
    await expect(nameInput).toBeVisible();
    expect(await nameInput.getAttribute('required')).not.toBeNull();
    await closeCreateDialog(page);
  });
});

test.describe('Tree navigation', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await waitForPageLoad(page);
  });

  test('clicking a node shows node detail view', async ({ page }) => {
    const nodeItems = page.locator('.tree-item.lvl-1');
    if (await nodeItems.count() === 0) return; // no nodes
    await nodeItems.first().click();
    await page.waitForTimeout(500);
    expect(await page.locator('.page-head h1').textContent()).toBeTruthy();
  });
});

test.describe('Extensions page', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await waitForPageLoad(page);
  });

  test('navigating to Extensions shows plugin list', async ({ page }) => {
    // The Extensions tree item may not exist in older deployments
    const extItem = page.locator('.tree-item', { hasText: 'Extension' });
    if (await extItem.count() === 0) return;
    await extItem.first().click();
    await page.waitForTimeout(500);
    await expect(page.locator('.page-head h1')).toContainText('Extension');
  });
});

test.describe('Mobile layout', () => {
  test('drawer toggles open and closed', async ({ page }) => {
    await page.goto('/');
    await waitForPageLoad(page);
    await expect(page.locator('#btn-menu')).toBeVisible();
    const tree = page.locator('#tree');
    await expect(tree).not.toHaveClass(/open/);
    await page.locator('#btn-menu').click();
    await page.waitForTimeout(300);
    await expect(tree).toHaveClass(/open/);
    await page.locator('#btn-menu').click();
    await page.waitForTimeout(300);
    await expect(tree).not.toHaveClass(/open/);
  });

  test('create VM button works on mobile', async ({ page }) => {
    await page.goto('/');
    await waitForPageLoad(page);
    await openCreateDialog(page);
    await expect(page.locator('#create-dialog h2')).toContainText('Create');
    await closeCreateDialog(page);
  });
});
