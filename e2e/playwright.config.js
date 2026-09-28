const { defineConfig } = require('@playwright/test');

// global-setup builds the binary once; every spec file then boots its own
// app (lib/app.js useApp) against a throwaway HOME dir with an in-process
// IMAP/SMTP test server. workers stays at 1 so the boots are sequential.
module.exports = defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: [['list']],
  globalSetup: require.resolve('./lib/global-setup'),
  globalTeardown: require.resolve('./lib/global-teardown'),
  use: {
    headless: true,
    ignoreHTTPSErrors: true,
  },
});
