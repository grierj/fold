// The Playwright half of the QC-1 panel measurement: it implements, against the real deletion panel in Chromium, the driver that
// panel-core.mjs measures through. Selectors are the ones web/e2e/admin-delete-user.spec.ts uses against the same panel (roles, button
// names, "Reasons N to M of ..." pager line, li[data-reason-id], [data-reason-heading]).
//
// Timing happens INSIDE the page, from the pointerdown's own timestamp to the frame after the commit it asked for, so a latency never
// contains a Playwright round trip. A click is a real pointer click (locator.check / locator.click); the page learns of it from the
// capture-phase pointerdown. The interval ends when the page shows what the control promises (the reason is chosen, the pager line or the
// focused heading is the expected one) and two animation frames have followed.
import { spawnSync } from "node:child_process";

export const ROWS = 'ul[aria-label="Contact-request reasons"] > li[data-reason-id]';
export const NAV = 'nav[aria-label="Reasons pages"] p';
export const CHECKING = "Checking their contact-request reasons…";

// Installed before any page script. window.__qc.arm(spec) is called right before a click; spec is {choose: rowIndex} or {line, heading}.
export const INIT_SCRIPT = `(() => {
  const qc = (window.__qc = { t0: null, spec: null, result: null, longTaskMs: 0, longTasks: 0 });
  const rows = () => document.querySelectorAll(${JSON.stringify(ROWS)});
  const met = () => {
    const s = qc.spec;
    if (!s) return false;
    if (s.choose !== undefined) {
      const li = rows()[s.choose];
      return !!li && !li.textContent.includes("Not chosen yet") && !li.textContent.includes("Changed since you looked");
    }
    const line = document.querySelector(${JSON.stringify(NAV)})?.textContent ?? null;
    if (s.line !== undefined && line !== s.line) return false;
    if (s.heading !== undefined && document.activeElement?.getAttribute("data-reason-heading") !== String(s.heading)) return false;
    return true;
  };
  const check = () => {
    if (!qc.spec || qc.t0 === null || qc.result !== null || !met()) return;
    qc.spec = null;
    requestAnimationFrame(() => requestAnimationFrame(() => { qc.result = performance.now() - qc.t0; }));
  };
  qc.arm = (spec) => { qc.spec = spec; qc.t0 = null; qc.result = null; };
  document.addEventListener("pointerdown", (e) => { if (qc.spec && qc.t0 === null) qc.t0 = e.timeStamp; }, true);
  document.addEventListener("focusin", check, true);
  new MutationObserver(check).observe(document, { subtree: true, childList: true, characterData: true, attributes: true });
  const tick = () => { check(); requestAnimationFrame(tick); };
  requestAnimationFrame(tick);
  // F7: the page's own read and parse of every review page response, and every crypto.subtle.digest call (count, bytes, time), so the
  // digest path that actually ran is observed, not inferred. The wrappers add one timestamp pair around the call and change no argument or result.
  qc.pages = []; qc.last = null;
  qc.digest = { calls: 0, bytes: 0, ms: 0 };
  // The product reads a review page as Response.text() and then JSON.parse(text) (web/src/api.ts): the text() read of each page response and the
  // JSON.parse of that same string are timed, in the page, and kept in arrival order.
  try {
    const text = Response.prototype.text;
    const parse = JSON.parse;
    Response.prototype.text = function () {
      const isPage = String(this.url).indexOf("/erasure-reasons/page") >= 0;
      const t = performance.now();
      const p = text.call(this);
      if (isPage) p.then((s) => { qc.last = s; qc.pages.push({ readMs: performance.now() - t, parseMs: null }); }, () => qc.pages.push({ readMs: null, parseMs: null }));
      return p;
    };
    JSON.parse = function (s, r) {
      if (typeof s === "string" && qc.last !== null && s === qc.last) {
        const t = performance.now();
        try { return parse.call(JSON, s, r); } finally {
          const e = qc.pages[qc.pages.length - 1];
          if (e && e.parseMs === null) e.parseMs = performance.now() - t;
          qc.last = null;
        }
      }
      return parse.call(JSON, s, r);
    };
  } catch (e) { /* the wrappers could not be installed: pageReadMs and pageParseMs are then missing and the judge says so */ }
  try {
    const sub = window.crypto && window.crypto.subtle;
    if (sub && typeof sub.digest === "function") {
      const digest = sub.digest.bind(sub);
      Object.defineProperty(sub, "digest", { configurable: true, writable: true, value: (alg, data) => {
        const t = performance.now();
        const bytes = data && data.byteLength ? data.byteLength : 0;
        const p = digest(alg, data);
        p.then(() => { qc.digest.calls++; qc.digest.bytes += bytes; qc.digest.ms += performance.now() - t; }, () => {});
        return p;
      } });
    }
  } catch (e) { /* subtle absent or not wrappable */ }
  try {
    new PerformanceObserver((list) => { for (const e of list.getEntries()) { qc.longTaskMs += e.duration; qc.longTasks++; } }).observe({ type: "longtask", buffered: true });
  } catch (e) { /* longtask is not supported: the metric is then missing and the judge says so */ }
})();`;

export const NO_SUBTLE_SCRIPT = 'Object.defineProperty(window.crypto, "subtle", { value: undefined, configurable: true });';

const round1 = (v) => Math.round(v * 10) / 10;

export function playwrightDriver({ page, context, url, targetEmail, changeCmd = "", phaseCmd = "", walk, pageResponses = () => 0 }) {
  const rows = page.locator(ROWS);
  let rowEl = null;
  const deletes = [];
  page.on("request", (r) => {
    if (r.method() === "DELETE" && /\/api\/v1\/admin\/users\/\d+$/.test(new URL(r.url()).pathname)) deletes.push({ at: Date.now(), bytes: r.postDataBuffer()?.length ?? 0 });
  });
  const mints = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && new URL(r.url()).pathname.endsWith("/admin/operation-attempts")) mints.push(Date.now());
  });
  let lastDelete = null;
  page.on("response", (r) => {
    if (r.request().method() === "DELETE" && /\/api\/v1\/admin\/users\/\d+$/.test(new URL(r.url()).pathname)) lastDelete = { status: r.status(), at: Date.now() };
  });

  const result = async () => {
    await page.waitForFunction(() => window.__qc.result !== null, null, { timeout: 60000, polling: 25 });
    return round1(await page.evaluate(() => window.__qc.result));
  };

  return {
    canChange: changeCmd !== "",
    // A phase boundary: the harness hook reads the cluster's WAL position and audit_logs (rows, bytes, rows by action) and prints them as one JSON object.
    async phase(name) {
      if (phaseCmd === "") return null;
      const r = spawnSync(phaseCmd, [name], { encoding: "utf8" });
      if (r.status !== 0) throw new Error(`the phase hook failed at ${name}: ${(r.stderr || "").trim().slice(0, 200)}`);
      try {
        return JSON.parse(r.stdout);
      } catch {
        throw new Error(`the phase hook printed no JSON at ${name}`);
      }
    },
    // Every review page response this page has received, from the first request on (never reset): a fresh traversal is proven by this count growing.
    async pageResponseCount() {
      return pageResponses();
    },
    async digest() {
      const d = await page.evaluate(() => window.__qc.digest);
      return { subtleCalls: d.calls, subtleBytes: d.bytes, subtleMs: Math.round(d.ms * 10) / 10 };
    },
    async open() {
      await page.goto(`${url}/admin?tab=users`);
      await page.getByRole("searchbox", { name: "Search by name or email" }).fill(targetEmail);
      rowEl = page.getByRole("row").filter({ hasText: targetEmail });
      await rowEl.getByRole("button", { name: /^More for/ }).first().click();
      await rowEl.getByRole("menuitem", { name: "Delete" }).click();
    },
    async snapshot() {
      return page.evaluate(
        ([navSel, rowSel, checking]) => ({
          navLine: document.querySelector(navSel)?.textContent ?? null,
          checking: document.body.textContent.includes(checking),
          rows: document.querySelectorAll(rowSel).length,
          progress: [...document.querySelectorAll("p.text-text-muted")].map((p) => p.textContent).find((t) => /chosen of|Choose Keep text|All .* chosen/.test(t)) ?? null,
        }),
        [NAV, ROWS, CHECKING],
      );
    },
    async env() {
      return page.evaluate(() => ({ secureContext: window.isSecureContext === true, hasSubtle: typeof window.crypto?.subtle?.digest === "function" }));
    },
    async chooseAt(i) {
      await page.evaluate((idx) => window.__qc.arm({ choose: idx }), i);
      await rows.nth(i).getByRole("radio", { name: i % 2 === 0 ? "Blank text" : "Keep text" }).check();
      return result();
    },
    async navigate(kind, arg, expect) {
      await page.evaluate((spec) => window.__qc.arm(spec), expect);
      if (kind === "next") await rowEl.getByRole("button", { name: "Next 50" }).click();
      else if (kind === "prev") await rowEl.getByRole("button", { name: "Previous 50" }).click();
      else if (kind === "goto") {
        await rowEl.getByLabel("Go to reason number").fill(String(arg));
        await rowEl.getByRole("button", { name: "Go", exact: true }).click();
      } else if (kind === "nextUnchosen") await rowEl.getByRole("button", { name: "Next reason without a choice" }).click();
      else if (kind === "nextChanged") await rowEl.getByRole("button", { name: "Next changed reason" }).click();
      else throw new Error(`unknown navigation ${kind}`);
      return result();
    },
    // Chooses every reason still without a choice with the panel's own per-reason handler: one radio click per reason, page by page. There is no
    // bulk control in the product and none here.
    async chooseAllRemaining(chosen) {
      const n = await page.evaluate(
        async ([rowSel, navSel]) => {
          const next = () => [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "Next 50");
          const first = () => [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "Previous 50");
          const line = () => document.querySelector(navSel)?.textContent ?? "";
          const wait = (ms) => new Promise((r) => setTimeout(r, ms));
          let count = 0;
          let guard = 0;
          while (first() && !first().disabled && guard++ < 100000) {
            first().click();
            await wait(0);
          }
          for (;;) {
            for (const li of document.querySelectorAll(rowSel)) {
              if (li.textContent.includes("Not chosen yet") || li.textContent.includes("Changed since you looked")) {
                li.querySelector('input[type="radio"][value="remove"]').click();
                count++;
              }
            }
            const b = next();
            if (!b || b.disabled) break;
            const before = line();
            b.click();
            for (let i = 0; i < 2000 && line() === before; i++) await wait(5);
          }
          return count;
        },
        [ROWS, NAV],
      );
      return { chosen: n };
    },
    async idAtRow(i) {
      return page.evaluate(([sel, idx]) => document.querySelectorAll(sel)[idx]?.getAttribute("data-reason-id") ?? null, [ROWS, i]);
    },
    async changeRow(requestId) {
      if (!requestId) throw new Error("no request id to change");
      const r = spawnSync(changeCmd, [requestId], { encoding: "utf8" });
      if (r.status !== 0) throw new Error(`the change hook failed: ${(r.stderr || "").trim()}`);
    },
    // Presses Delete account. The server is expected to refuse (the harness changed a reason): nothing is deleted.
    async press() {
      deletes.length = 0;
      mints.length = 0;
      lastDelete = null;
      const clickedAt = Date.now();
      await rowEl.getByRole("button", { name: "Delete account" }).click();
      await page.waitForFunction(() => true);
      for (let i = 0; i < 6000 && (deletes.length === 0 || lastDelete === null); i++) await new Promise((r) => setTimeout(r, 10));
      if (deletes.length === 0 || lastDelete === null) throw new Error("the press sent no DELETE");
      return { status: lastDelete.status, bodyBytes: deletes[0].bytes, pressToRequestMs: deletes[0].at - clickedAt, responseMs: lastDelete.at - deletes[0].at };
    },
    async walkStats() {
      const stats = await walk();
      // The page's own read+parse figure for each of the first walk's responses, in arrival order (the same order as the Node-side list).
      const inPage = await page.evaluate(() => window.__qc.pages.map((p) => ({ r: p.readMs, p: p.parseMs })));
      const r2 = (v) => (typeof v === "number" ? Math.round(v * 100) / 100 : null);
      return { ...stats, pageList: stats.pageList.map((p, i) => ({ ...p, pageReadMs: r2(inPage[i]?.r), pageParseMs: r2(inPage[i]?.p) })) };
    },
    async metrics() {
      const cdp = await context.newCDPSession(page);
      await cdp.send("Performance.enable");
      const m = async (name) => (await cdp.send("Performance.getMetrics")).metrics.find((x) => x.name === name)?.value ?? null;
      const heapUsedBytes = await m("JSHeapUsedSize");
      await cdp.send("HeapProfiler.enable");
      await cdp.send("HeapProfiler.collectGarbage");
      const heapAfterGcBytes = await m("JSHeapUsedSize");
      const domNodes = await m("Nodes");
      const long = await page.evaluate(() => ({ ms: window.__qc.longTaskMs, count: window.__qc.longTasks }));
      return { heapUsedBytes, heapAfterGcBytes, domNodes, longTaskMs: Math.round(long.ms), longTaskCount: long.count };
    },
  };
}
