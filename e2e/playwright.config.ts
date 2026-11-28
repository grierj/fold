import { defineConfig } from "@playwright/test";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

// The gates run against the real binary in development mode, on a fresh
// data folder, with mail kept in the outbox.
const port = 18181;
const dataDir = process.env.E2E_DATA_DIR ?? mkdtempSync(join(tmpdir(), "fold-e2e-"));
process.env.E2E_DATA_DIR = dataDir;
export const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.ts/,
  workers: 1,
  fullyParallel: false,
  timeout: 120_000,
  reporter: [["list"]],
  use: {
    baseURL,
    // Every browser is in Boise, the pilot church's zone, unless a test says
    // otherwise; a signed-in browser tells the server its zone (DEC-58).
    timezoneId: "America/Boise",
    launchOptions: process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {},
  },
  webServer: {
    command: "../bin/fold serve",
    url: `${baseURL}/healthz`,
    reuseExistingServer: false,
    env: { DEV: "1", DATA_DIR: dataDir, LISTEN: `127.0.0.1:${port}`, SITE_URL: baseURL },
    stdout: "ignore",
    stderr: "pipe",
  },
});
