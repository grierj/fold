// QC-1 item 10: the rendered deletion panel in a real browser against the real API. Opens Admin -> Users -> the account's Delete and
// measures, for a dense history seeded by the harness (qc1-measure.sh):
//   - readiness: max(the whole review read with the displayed total equal to the seeded count and the Checking line gone, the first
//     50-reason page rendered and its first radio accepting a real click within 200 ms), see panel-core.mjs;
//   - 30 real clicks (first page while the walk runs, mid-list, last page, after the walk) and the navigation controls, timed in the page;
//   - heap before and after a forced collection, DOM nodes and long tasks after the walk and after the interactions;
//   - the page requests the walk made, every one answered 200 from the stack's own origin with its decoded body read;
//   - whether the page is a secure context, whether crypto.subtle exists and so which digest path ran (inferred: the product exposes none);
//   - the changed-reason path (when QC1_CHANGE_CMD names a script): every reason chosen with the panel's own handler, one reason changed
//     behind the page, Delete pressed (the real server refuses with reason_review_changed, nothing is deleted), the review read again, the
//     DELETE body's size and the time from the press to the request, and "Next changed reason".
// Nothing is mocked: no route is intercepted. The figures are discarded unless every page response came from the stack's own origin with
// status 200 and every body could be read (apiOrigin is "real" only then).
//
// Environment: QC1_URL (http://127.0.0.1:PORT), QC1_WEB_DIR (the repo's web/, where `npm ci` installed @playwright/test), QC1_CHROMIUM (an
// installed Chromium, see browser.mjs), QC1_ADMIN_EMAIL, QC1_ADMIN_PASSWORD, QC1_TARGET_EMAIL, QC1_EXPECT (reasons the account holds), QC1_OUT
// (file for the JSON result). Optional: QC1_CHANGE_CMD (an executable called with one request id: it changes that reason behind the page),
// QC1_NO_SUBTLE=1 (remove crypto.subtle before the page loads, as a plain-http LAN address lacks it, so the bundled SHA-256 runs), QC1_CLICKS
// (default 30), QC1_PHASE_CMD (an executable called with a phase name that prints the WAL position and audit_logs figures as JSON: open, walk-end,
// interactions-end, press-start, press-end). Exit 3 when the browser tooling is not usable, 4 when sign-in fails, 5 when the run breaks before any
// measurement; nothing is written then, and the runner leaves the row missing (the run stays incomplete). Exit 6 when a PHASE failed after the run
// started: the file is written with `failed` naming the phase and every figure measured before it (including any budget already crossed); the runner
// records it as a PARTIAL row, which the judge keeps for its budget findings and calls invalid as incomplete. browser-preflight.mjs proves the tooling with the same launch.
//
// Timing is the Node clock around Playwright calls for readiness, and the page's own clock for clicks and navigation (panel-driver.mjs).
import { writeFileSync } from "node:fs";
import { launchOptions, resolvePlaywright } from "./browser.mjs";
import { totalBodyBytes, walkOrigin } from "./response-accounting.mjs";
import { measurePanel, CLICKS_DEFAULT } from "./panel-core.mjs";
import { playwrightDriver, INIT_SCRIPT, NO_SUBTLE_SCRIPT } from "./panel-driver.mjs";

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
  await page.addInitScript(INIT_SCRIPT);
  if (env.QC1_NO_SUBTLE === "1") await page.addInitScript(NO_SUBTLE_SCRIPT);
  // Sign in through the JSON API on the page's own cookie jar, as the e2e specs do.
  const csrf = async () => (await (await page.request.get(`${url}/api/v1/auth/csrf`)).json()).csrfToken;
  const login = await page.request.post(`${url}/api/v1/auth/login`, {
    headers: { "X-CSRF-Token": await csrf() },
    data: { email: env.QC1_ADMIN_EMAIL, password: env.QC1_ADMIN_PASSWORD },
  });
  if (!login.ok()) { process.stderr.write(`panel-measure: sign-in answered ${login.status()}\n`); process.exit(4); }

  // What the first walk's responses add up to; read when the walk has ended (a later walk, after the refused press, is not part of it).
  const walk = { requests: 0, bad: 0, foreignOrigin: 0, bytes: null };
  const bodies = [];
  let counting = true;
  let allPageResponses = 0; // every review page response this page receives, from the start and never reset: a fresh traversal shows as this growing
  page.on("response", (r) => {
    if (!new URL(r.url()).pathname.endsWith("/erasure-reasons/page")) return;
    allPageResponses++;
    if (!counting) return;
    walk.requests++;
    if (r.status() !== 200) walk.bad++;
    if (new URL(r.url()).origin !== origin) walk.foreignOrigin++;
    // Per page: the decoded body size, the request time (Playwright's own timing) and JSON.parse of that body alone in Node.
    bodies.push(
      r.body().then(
        (body) => {
          const t = r.request().timing();
          const p0 = performance.now();
          let ok = true;
          try { JSON.parse(body.toString("utf8")); } catch { ok = false; }
          const nodeParseMs = ok ? Math.round((performance.now() - p0) * 100) / 100 : null;
          const requestMs = t && t.responseEnd >= 0 && t.startTime >= 0 ? Math.round((t.responseEnd) * 100) / 100 : null;
          return { bytes: body.length, requestMs, nodeParseMs };
        },
        () => ({ bytes: null, requestMs: null, nodeParseMs: null }),
      ),
    );
  });
  const walkStats = async () => {
    counting = false;
    const list = await Promise.all(bodies);
    const sizes = list.map((p) => p.bytes);
    walk.bytes = totalBodyBytes(sizes);
    return { pageRequests: walk.requests, pageResponseBytes: walk.bytes, apiOrigin: walkOrigin(walk, walk.bytes, sizes.length), pageList: list };
  };

  const driver = playwrightDriver({ page, context, url, targetEmail: env.QC1_TARGET_EMAIL, expected, changeCmd: env.QC1_CHANGE_CMD ?? "", phaseCmd: env.QC1_PHASE_CMD ?? "", walk: walkStats, pageResponses: () => allPageResponses });
  const result = await measurePanel(driver, {
    expected,
    now: () => performance.now(),
    sleep: (ms) => new Promise((r) => setTimeout(r, ms)),
    clicksTotal: Number(env.QC1_CLICKS ?? CLICKS_DEFAULT),
  });
  // A run that failed part-way is WRITTEN anyway, with `failed` naming the phase: the phases that finished keep their figures (and any budget already
  // crossed). The runner records such a file as a partial row and the judge keeps its budget findings; the exit status is 6 so it is never mistaken for a
  // clean run.
  writeFileSync(env.QC1_OUT, JSON.stringify({
    ...result,
    browser: `chromium ${browser.version()}`,
    noSubtle: env.QC1_NO_SUBTLE === "1",
    note: "readiness and walk times are the Node clock around Playwright calls; click and navigation latencies are the page's own clock from the pointerdown to the frame after the commit; headless Chromium, 1280x900, the app's own embedded bundle, a runner where the browser shares the machine with the stack",
  }) + "\n");
  if (result.failed) {
    process.stderr.write(`panel-measure: stopped in ${result.failed.phase}: ${result.failed.error}\n`);
    process.exitCode = 6;
  }
} catch (e) {
  process.stderr.write(`panel-measure: ${e && e.message ? e.message : e}\n`);
  process.exit(5);
} finally {
  await browser.close();
}
