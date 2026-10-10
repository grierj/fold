import { defineConfig, devices } from "@playwright/test";

// Browser tests (make e2e): Chromium only, against the real fold binary and
// a fresh Postgres database (e2e/server.sh). The "seed" project runs the
// setup wizard and creates the people and groups the specs use; the specs
// run one at a time because they share that data.
const port = process.env.E2E_PORT ?? "18090";

export default defineConfig({
  testDir: "e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  reporter: "list",
  timeout: 15_000,
  expect: { timeout: 5_000 },
  use: { baseURL: `http://127.0.0.1:${port}`, trace: "retain-on-failure" },
  projects: [
    { name: "seed", testMatch: /seed\.setup\.ts/ },
    { name: "chromium", use: { ...devices["Desktop Chrome"] }, dependencies: ["seed"] },
  ],
  webServer: {
    command: "bash e2e/server.sh",
    url: `http://127.0.0.1:${port}/healthz`,
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: "ignore",
    stderr: "pipe",
  },
});
