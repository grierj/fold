// QC-1 item 10: the rendered delete panel in a real browser against the real
// API. Opens Admin -> Users -> the account's Delete, and measures how long the
// reason review takes to render completely (every reason an <li data-reason-id>
// and the "Checking" line gone) and what the page holds afterwards: JS heap
// before and after a forced collection, DOM nodes, long-task time, and the page
// requests the walk made. Nothing is mocked: no route is intercepted, and the
// figures are discarded unless every page response came from the stack's own
// origin with status 200 and every body could be read.
//
// Environment: QC1_URL (http://127.0.0.1:PORT), QC1_WEB_DIR (the repo's web/,
// where `npm ci` installed @playwright/test), QC1_CHROMIUM (an installed
// Chromium, see browser.mjs), QC1_ADMIN_EMAIL, QC1_ADMIN_PASSWORD,
// QC1_TARGET_EMAIL, QC1_EXPECT (reasons the account holds), QC1_OUT (file for the
// JSON result). Exit 3 when the browser tooling is not usable, 4 when sign-in
// fails, 5 when the panel does not finish; nothing is written then, and the
// runner leaves the row missing (the run stays incomplete). browser-preflight.mjs
// proves the tooling with the same launch before anything is measured.
//
// Timing is the Node clock around Playwright calls: it includes the protocol
// round trips of each call (a millisecond or two), not a paint-timing API. The
// "ready" point is the one a person waits for before they can choose.
import { writeFileSync } from "node:fs";
import { launchOptions, resolvePlaywright } from "./browser.mjs";
import { totalBodyBytes, walkOrigin } from "./response-accounting.mjs";

const env = process.env;
for (const k of ["QC1_URL", "QC1_WEB_DIR", "QC1_ADMIN_EMAIL", "QC1_ADMIN_PASSWORD", "QC1_TARGET_EMAIL", "QC1_EXPECT", "QC1_OUT"]) {
  if (!env[k]) { process.stderr.write(`panel-measure: ${k} is required\n`); process.exit(2); }
}
const expected = Number(env.QC1_EXPECT);
const url = env.QC1_URL.replace(/\/$/, "");
const origin = new URL(url).origin;

let pw;
try {
  pw = resolvePlaywright(env.QC1_WEB_DIR);
} catch (e) {
  process.stderr.write(`panel-measure: @playwright/test does not resolve from ${env.QC1_WEB_DIR} (npm ci there): ${e.message}\n`);
  process.exit(3);
}
let browser;
try {
  browser = await pw.chromium.launch(launchOptions(env));
} catch (e) {
  process.stderr.write(`panel-measure: Chromium did not launch (${env.QC1_CHROMIUM ?? "Playwright's own download"}): ${e.message.split("\n")[0]}\n`);
  process.exit(3);
}

try {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await context.newPage();
  await page.addInitScript(() => {
    window.__longTaskMs = 0;
    window.__longTasks = 0;
    new PerformanceObserver((list) => {
      for (const e of list.getEntries()) { window.__longTaskMs += e.duration; window.__longTasks++; }
    }).observe({ type: "longtask", buffered: true });
  });
  // Sign in through the JSON API on the page's own cookie jar, as the e2e specs do.
  const csrf = async () => (await (await page.request.get(`${url}/api/v1/auth/csrf`)).json()).csrfToken;
  const login = await page.request.post(`${url}/api/v1/auth/login`, {
    headers: { "X-CSRF-Token": await csrf() },
    data: { email: env.QC1_ADMIN_EMAIL, password: env.QC1_ADMIN_PASSWORD },
  });
  if (!login.ok()) { process.stderr.write(`panel-measure: sign-in answered ${login.status()}\n`); process.exit(4); }

  const walk = { requests: 0, bad: 0, foreignOrigin: 0, bytes: null };
  const responseBodies = [];
  page.on("response", (r) => {
    if (!new URL(r.url()).pathname.endsWith("/erasure-reasons/page")) return;
    walk.requests++;
    if (r.status() !== 200) walk.bad++;
    if (new URL(r.url()).origin !== origin) walk.foreignOrigin++;
    responseBodies.push(r.body().then((body) => body.length).catch(() => null));
  });

  await page.goto(`${url}/admin?tab=users`);
  await page.getByRole("searchbox", { name: "Search by name or email" }).fill(env.QC1_TARGET_EMAIL);
  const row = page.getByRole("row").filter({ hasText: env.QC1_TARGET_EMAIL });
  await row.getByRole("button", { name: /^More for/ }).first().click();
  const items = page.locator('ul[aria-label="Contact-request reasons"] > li[data-reason-id]');

  const started = performance.now();
  await row.getByRole("menuitem", { name: "Delete" }).click();
  await page.waitForFunction(
    (n) => document.querySelectorAll('ul[aria-label="Contact-request reasons"] > li[data-reason-id]').length >= n,
    expected,
    { timeout: 900000, polling: "raf" },
  );
  await row.getByText("Checking their contact-request reasons…").waitFor({ state: "detached", timeout: 900000 });
  const renderReadyMs = Math.round(performance.now() - started);
  const itemsRendered = await items.count();

  const cdp = await context.newCDPSession(page);
  await cdp.send("Performance.enable");
  const metric = async (name) => (await cdp.send("Performance.getMetrics")).metrics.find((m) => m.name === name)?.value ?? null;
  const heapUsedBytes = await metric("JSHeapUsedSize");
  await cdp.send("HeapProfiler.enable");
  await cdp.send("HeapProfiler.collectGarbage");
  const heapAfterGcBytes = await metric("JSHeapUsedSize");
  const domNodes = await metric("Nodes");
  const long = await page.evaluate(() => ({ ms: window.__longTaskMs, count: window.__longTasks }));

  const bodySizes = await Promise.all(responseBodies);
  walk.bytes = totalBodyBytes(bodySizes);
  const apiOrigin = walkOrigin(walk, walk.bytes, bodySizes.length);
  writeFileSync(env.QC1_OUT, JSON.stringify({
    apiOrigin,
    browser: `chromium ${browser.version()}`,
    renderReadyMs, itemsRendered, expected,
    heapUsedBytes, heapAfterGcBytes, domNodes,
    longTaskMs: Math.round(long.ms), longTaskCount: long.count,
    pageRequests: walk.requests, pageResponseBytes: walk.bytes, pageResponseBytesKind: "decoded response body bytes",
    note: "Node clock around Playwright calls, headless Chromium, 1280x900, the app's own embedded bundle; ready = every reason rendered and the Checking line gone",
  }) + "\n");
} catch (e) {
  process.stderr.write(`panel-measure: ${e && e.message ? e.message : e}\n`);
  process.exit(5);
} finally {
  await browser.close();
}
