import { defineConfig, devices } from "@playwright/test";

/**
 * Playwright config for AC-5: all six views render within 5 seconds.
 *
 * Requires a running dashboard at BASE_URL (default http://localhost:3000).
 * Start the full dev stack first:
 *   docker compose up -d && go run ./dev/seed.go
 *   cd dashboard && npm run dev   (or use the docker-compose dashboard service)
 */
export default defineConfig({
  testDir: "./e2e",
  timeout: 15_000,        // per-test timeout
  expect: { timeout: 5_000 }, // AC-5: every assertion must resolve within 5 s
  fullyParallel: false,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: process.env.BASE_URL ?? "http://localhost:3000",
    trace: "on-first-retry",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
