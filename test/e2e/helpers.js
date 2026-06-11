// Shared helpers for Corral E2E Playwright tests.

import { expect } from '@playwright/test';

/**
 * Wait for the page to finish initial data load (API calls complete,
 * tree rendered, datacenter view visible).
 */
export async function waitForPageLoad(page) {
  // The page fires /api/vms and /api/nodes on load, then renders the tree
  await page.waitForSelector('#tree .tree-item', { timeout: 20000 });
  await page.waitForSelector('.page-head h1', { timeout: 10000 });
}

/**
 * Open the create VM dialog.
 */
export async function openCreateDialog(page) {
  await page.click('#btn-create');
  await expect(page.locator('#create-dialog')).toBeVisible({ timeout: 5000 });
}

/**
 * Close the create VM dialog.
 */
export async function closeCreateDialog(page) {
  await page.click('#btn-cancel');
  await expect(page.locator('#create-dialog')).not.toBeVisible({ timeout: 5000 });
}

/**
 * Click a tree item by its text label.
 */
export async function clickTreeItem(page, label) {
  const item = page.locator('.tree-item', { hasText: label }).first();
  await item.click();
  await page.waitForTimeout(500); // allow re-render
}

/**
 * Assert the datacenter view is showing (cards with VM/node counts).
 */
export async function expectDatacenterView(page) {
  await expect(page.locator('.page-head h1')).toContainText('Datacenter');
}

/**
 * Assert a node detail view is showing.
 */
export async function expectNodeView(page, nodeName) {
  await expect(page.locator('.page-head h1')).toContainText(nodeName);
}
