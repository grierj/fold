import { defineConfig, devices } from "@playwright/test";

// Browser tests (make e2e): Chromium only, against the real fold binary and
// a fresh Postgres database (e2e/server.sh). The "seed" project runs the
// setup wizard and creates the people and groups the specs use; the specs
// run one at a time because they share that data. e2e/run.sh splits them
// across parallel processes (--shard), each with its own server, database
// and seed, so a shard is exactly this run on a slice of the specs.
const port = process.env.E2E_PORT ?? "18090";

export default defineConfig({
  testDir: "e2e",
  // One folder per shard, so parallel shards never clear each other's traces.
  outputDir: process.env.E2E_OUTPUT_DIR ?? "test-results",
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
