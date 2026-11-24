import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  workers: 1,
  timeout: 180_000,
  expect: { timeout: 12_000 },
  reporter: [["list", { printSteps: true }]],
  outputDir: "../web/.next/e2e-results",
  use: { browserName: "chromium", timezoneId: "UTC", actionTimeout: 12_000, navigationTimeout: 30_000 },
});
