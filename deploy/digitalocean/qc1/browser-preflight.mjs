// Proves, before anything is measured or drilled, that the browser each consumer
// will use can be found and driven. There are two consumers, and each is checked
// through ITS OWN entry point and ITS OWN launch options:
//   qc1      the QC-1 panel measurement: chromium from @playwright/test, launched
//            with launchOptions(env) (qc1/browser-launch.cjs);
//   restore  the restore drill's recovery browser proof (testdata/
//            restore-browser.cjs): chromium from the `playwright` package,
//            launched with restoreLaunchOptions(env) (the same executable and
//            sandbox rule when QC1_CHROMIUM names a Chromium, plain
//            { headless: true } otherwise).
// QC1_PREFLIGHT_FOR is a comma list of them (default: both).
//
// For each: Playwright resolves from web/node_modules, the locked packages agree,
// the node engine range is met, the Chromium launches, and the calls the consumer
// relies on work (DOM queries, the long-task observer, CDP Performance.getMetrics
// heap and node counts, a forced collection, and a page load over a localhost
// HTTP origin with a Secure cookie, which the restore proof depends on).
//
// Environment: QC1_WEB_DIR (required); QC1_CHROMIUM and QC1_NO_SANDBOX as in
// browser-launch.cjs; QC1_CHROMIUM_MAJOR_TOLERANCE (default 4): the most the
// installed Chromium's major version may differ from the one this Playwright
// bundles (a judgement, not a guarantee; the smoke checks are the proof);
// QC1_REQUIRE_NVMRC_NODE=1 makes a node major different from .nvmrc fatal (the
// default records it: Playwright asks only for node >= its engines range, and
// the Alpine runner's nodejs may be older than the repository's Node 24; no
// application build or test runs under it).
// Prints one JSON object on stdout. Exit 0 ok; 2 usage; 3 tooling absent; 6 a
// smoke check failed; 7 Chromium major further from the bundled one than the
// tolerance; 8 node major differs from .nvmrc under QC1_REQUIRE_NVMRC_NODE=1.
import { readFileSync, existsSync } from "node:fs";
import http from "node:http";
import path from "node:path";
import { launchOptions, restoreLaunchOptions, nodeEngineProblem, chromiumMajorDelta, majorOf, resolvePlaywright } from "./browser.mjs";

const env = process.env;
if (!env.QC1_WEB_DIR) { process.stderr.write("browser-preflight: QC1_WEB_DIR is required\n"); process.exit(2); }
const consumers = (env.QC1_PREFLIGHT_FOR ?? "qc1,restore").split(",").map((s) => s.trim()).filter(Boolean);
for (const c of consumers) if (c !== "qc1" && c !== "restore") { process.stderr.write(`browser-preflight: unknown consumer ${c}\n`); process.exit(2); }
const tolerance = Number(env.QC1_CHROMIUM_MAJOR_TOLERANCE ?? 4);
const sandboxOff = env.QC1_NO_SANDBOX === "1" && env.FOLD_DRILL_ISOLATED_DAEMON === "1";
const result = { ok: false, consumers, webDir: env.QC1_WEB_DIR, node: process.versions.node, executablePath: env.QC1_CHROMIUM ?? null, sandbox: !sandboxOff };

let pw;
try {
  pw = resolvePlaywright(env.QC1_WEB_DIR);
} catch (e) {
  process.stderr.write(`browser-preflight: Playwright does not resolve from ${env.QC1_WEB_DIR} (run npm ci there): ${e.message.split("\n")[0]}\n`);
  process.exit(3);
}
Object.assign(result, { playwright: pw.playwrightVersion, playwrightPackage: pw.playwrightPackageVersion, playwrightCore: pw.coreVersion, bundledChromium: pw.bundledChromiumVersion, resolvedFrom: pw.corePath });
if (new Set([pw.playwrightVersion, pw.playwrightPackageVersion, pw.coreVersion]).size !== 1) {
  process.stderr.write(`browser-preflight: @playwright/test ${pw.playwrightVersion}, playwright ${pw.playwrightPackageVersion} and playwright-core ${pw.coreVersion} differ\n`);
  process.exit(6);
}
const engine = nodeEngineProblem(pw.nodeEngine, process.versions.node);
if (engine) { process.stderr.write(`browser-preflight: ${engine}\n`); process.exit(3); }

// Node against the repository's .nvmrc (found next to web/): recorded, and fatal only when asked.
const nvmrcPath = path.join(path.dirname(path.resolve(env.QC1_WEB_DIR)), ".nvmrc");
const nvmrc = existsSync(nvmrcPath) ? readFileSync(nvmrcPath, "utf8").trim() : null;
const nvmrcMajor = nvmrc === null ? null : majorOf(`${nvmrc.replace(/^v/, "")}.`);
result.nvmrc = { value: nvmrc, major: nvmrcMajor, nodeMajor: majorOf(`${process.versions.node}.`), matches: nvmrcMajor === null ? null : nvmrcMajor === majorOf(`${process.versions.node}.`) };
if (env.QC1_REQUIRE_NVMRC_NODE === "1" && result.nvmrc.matches !== true) {
  process.stdout.write(JSON.stringify(result) + "\n");
  process.stderr.write(`browser-preflight: node ${process.versions.node} is not .nvmrc ${nvmrc}\n`);
  process.exit(8);
}

// A localhost HTTP origin that sets a Secure HttpOnly cookie, as the recovery proof's origin does.
const server = http.createServer((req, res) => {
  res.setHeader("Set-Cookie", "fold-session=preflight; Secure; HttpOnly; SameSite=Lax; Path=/");
  res.setHeader("Content-Type", "text/html");
  res.end('<ul aria-label="Contact-request reasons"><li data-reason-id="1">a</li><li data-reason-id="2">b</li></ul>');
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const origin = `http://localhost:${server.address().port}`;

const specs = {
  qc1: { via: "@playwright/test", engine: pw.chromium, options: launchOptions(env) },
  restore: { via: "playwright", engine: pw.chromiumViaPlaywright, options: restoreLaunchOptions(env) },
};
result.smoke = {};
let exitCode = 0;
for (const consumer of consumers) {
  const { via, engine: chromium, options } = specs[consumer];
  const entry = { via, launchOptions: options };
  result.smoke[consumer] = entry;
  let browser;
  try {
    browser = await chromium.launch(options);
  } catch (e) {
    process.stderr.write(`browser-preflight: Chromium did not launch for ${consumer} (${options.executablePath ?? "Playwright's own download"}): ${e.message.split("\n")[0]}\n`);
    exitCode = 3; break;
  }
  try {
    entry.chromium = browser.version();
    entry.majorDelta = chromiumMajorDelta(entry.chromium, pw.bundledChromiumVersion);
    const context = await browser.newContext();
    const page = await context.newPage();
    await page.goto(origin);
    const dom = await page.evaluate(() => ({
      items: document.querySelectorAll('ul[aria-label="Contact-request reasons"] > li[data-reason-id]').length,
      longtask: PerformanceObserver.supportedEntryTypes.includes("longtask"),
    }));
    const cookies = await context.cookies(origin);
    const cdp = await context.newCDPSession(page);
    await cdp.send("Performance.enable");
    const metrics = (await cdp.send("Performance.getMetrics")).metrics;
    const heap = metrics.find((m) => m.name === "JSHeapUsedSize")?.value ?? null;
    const nodes = metrics.find((m) => m.name === "Nodes")?.value ?? null;
    await cdp.send("HeapProfiler.enable");
    await cdp.send("HeapProfiler.collectGarbage");
    Object.assign(entry, { domItems: dom.items, longTaskObserver: dom.longtask, heapUsedBytes: heap, domNodes: nodes,
      secureLocalhostCookie: cookies.some((c) => c.name === "fold-session" && c.secure && c.httpOnly) });
    const failed = [];
    if (dom.items !== 2) failed.push("DOM query");
    if (consumer === "qc1" && !dom.longtask) failed.push("longtask observer");
    if (!(heap > 0)) failed.push("CDP JSHeapUsedSize");
    if (!(nodes > 0)) failed.push("CDP Nodes");
    if (consumer === "restore" && !entry.secureLocalhostCookie) failed.push("Secure cookie on localhost");
    if (failed.length) { entry.failed = failed; process.stderr.write(`browser-preflight: smoke checks failed for ${consumer}: ${failed.join(", ")}\n`); exitCode = 6; break; }
    if (entry.majorDelta !== null && entry.majorDelta > tolerance) {
      process.stderr.write(`browser-preflight: Chromium ${entry.chromium} is ${entry.majorDelta} major versions from the ${pw.bundledChromiumVersion} this Playwright bundles (tolerance ${tolerance})\n`);
      exitCode = 7; break;
    }
  } catch (e) {
    process.stderr.write(`browser-preflight: ${consumer}: ${e.message.split("\n")[0]}\n`);
    exitCode = 6; break;
  } finally {
    await browser.close();
  }
}
server.close();
result.tolerance = tolerance;
result.chromium = Object.values(result.smoke).map((e) => e.chromium).find(Boolean) ?? null;
result.majorDelta = Object.values(result.smoke).map((e) => e.majorDelta).find((d) => d !== undefined) ?? null;
result.ok = exitCode === 0;
process.stdout.write(JSON.stringify(result) + "\n");
process.exit(exitCode);
