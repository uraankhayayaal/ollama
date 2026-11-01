import { defineConfig } from '@playwright/test';

export default defineConfig({
  use: {
    baseURL: 'http://127.0.0.1:8090',
    trace: 'on-first-retry',
  },
  timeout: 15000,
  retries: 0,
  fullyParallel: true,
});
