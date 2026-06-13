import { defineConfig, devices } from '@playwright/test';

// Run against any live corral web instance: CORRAL_URL=https://… npx playwright test
// Defaults to a local `corral web` on :8006 (pointed at the production cluster).
const baseURL = process.env.CORRAL_URL || 'http://localhost:8006';

export default defineConfig({
  testDir: '.',
  timeout: 300_000,
  expect: { timeout: 15_000 },
  retries: 0,
  workers: 1, // tests share one cluster — serialize
  use: {
    baseURL,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
});
