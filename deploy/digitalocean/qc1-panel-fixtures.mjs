// Daemon-free fixtures for the QC-1 rendered-panel measurement (qc1/panel-core.mjs): a fake driver models the paginated deletion panel on a
// VIRTUAL clock (no browser, no sleeping), so the sequence, the readiness rule, the click and navigation bookkeeping and the finding the judge
// reads are tested here. Run by qc1-fixtures.sh; exits non-zero with a line on stderr on the first failure.
import assert from "node:assert/strict";
import { measurePanel, navLine, parseNavLine, parseUnchosen, percentile, summarize, summarizePages, DISPLAY_PAGE, FIRST_CLICK_MS, NEXT_CHANGED_ORIGINS, loadDelta, parseLoadavg, parseProcStatCpu, parsePressureCpu } from "./qc1/panel-core.mjs";
import { pageObserver } from "./qc1/page-observer.mjs";
import { bodyRuneSummary, countRunes, domTextAgreement, expectedShownIds, shownRange } from "./qc1/response-accounting.mjs";

let clock = 0;
const now = () => clock;
const sleep = async (ms) => {
  clock += ms;
};

// A model of the panel: the first page is on screen at firstPageAt, the walk ends at walkEndAt; a refused press starts a second walk only after
// restartDelay (the old walk's "ended" state is still on screen until then, as it is in the real panel), and the page responses it receives are counted.
function fakePanel({ expected, firstPageAt = 700, walkEndAt = 9000, clickMs = () => 12, navMs = () => 20, canChange = true, neverEnds = false, restartDelay = 800, walk2Ms = 2500, failAt = null, hook = true, subtleBytes = null, slowPages = {}, nullFields = {} }) {
  const s = { start: 0, chosen: new Set(), chooseCalls: 0, navCalls: [], restartAt: null, walk2End: null, changed: null, pressed: false, tOpen: 0, wal: 1000, marks: [] };
  const pages = Math.ceil(expected / 100);
  const walking = () => {
    if (neverEnds) return true;
    if (s.restartAt === null) return clock - s.tOpen < walkEndAt;
    if (clock < s.restartAt) return false; // the old walk's ended state, still on screen
    return clock < s.walk2End;
  };
  const total = () => (walking() ? Math.max(1, Math.min(expected, Math.floor((expected * Math.max(0, clock - (s.restartAt ?? s.tOpen))) / (s.restartAt === null ? walkEndAt : walk2Ms)))) : expected);
  const unchosen = () => {
    let n = 0;
    for (let i = 0; i < total(); i++) if (!s.chosen.has(i) || s.changed === i) n++;
    return n;
  };
  const pageCount = () => {
    const first = Math.min(pages, Math.floor((pages * Math.max(0, clock - s.tOpen)) / walkEndAt));
    if (s.restartAt === null || clock < s.restartAt) return first;
    return pages + Math.min(pages, Math.floor((pages * (clock - s.restartAt)) / walk2Ms));
  };
  const d = {
    state: s,
    canChange,
    // Sign-in, navigation and the user search take 300 ms before the Delete-menu action; the menu action is where every measured interval starts.
    async open() {
      clock += 300;
      s.tOpen = clock;
      s.menuAt = clock;
    },
    async timeline() {
      return {
        menuAt: s.menuAt ?? null,
        firstPageAt: s.menuAt === undefined ? null : s.menuAt + firstPageAt,
        walkDoneAt: neverEnds || s.menuAt === undefined ? null : s.menuAt + walkEndAt,
        pressAt: s.pressedAt ?? null,
        walk2DoneAt: s.walk2End ?? null,
      };
    },
    async lastClickEnd() {
      return s.lastEnd;
    },
    async snapshot() {
      if (failAt === "snapshot-after-walk" && clock - s.tOpen > walkEndAt + 100) throw new Error("the page stopped answering");
      const rows = clock - s.tOpen >= firstPageAt ? Math.min(DISPLAY_PAGE, expected - s.start) : 0;
      const t = total();
      return {
        navLine: rows > 0 ? navLine(s.start, t, walking()) : null,
        checking: walking(),
        rows,
        progress: s.chosen.size === 0 || unchosen() > 0 ? `Choose Keep text or Blank text for ${unchosen() === 1 ? "1 more reason" : `${unchosen()} more reasons`}.` : `All ${expected} reasons chosen.`,
      };
    },
    async env() {
      return { secureContext: true, hasSubtle: true };
    },
    async chooseAt(i) {
      s.chooseCalls++;
      const ms = clickMs(s.chooseCalls);
      clock += ms;
      s.lastEnd = clock;
      s.chosen.add(s.start + i);
      return ms;
    },
    async navigate(kind, arg, expect) {
      s.navCalls.push(kind);
      const ms = navMs(kind);
      clock += ms;
      if (kind === "next") s.start += DISPLAY_PAGE;
      else if (kind === "prev") s.start -= DISPLAY_PAGE;
      else if (kind === "goto") s.start = Math.floor((arg - 1) / DISPLAY_PAGE) * DISPLAY_PAGE;
      else if (kind === "nextUnchosen") {
        let at = -1;
        for (let i = 0; i < expected; i++) if (!s.chosen.has(i)) { at = i; break; }
        if (at < 0) throw new Error("nothing is unchosen");
        s.start = Math.floor(at / DISPLAY_PAGE) * DISPLAY_PAGE;
      } else if (kind === "nextChanged") {
        if (s.changed === null) throw new Error("nothing has changed");
        s.start = Math.floor(s.changed / DISPLAY_PAGE) * DISPLAY_PAGE;
      }
      if (expect.line !== undefined) assert.equal(navLine(s.start, expected, false), expect.line, `after ${kind}`);
      return ms;
    },
    async chooseAllRemaining() {
      let n = 0;
      for (let i = 0; i < expected; i++) if (!s.chosen.has(i)) { s.chosen.add(i); n++; }
      return { chosen: n };
    },
    async idAtRow(i) {
      return String(1000 + s.start + i);
    },
    async changeRow(id) {
      s.changed = Number(id) - 1000;
    },
    async press() {
      s.pressed = true;
      s.pressedAt = clock;
      s.start = 0;
      s.restartAt = clock + restartDelay;
      s.walk2End = s.restartAt + walk2Ms;
      clock += 40;
      return { status: 409, bodyBytes: 200 + (Math.ceil(expected / 4) * 4) / 3 | 0, pressToRequestMs: 35, responseMs: 900 };
    },
    async pageResponseCount() {
      return pageCount();
    },
    async digest() {
      const pre = 35 + 40 * expected;
      return { subtleCalls: subtleBytes === 0 ? 0 : 2, subtleBytes: subtleBytes ?? pre + 4, subtleMs: 3 };
    },
    async walkStats() {
      return { pageRequests: pages, pageResponseBytes: expected * 310, apiOrigin: "real" };
    },
    // Every review page observation that has COMPLETED by now, in every walk (what the real collector persists as each page finishes).
    async pageObservations() {
      const first = Math.min(pages, Math.floor((pages * Math.max(0, clock - s.tOpen)) / walkEndAt));
      const second = s.restartAt === null || clock < s.restartAt ? 0 : Math.min(pages, Math.floor((pages * (clock - s.restartAt)) / walk2Ms));
      const mk = (walk, i, seq) => ({
        seq, walk, status: 200, bytes: 30000 + i, requestMs: slowPages[`${walk}:${i}`] ?? 10 + (i % 7), nodeParseMs: 1.5, pageReadMs: 2, pageParseMs: 3 + (i % 3), ...(nullFields[`${walk}:${i}`] ?? {}),
      });
      return [...Array.from({ length: first }, (_, i) => mk(1, i, i)), ...Array.from({ length: second }, (_, i) => mk(2, i, pages + i))];
    },
    async metrics() {
      return { heapUsedBytes: 9e6, heapAfterGcBytes: 6e6, domNodes: 4200, longTaskMs: 120, longTaskCount: 3 };
    },
  };
  if (hook) {
    // The phase hook: absolute WAL bytes and audit_logs (rows, bytes, rows by action) that grow with the virtual clock.
    d.phase = async (name) => {
      s.marks.push(name);
      clock += 500; // the hook (a docker exec and four queries) is slow, and none of it may reach a measured interval
      s.wal += 1000 + Math.floor(clock);
      const views = Math.floor(clock / 1000);
      return { wal: s.wal, auditRows: 10 + views, auditBytes: 8192 * (1 + Math.floor(views / 50)), auditActions: { "reason.view": views, ...(s.pressed ? { "reason.refused": 1 } : {}) } };
    };
  }
  return d;
}
const run = (opts, extra = {}) => {
  clock = 0;
  const p = fakePanel(opts);
  return measurePanel(p, { expected: opts.expected, now, sleep, ...extra }).then((r) => ({ r, p }));
};

// --- the bookkeeping helpers ---------------------------------------------------------------------------------------------------------------------
assert.equal(percentile([], 95), null);
assert.equal(percentile([5], 95), 5);
assert.equal(percentile(Array.from({ length: 20 }, (_, i) => i + 1), 95), 19);
assert.equal(percentile(Array.from({ length: 30 }, (_, i) => i + 1), 95), 29);
assert.deepEqual(summarize([]), { count: 0, p50Ms: null, p95Ms: null, maxMs: null });
assert.deepEqual(summarize([3, 1, 2]), { count: 3, p50Ms: 2, p95Ms: 3, maxMs: 3 });
assert.equal(navLine(0, 10000, true), "Reasons 1 to 50 of at least 10,000");
assert.equal(navLine(9950, 10000, false), "Reasons 9,951 to 10,000 of 10,000");
assert.deepEqual(parseNavLine("Reasons 1 to 50 of at least 10,000"), { from: 0, last: 50, total: 10000, loading: true });
assert.deepEqual(parseNavLine("Reasons 51 to 60 of 60"), { from: 50, last: 60, total: 60, loading: false });
assert.equal(parseNavLine("something else"), null);
assert.equal(parseNavLine(null), null);
assert.equal(parseUnchosen("Choose Keep text or Blank text for 1 more reason."), 1);
assert.equal(parseUnchosen("Choose Keep text or Blank text for 1,234 more reasons."), 1234);
assert.equal(parseUnchosen("All 10 reasons chosen."), null);

// --- a healthy 1,000-reason review -----------------------------------------------------------------------------------------------------------------
{
  const { r, p } = await run({ expected: 1000 });
  assert.equal(r.failed, null);
  assert.equal(r.walkTotal, 1000);
  assert.equal(r.firstPage.itemsRendered, 50);
  assert.equal(r.firstPage.expectedItems, 50);
  assert.equal(r.clicks.count, 30);
  assert.ok(r.clicks.beforeWalkEnd >= 1 && r.clicks.beforeWalkEnd <= 8, `before the walk ends: ${r.clicks.beforeWalkEnd}`);
  assert.ok(r.clicks.midList > 0 && r.clicks.lastPage > 0, "mid-list and last page clicks");
  assert.equal(r.clicks.p95Ms, 12);
  assert.equal(r.clicks.maxMs, 12);
  // readiness is the later of the walk's end and the first interactive click
  assert.ok(r.firstPage.firstInteractiveMs !== null && r.firstPage.firstInteractiveMs < r.walkEndMs);
  assert.equal(r.renderReadyMs, Math.max(r.walkEndMs, r.firstPage.firstInteractiveMs));
  assert.equal(r.renderReadyMs, r.walkEndMs);
  assert.ok(r.walkEndMs >= 9000, `the walk end is read when the review has ended: ${r.walkEndMs}`);
  for (const k of ["nextPage", "jump", "nextUnchosen", "nextChanged"]) assert.ok(r.navigation[k].observed && r.navigation[k].maxMs === 20, `navigation.${k}`);
  assert.ok(r.navigation.nextPage.count >= 2 && r.navigation.jump.count >= 3);
  assert.equal(r.press.observed, true);
  assert.equal(r.press.status, "409");
  assert.equal(typeof r.press.bodyBytes, "number");
  assert.equal(r.reload.unchosenAfterReload, 1);
  assert.equal(r.reload.choicesSurvived, 999);
  assert.equal(r.pageRequests, 10);
  assert.equal(r.pageResponseBytes, 310000);
  assert.equal(r.apiOrigin, "real");
  assert.equal(r.secureContext, true);
  assert.equal(r.digestPathInferred, "webcrypto");
  assert.equal(r.domNodes, 4200);
  assert.equal(r.afterInteractions.domNodes, 4200);
  // F7: per-page figures, the digest path that ran, the press response and the per-phase WAL and audit-by-action figures.
  assert.equal(r.pages.count, 20, "both walks: the first read and the read again after the refused press");
  assert.deepEqual(r.pages.walks.map((w) => [w.walk, w.count]), [[1, 10], [2, 10]]);
  assert.deepEqual(r.pages.absent, { bytes: 0, requestMs: 0, readMs: 0, parseMs: 0, nodeParseMs: 0 });
  assert.equal(r.pages.responseBytesMax, 30009);
  assert.equal(r.pages.requestMsMax, 16);
  assert.equal(r.pages.readMsMax, 2);
  assert.equal(r.pages.parseMsMax, 5);
  assert.equal(r.pages.parseMsTotal, 78);
  assert.equal(r.pages.readMsTotal, 40);
  assert.equal(r.pages.nodeParseMsMax, 1.5);
  assert.equal(r.digest.path, "webcrypto");
  assert.equal(r.digest.preimageBytes, 35 + 40 * 1000);
  assert.equal(r.digest.buildMs, 35);
  assert.match(r.digest.buildMsKind, /upper bound/);
  assert.equal(r.press.responseMs, 900);
  for (const ph of ["walk", "interactions", "press"]) {
    assert.ok(r.phases[ph] !== null, `phase ${ph}`);
    assert.ok(r.phases[ph].walBytes > 0, `${ph} wal`);
    assert.equal(typeof r.phases[ph].auditActionsDelta, "object");
    assert.equal(typeof r.phases[ph].auditBytes.before, "number");
  }
  assert.equal(r.phases.press.auditActionsDelta["reason.refused"], 1, "the refused press is its own phase in the audit by action");
  assert.deepEqual(p.state.marks, ["open", "walk-end", "interactions-end", "press-start", "press-end"]);
}

// --- readiness: the later of the walk and the first interactive click ----------------------------------------------------------------------------------
{
  // The first radio takes 250 ms to accept a click (over 200): it does not count; the next reason is tried and the moment of the first fast click counts.
  const { r, p } = await run({ expected: 1000, clickMs: (n) => (n === 1 ? 250 : 15) });
  assert.ok(r.firstPage.firstInteractiveMs !== null);
  assert.ok(p.state.chooseCalls >= 2);
  assert.equal(r.clicks.maxMs, 250, "the slow click is kept among the samples");
}
{
  // Every first-page click is slow: never interactive within the rule, so there is no readiness number at all (null, which the judge calls missing).
  const { r } = await run({ expected: 1000, clickMs: () => FIRST_CLICK_MS + 50 });
  assert.equal(r.firstPage.firstInteractiveMs, null);
  assert.equal(r.renderReadyMs, null);
}
{
  // A fast walk (ends before the first interactive click): the interactive moment is what readiness is.
  const { r } = await run({ expected: 1000, walkEndAt: 500, firstPageAt: 900 });
  assert.ok(r.walkEndMs >= 0);
  assert.equal(r.renderReadyMs, Math.max(r.walkEndMs, r.firstPage.firstInteractiveMs));
}
// A walk that never ends is a failure of the measurement, reported with the phase it stopped in, and what finished before it is KEPT.
{
  const { r } = await run({ expected: 1000, neverEnds: true }, { timeoutMs: 20000 });
  assert.equal(r.failed.phase, "walk");
  assert.match(r.failed.error, /timed out waiting for the walk to end/);
  assert.ok(r.clicks.beforeWalkEnd >= 1, "the clicks made before the failure are kept");
  assert.equal(r.walkEndMs, null);
  assert.equal(r.renderReadyMs, null);
  assert.ok(r.pages !== null && r.pages.walks.length === 1, "the pages that completed before the failure are kept");
  assert.equal(r.press, null, "the press phase was never reached");
}
// A failure LATE in the flow keeps the earlier phases, including a figure that already crossed a budget (here a 130 s walk).
{
  const { r } = await run({ expected: 1000, walkEndAt: 130000, failAt: "snapshot-after-walk" }, { timeoutMs: 400000 });
  assert.equal(r.failed.phase, "interactions");
  assert.match(r.failed.error, /stopped answering/);
  assert.ok(r.walkEndMs > 120000, `the walk figure survives the later failure: ${r.walkEndMs}`);
  assert.ok(r.pages !== null && r.phases.walk !== null, "the walk phase keeps its page and WAL figures");
  assert.equal(r.phases.interactions, null);
  assert.equal(r.phases.press, null);
  assert.equal(r.digest, null);
}
// A fresh traversal after the refusal is PROVEN: the old walk's "ended" state stays on screen for a while after Delete is pressed, and the measurement
// does not take it for the second walk.
{
  const { r, p } = await run({ expected: 1000, restartDelay: 3000, walk2Ms: 4000 });
  assert.equal(r.failed, null);
  assert.ok(r.reload.secondWalkPageResponses >= 10, `a whole second walk of page responses (${r.reload.secondWalkPageResponses})`);
  assert.ok(r.reload.reloadAfterRefusalMs >= 3000 + 4000 - 100, `the reload time spans the delay and the second walk: ${r.reload.reloadAfterRefusalMs}`);
  assert.equal(r.reload.unchosenAfterReload, 1);
  assert.ok(p.state.restartAt !== null);
}
// If the second walk never starts (the old ended state is all there ever is), the run says so instead of passing on the old state.
{
  const { r } = await run({ expected: 1000, restartDelay: 10 ** 9 }, { timeoutMs: 30000 });
  assert.equal(r.failed.phase, "press");
  assert.match(r.failed.error, /timed out waiting for a fresh traversal/);
}
// The digest path is the one that ACTUALLY ran: calls over the whole preimage are webcrypto; no call at all (or only the product's self-test) is the bundled fallback.
{
  const { r } = await run({ expected: 1000, subtleBytes: 0 });
  assert.equal(r.digest.path, "fallback");
  assert.equal(r.digest.subtleCalls, 0);
  const { r: r2 } = await run({ expected: 1000, subtleBytes: 12 });
  assert.equal(r2.digest.path, "fallback", "a self-test-sized call is not the digest");
}
// Without a phase hook the phases are null (missing), not zero.
{
  const { r } = await run({ expected: 1000, hook: false });
  assert.equal(r.failed, null);
  assert.equal(r.phases.walk, null);
  assert.equal(r.phases.press, null);
}

// --- every page observation, in every walk, even when the run stops ------------------------------------------------------------------------------------
{
  // A slow request in the RELOAD (walk 2) is seen as the reload's, and is the overall maximum.
  const { r } = await run({ expected: 1000, slowPages: { "2:3": 11000 } });
  assert.equal(r.failed, null);
  assert.equal(r.pages.requestMsMax, 11000);
  assert.equal(r.pages.walks[0].requestMsMax, 16);
  assert.equal(r.pages.walks[1].requestMsMax, 11000);
}
{
  // A run that stops MID-WALK (the walk never ends) keeps the pages that had completed, including a slow one, though the first walk's totals were never pulled.
  const { r } = await run({ expected: 1000, neverEnds: true, slowPages: { "1:2": 12500 } }, { timeoutMs: 20000 });
  assert.equal(r.failed.phase, "walk");
  assert.equal(r.walkEndMs, null);
  assert.equal(r.pageRequests, null, "the first walk's totals were never taken");
  assert.ok(r.pages !== null && r.pages.count >= 3, `completed pages are kept: ${r.pages && r.pages.count}`);
  assert.equal(r.pages.requestMsMax, 12500);
  assert.deepEqual(r.pages.walks.map((w) => w.walk), [1]);
}
{
  // Absent fields are counted, never zero, and never hide the maximum of the pages that have them.
  const { r } = await run({ expected: 1000, nullFields: { "1:1": { requestMs: null, pageParseMs: null }, "2:4": { bytes: null, pageReadMs: null } }, slowPages: { "1:5": 9000 } });
  assert.deepEqual(r.pages.absent, { bytes: 1, requestMs: 1, readMs: 1, parseMs: 1, nodeParseMs: 0 });
  assert.equal(r.pages.requestMsMax, 9000);
  assert.equal(r.pages.count, 20);
}
{
  // If the page is gone at the end, the pages collected when the walk ended are still used.
  const p = fakePanel({ expected: 1000, slowPages: { "1:1": 15000 } });
  let calls = 0;
  const original = p.pageObservations;
  p.pageObservations = async () => {
    calls++;
    if (calls > 1) throw new Error("the page is gone");
    return original();
  };
  clock = 0;
  const r = await measurePanel(p, { expected: 1000, now, sleep });
  assert.equal(r.pages.requestMsMax, 15000);
}
// summarizePages on its own: nothing observed is null, not zero.
assert.equal(summarizePages([]), null);
assert.equal(summarizePages(null), null);
{
  const s = summarizePages([{ walk: 1, bytes: 10, requestMs: 5, nodeParseMs: 1, pageReadMs: 2, pageParseMs: 3 }, { walk: 1, bytes: null, requestMs: 7, nodeParseMs: null, pageReadMs: null, pageParseMs: null }]);
  assert.equal(s.requestMsMax, 7);
  assert.equal(s.responseBytesMax, 10);
  assert.deepEqual(s.absent, { bytes: 1, requestMs: 0, readMs: 1, parseMs: 1, nodeParseMs: 1 });
}

// --- the page observer: each page is recorded when it ARRIVES and persisted as it completes, through every walk ------------------------------------------------
const resp = (path, { status = 200, body = '{"items":[]}', timing = { startTime: 1, responseEnd: 20 }, origin = "http://127.0.0.1:8080", fail = false, gate = null } = {}) => ({
  url: () => `${origin}${path}`,
  status: () => status,
  body: async () => {
    if (gate) await gate;
    if (fail) throw new Error("no body");
    return Buffer.from(body);
  },
  request: () => ({ timing: () => timing }),
});
const PAGE = "/api/v1/admin/users/7/erasure-reasons/page";
const parsed = (lines, event) => lines.map((l) => JSON.parse(l)).filter((x) => x.event === event);
{
  const lines = [];
  const obs = pageObserver({ origin: "http://127.0.0.1:8080", append: (l) => lines.push(l), sleep: async () => {}, clock: () => 0 });
  await obs.onResponse(resp("/api/v1/auth/me"));
  assert.equal(obs.responses(), 0, "only review pages are observed");
  assert.equal(lines.length, 0);
  await obs.onResponse(resp(PAGE));
  assert.deepEqual(lines.map((l) => JSON.parse(l).event), ["start", "done"], "arrival, then completion");
  assert.deepEqual(JSON.parse(lines[0]), { seq: 0, walk: 1, event: "start", status: 200, foreignOrigin: false });
  assert.deepEqual(JSON.parse(lines[1]), { seq: 0, walk: 1, status: 200, foreignOrigin: false, bytes: 12, runes: 12, requestMs: 20, nodeParseMs: 0, event: "done" });
  // A slow page is on the file the moment it ARRIVES, and not reported as done until its body has been read.
  let release;
  const gate = new Promise((r) => { release = r; });
  const pending = obs.onResponse(resp(PAGE, { gate, timing: { startTime: 1, responseEnd: 14000 } }));
  assert.equal(parsed(lines, "start").length, 2, "the slow page's start is already persisted");
  assert.equal(parsed(lines, "done").length, 1);
  assert.equal(obs.responses(), 2, "a request that has arrived counts for a fresh-traversal proof before it completes");
  assert.equal(obs.observations().length, 1, "completed only");
  const withMissing = obs.observationsWithMissing();
  assert.equal(withMissing.length, 2);
  assert.deepEqual([withMissing[1].completed, withMissing[1].bytes, withMissing[1].requestMs], [false, null, null], "the unfinished one is an explicit missing entry");
  assert.deepEqual(obs.reconcile(), [{ walk: 1, started: 2, completed: 1, pending: 1 }]);
  release();
  await pending;
  assert.deepEqual(obs.reconcile(), [{ walk: 1, started: 2, completed: 2, pending: 0 }]);
  assert.equal(obs.observations()[1].requestMs, 14000);
  assert.deepEqual(await obs.walkStats(1), { pageRequests: 2, pageResponseBytes: 24, apiOrigin: "real" });
  // the reload is walk 2, persisted and kept apart
  obs.startWalk(2);
  await obs.onResponse(resp(PAGE, { timing: { startTime: 1, responseEnd: 11000 } }));
  assert.deepEqual(obs.observations().map((o) => [o.seq, o.walk]), [[0, 1], [1, 1], [2, 2]]);
  assert.equal(parsed(lines, "done")[2].walk, 2);
  assert.equal(parsed(lines, "done")[2].requestMs, 11000);
  assert.equal((await obs.walkStats(1)).pageRequests, 2, "the reload does not change the first walk's totals");
  // an unreadable body is recorded with null fields (absent), a non-JSON body has no parse time
  await obs.onResponse(resp(PAGE, { fail: true }));
  await obs.onResponse(resp(PAGE, { body: "not json" }));
  const dones = parsed(lines, "done");
  assert.equal(dones[3].completed, false);
  assert.equal(dones[3].requestMs, null);
  assert.equal(typeof dones[3].ageMs, "number");
  assert.equal(dones[4].nodeParseMs, null);
  assert.equal(dones[4].bytes, 8);
  // a foreign origin and a non-200 make the origin unverified
  const o2 = pageObserver({ origin: "http://127.0.0.1:8080", sleep: async () => {} });
  await o2.onResponse(resp(PAGE, { origin: "http://evil.example" }));
  assert.equal((await o2.walkStats(1)).apiOrigin, "unverified");
  const o3 = pageObserver({ origin: "http://127.0.0.1:8080", sleep: async () => {} });
  await o3.onResponse(resp(PAGE, { status: 503 }));
  assert.equal((await o3.walkStats(1)).apiOrigin, "unverified");
  // a walk with a request that never completes gets no totals (null, unverified), after waiting for it
  const o4 = pageObserver({ origin: "http://127.0.0.1:8080", sleep: async () => {} });
  void o4.onResponse(resp(PAGE, { gate: new Promise(() => {}) }));
  await new Promise((r) => setTimeout(r, 0));
  assert.deepEqual(await o4.walkStats(1, 100), { pageRequests: 1, pageResponseBytes: null, apiOrigin: "unverified" });
}
{
  // A body that rejects after a timeout must retain its observed lower bound.
  let t = 0, rejectBody;
  const lines = [];
  const obs = pageObserver({ origin: "http://127.0.0.1:8080", append: (l) => lines.push(l), sleep: async (ms) => { t += ms; }, clock: () => t });
  const pending = obs.onResponse(resp(PAGE, { gate: new Promise((_, reject) => { rejectBody = reject; }) }));
  await obs.drain(10500);
  rejectBody(new Error("body failed"));
  await pending;
  assert.equal(obs.observationsWithMissing()[0].completed, false);
  assert.ok(summarizePages(obs.observationsWithMissing()).pendingAgeMsMax >= 10000);
  assert.ok(parsed(lines, "done")[0].ageMs >= 10000);
}
{
  // DRAIN: the final collection waits (bounded) for everything in flight, in every walk. What does not complete is marked timed out ONCE, persisted, and stays as an explicit entry with the age it reached.
  let t = 0;
  const lines = [];
  const obs = pageObserver({ origin: "http://127.0.0.1:8080", append: (l) => lines.push(l), sleep: async (ms) => { t += ms; await new Promise((r) => setTimeout(r, 0)); }, clock: () => t });
  for (let i = 0; i < 10; i++) await obs.onResponse(resp(PAGE));
  obs.startWalk(2);
  for (let i = 0; i < 9; i++) await obs.onResponse(resp(PAGE));
  let finish;
  void obs.onResponse(resp(PAGE, { gate: new Promise((r) => { finish = r; }) }));
  await new Promise((r) => setTimeout(r, 0));
  assert.deepEqual(obs.reconcile(), [{ walk: 1, started: 10, completed: 10, pending: 0 }, { walk: 2, started: 10, completed: 9, pending: 1 }]);
  assert.equal(await obs.drain(300), 1, "one is still pending after the drain");
  assert.equal(parsed(lines, "timeout").length, 1);
  assert.equal(await obs.drain(100), 1);
  assert.equal(parsed(lines, "timeout").length, 1, "the timeout is persisted once");
  assert.ok(parsed(lines, "timeout")[0].ageMs >= 300);
  const all = obs.observationsWithMissing();
  assert.equal(all.length, 20, "every response that arrived, in both walks");
  assert.deepEqual(all.filter((o) => o.completed === false).map((o) => [o.seq, o.walk, o.bytes, o.requestMs]), [[19, 2, null, null]]);
  // ...and the summary the core builds from them says so, never valid
  const s = summarizePages(all);
  assert.deepEqual([s.started, s.completed, s.incomplete], [20, 19, 1]);
  assert.deepEqual(s.walks.map((w) => [w.walk, w.started, w.completed, w.incomplete]), [[1, 10, 10, 0], [2, 10, 9, 1]]);
  assert.deepEqual(s.absent, { bytes: 1, requestMs: 1, readMs: 20, parseMs: 20, nodeParseMs: 1 }, "these observations carry no page-side times, and the unfinished one carries nothing");
  assert.ok(s.pendingAgeMsMax >= 300);
  // if it finishes late, the next collection has it as completed and nothing else changes
  finish();
  await new Promise((r) => setTimeout(r, 0));
  assert.deepEqual(obs.reconcile(), [{ walk: 1, started: 10, completed: 10, pending: 0 }, { walk: 2, started: 10, completed: 10, pending: 0 }]);
  assert.equal(obs.observationsWithMissing().filter((o) => o.completed === false).length, 0);
}
{
  // THE REVIEW'S CASE through the whole measurement: 10 first responses, 9 completed reload responses and 1 still pending at the end. The row is INCOMPLETE and carries it; the budget
  // findings of what did finish are kept; it can never be a valid measured case.
  const lines = [];
  let t = 0;
  const obs = pageObserver({ origin: "http://127.0.0.1:8080", append: (l) => lines.push(l), sleep: async (ms) => { t += ms; await new Promise((r) => setTimeout(r, 0)); }, clock: () => t });
  for (let i = 0; i < 10; i++) await obs.onResponse(resp(PAGE, { timing: { startTime: 1, responseEnd: i === 4 ? 10500 : 20 } }));
  obs.startWalk(2);
  for (let i = 0; i < 9; i++) await obs.onResponse(resp(PAGE));
  void obs.onResponse(resp(PAGE, { gate: new Promise(() => {}) }));
  await new Promise((r) => setTimeout(r, 0));
  const p = fakePanel({ expected: 1000 });
  p.pageObservations = async () => {
    await obs.drain(200);
    return obs.observationsWithMissing().map((o) => ({ ...o, pageReadMs: o.completed === false ? null : 2, pageParseMs: o.completed === false ? null : 3 }));
  };
  clock = 0;
  const r = await measurePanel(p, { expected: 1000, now, sleep });
  assert.equal(r.failed, null);
  assert.deepEqual([r.pages.started, r.pages.completed, r.pages.incomplete], [20, 19, 1]);
  assert.equal(r.pages.requestMsMax, 10500, "the slow page that did finish keeps its figure beside the unfinished one");
  assert.deepEqual(r.pages.walks.map((w) => [w.walk, w.incomplete]), [[1, 0], [2, 1]]);
}

// --- small and boundary reviews -----------------------------------------------------------------------------------------------------------------------
{
  const { r } = await run({ expected: 60 });
  assert.equal(r.firstPage.expectedItems, 50);
  assert.equal(r.walkTotal, 60);
  assert.ok(r.navigation.nextPage.observed, "60 reasons have a second page");
  assert.equal(r.navigation.nextPage.count, 2, "next, then previous");
}
{
  const { r } = await run({ expected: 50 });
  assert.equal(r.navigation.nextPage.observed, false, "50 reasons have one page: no page navigation is invented");
  assert.equal(r.firstPage.itemsRendered, 50);
}
{
  const { r } = await run({ expected: 10 });
  assert.equal(r.firstPage.expectedItems, 10);
  assert.ok(r.clicks.count <= 10, "no more clicks than reasons");
}
// A review of 100,000: the same sequence, bounded work (the fake panel has no per-reason DOM).
{
  const { r, p } = await run({ expected: 100000, walkEndAt: 60000 });
  assert.equal(r.walkTotal, 100000);
  assert.equal(r.clicks.count, 30);
  assert.ok(p.state.chooseCalls < 100, "the sequence is O(1) in the review's length until choose-all");
  assert.equal(r.reload.unchosenAfterReload, 1);
  assert.equal(r.pageRequests, 1000);
}
// Without a change hook the changed-reason path is recorded as not observed, with a reason.
{
  const { r } = await run({ expected: 1000, canChange: false });
  assert.equal(r.press.observed, false);
  assert.match(r.press.reason, /no change hook/);
  assert.equal(r.reload, null);
  assert.equal(r.navigation.nextChanged.observed, false);
}
// A bad expected count is refused before anything is touched.
await assert.rejects(measurePanel(fakePanel({ expected: 1 }), { expected: 0, now, sleep }), /whole number of reasons/);
await assert.rejects(measurePanel(fakePanel({ expected: 1 }), { expected: 1.5, now, sleep }), /whole number of reasons/);
// A measurement given no clock does not read one.
clock = 0;
{
  const r = await measurePanel(fakePanel({ expected: 5 }), { expected: 5 });
  assert.match(r.failed.error, /not given a clock/, "no clock is a recorded failure, not a number");
}
// --- the clock starts at the Delete-menu action and every stamp is the page's own -----------------------------------------------------------------------------
{
  // Sign-in, navigation and search before the menu (300 ms) and a slow phase hook (500 ms a mark) are NOT in any interval; 190 ms clicks made while the walk runs
  // do not postpone the walk's stamp (a loop that noticed the end after its clicks would read well over 2,000 ms here).
  const { r } = await run({ expected: 1000, firstPageAt: 700, walkEndAt: 2000, clickMs: () => 190 });
  assert.equal(r.failed, null);
  assert.equal(r.firstPage.firstPageMs, 700);
  assert.equal(r.walkEndMs, 2000, "the walk end is the page's stamp, from the menu action");
  assert.ok(r.firstPage.firstInteractiveMs !== null && r.firstPage.firstInteractiveMs >= 700 + 190 && r.firstPage.firstInteractiveMs < 1100, `first interactive is its own completion stamp: ${r.firstPage.firstInteractiveMs}`);
  assert.equal(r.renderReadyMs, 2000);
}
{
  // The first interactive click completes AFTER the walk ended: readiness is that later stamp.
  const { r } = await run({ expected: 1000, firstPageAt: 100, walkEndAt: 400, clickMs: () => 150 });
  assert.equal(r.walkEndMs, 400);
  assert.ok(r.firstPage.firstInteractiveMs > 100, "from the menu action");
  assert.equal(r.renderReadyMs, Math.max(r.walkEndMs, r.firstPage.firstInteractiveMs));
}
{
  // The second walk is timed from the pointerdown on Delete to the page's own stamp of the new walk's end.
  const { r } = await run({ expected: 1000, restartDelay: 3000, walk2Ms: 4000 });
  assert.equal(r.reload.reloadAfterRefusalMs, 3000 + 4000);
}
// A page that stamps nothing leaves every interval null (missing), not a guess from the loop's clock.
{
  const p = fakePanel({ expected: 1000 });
  p.timeline = async () => ({ menuAt: null, firstPageAt: null, walkDoneAt: null, pressAt: null, walk2DoneAt: null });
  clock = 0;
  const r = await measurePanel(p, { expected: 1000, now, sleep });
  assert.equal(r.walkEndMs, null);
  assert.equal(r.firstPage.firstPageMs, null);
  assert.equal(r.renderReadyMs, null);
  assert.equal(r.reload.reloadAfterRefusalMs, null);
}

// --- multibyte text (#293): the accounting that shows a page really carried it, and the comparison of the text on screen with the database's ---------------------------------
{
  const cp = (...c) => String.fromCodePoint(...c);
  // The unit qc1/scenario.sql repeats: six emoji, a woman and a laptop joined by a ZWJ, accented letters, three CJK characters, the euro sign, combining marks.
  const unit = cp(0x1f600, 0x1f603, 0x1f604, 0x1f601, 0x1f389, 0x1f64f, 0x1f469, 0x200d, 0x1f4bb, 0xe9, 0xf1, 0x3a9, 0x20ac, 0x65e5, 0x672c, 0x8a9e) + "e" + cp(0x301) + "a" + cp(0x323, 0x301);
  assert.equal(Array.from(unit).length, 21, "the unit is 21 runes");
  assert.equal(Buffer.byteLength(unit), 61, "and 61 bytes");
  assert.equal(countRunes(Buffer.from(unit)), 21);
  assert.equal(countRunes(Buffer.from("abc")), 3);
  assert.equal(countRunes(Buffer.alloc(0)), 0);
  assert.deepEqual(bodyRuneSummary([{ bytes: 61, runes: 21 }, { bytes: 3, runes: 3 }, { bytes: null, runes: null }, { bytes: 5 }]), { responses: 2, bytes: 64, runes: 24, bytesPerRune: Math.round((64 / 24) * 1000) / 1000 }, "a response without both figures is not counted");
  assert.deepEqual(bodyRuneSummary([]), { responses: 0, bytes: 0, runes: 0, bytesPerRune: null });
  assert.deepEqual(bodyRuneSummary(undefined), { responses: 0, bytes: 0, runes: 0, bytesPerRune: null });

  const lines = [];
  const obs = pageObserver({ origin: "http://127.0.0.1:8080", append: (l) => lines.push(l), sleep: async () => {}, clock: () => 0 });
  const body = JSON.stringify({ items: [{ requestId: "1", reason: unit }] });
  await obs.onResponse(resp(PAGE, { body }));
  const done = parsed(lines, "done")[0];
  assert.equal(done.bytes, Buffer.byteLength(body));
  assert.equal(done.runes, Array.from(body).length);
  assert.ok(done.bytes > done.runes, "multibyte text takes more bytes than runes");
  assert.deepEqual(bodyRuneSummary(obs.observations()), { responses: 1, bytes: done.bytes, runes: done.runes, bytesPerRune: Math.round((done.bytes / done.runes) * 1000) / 1000 });

  // The final screen against the database (#293). 120 reasons were issued, ids 101 to 220, listed in that order, 50 to a page. Every case below changes ONE thing about a screen that
  // is exactly right, and the right screen is first shown to pass, so a failing case fails for the thing it changed.
  const ids = Array.from({ length: 120 }, (_, i) => String(101 + i));
  const textOf = (id) => `${unit} reason ${id}`;
  const dbTexts = (from, to) => Object.fromEntries(ids.slice(from - 1, to).map((id) => [id, textOf(id)]));
  const rowOf = (id, text = textOf(id)) => ({ id, reasonElements: 1, text });
  const screenOf = (from, to, over = {}) => ({ captured: true, nav: `Reasons ${from} to ${to} of 120`, navCount: 1, rows: ids.slice(from - 1, to).map((id) => rowOf(id)), ...over });
  const check = (screen, over = {}) => domTextAgreement(screen, { orderedIds: ids, displayPage: 50, texts: dbTexts(1, 120), skip: null, ...over });
  const good = check(screenOf(1, 50));
  assert.deepEqual(good, { checked: true, membershipOk: true, exact: true, nav: { from: 1, to: 50, total: 120 }, expectedRows: 50, rowsOnScreen: 50, rowsChecked: 50, skipped: 0, mismatched: 0, runesChecked: 50 * Array.from(textOf("101")).length, firstMismatchId: null, missingIds: [], unexpectedIds: [], duplicateIds: [], badElements: 0 });
  assert.equal(check(screenOf(101, 120)).exact, true, "the last, short page is a whole page");
  assert.equal(check(screenOf(51, 100)).exact, true);
  assert.deepEqual(expectedShownIds(screenOf(51, 100), ids, 50), { ok: true, from: 51, to: 100, total: 120, ids: ids.slice(50, 100) });
  const bad = (why, screen, over, want) => {
    const r = check(screen, over);
    assert.equal(r.checked, true, `${why}: a screen that was read and is wrong is checked`);
    assert.equal(r.exact, false, `${why}: not exact`);
    for (const [k, v] of Object.entries(want)) assert.deepEqual(r[k], v, `${why}: ${k}`);
    return r;
  };
  // The review's case: one correct reason and 49 unexpected ids. Nothing is ignored.
  const unexpected = screenOf(1, 50);
  unexpected.rows = [unexpected.rows[0], ...Array.from({ length: 49 }, (_, i) => rowOf(String(900 + i), "anything"))];
  bad("49 unexpected ids", unexpected, {}, { membershipOk: false, rowsOnScreen: 50, expectedRows: 50, rowsChecked: 1 });
  assert.equal(check(unexpected).unexpectedIds.length, 5, "the first five unexpected ids are named");
  assert.equal(check(unexpected).missingIds.length, 5);
  // A page that is another page of the same reasons than the pager line names.
  bad("rows of page 2 under page 1's line", screenOf(1, 50, { rows: ids.slice(50, 100).map((id) => rowOf(id)) }), {}, { membershipOk: false });
  // One row twice, one row less.
  const dup = screenOf(1, 50);
  dup.rows[49] = rowOf("101");
  bad("a duplicated row", dup, {}, { membershipOk: false, duplicateIds: ["101"], missingIds: ["150"], rowsOnScreen: 50 });
  const short = screenOf(1, 50);
  short.rows.pop();
  bad("a missing row", short, {}, { membershipOk: false, missingIds: ["150"], rowsOnScreen: 49, expectedRows: 50 });
  const longer = screenOf(1, 50);
  longer.rows.push(rowOf("151"));
  bad("an extra row", longer, {}, { membershipOk: false, unexpectedIds: ["151"], rowsOnScreen: 51 });
  const swapped = screenOf(1, 50);
  [swapped.rows[3], swapped.rows[4]] = [swapped.rows[4], swapped.rows[3]];
  bad("rows out of order", swapped, {}, { membershipOk: false, missingIds: [], unexpectedIds: [], duplicateIds: [] });
  bad("no rows", screenOf(1, 50, { rows: [] }), {}, { membershipOk: false, rowsOnScreen: 0, rowsChecked: 0 });
  bad("a row with no id", screenOf(1, 50, { rows: [{ reasonElements: 1, text: "x" }, ...screenOf(1, 50).rows.slice(1)] }), {}, { membershipOk: false });
  // The text: exactly the database's, to the rune, with nothing appended.
  const appended = screenOf(1, 50);
  appended.rows[7] = rowOf("108", `${textOf("108")} extra`);
  bad("appended text", appended, {}, { mismatched: 1, firstMismatchId: "108", membershipOk: true });
  const prefixed = screenOf(1, 50);
  prefixed.rows[7] = rowOf("108", `Keep text ${textOf("108")}`);
  bad("text with something before it", prefixed, {}, { mismatched: 1, firstMismatchId: "108" });
  const nocombining = screenOf(1, 50);
  nocombining.rows[0] = rowOf("101", textOf("101").replaceAll(cp(0x301), ""));
  bad("a missing combining mark", nocombining, {}, { mismatched: 1, firstMismatchId: "101" });
  const two = screenOf(1, 50);
  two.rows[2] = { id: "103", reasonElements: 2, text: null };
  bad("two reason elements", two, {}, { badElements: 1, mismatched: 1, firstMismatchId: "103" });
  const none = screenOf(1, 50);
  none.rows[2] = { id: "103", reasonElements: 0, text: null };
  bad("no reason element", none, {}, { badElements: 1, mismatched: 1 });
  const notext = screenOf(1, 50);
  notext.rows[2] = { id: "103", reasonElements: 1, text: undefined };
  bad("a reason element with no text", notext, {}, { mismatched: 1 });
  bad("no database text for a row", screenOf(1, 50), { texts: dbTexts(1, 49) }, { mismatched: 1, firstMismatchId: "150" });
  assert.equal(check(screenOf(1, 50), { texts: { ...dbTexts(1, 50), 101: 5 } }).mismatched, 1, "a database text that is not a string cannot be agreed with");
  // The reason the harness changed behind the page: the database's text now, or the one it held before, and nothing else.
  const skip = { id: "117", oldText: textOf("117") };
  const nowText = `${Array.from(textOf("117")).slice(0, -1).join("")}!`;
  const changed = { ...dbTexts(1, 50), 117: nowText };
  const withNow = screenOf(1, 50);
  withNow.rows[16] = rowOf("117", nowText);
  const withOld = screenOf(1, 50);
  assert.deepEqual([check(withNow, { texts: changed, skip }), check(withOld, { texts: changed, skip })].map((r) => [r.exact, r.rowsChecked, r.skipped, r.mismatched]), [[true, 49, 1, 0], [true, 49, 1, 0]]);
  const withThird = screenOf(1, 50);
  withThird.rows[16] = rowOf("117", "something else");
  bad("the changed reason shows a third text", withThird, { texts: changed, skip }, { mismatched: 1, skipped: 1, rowsChecked: 49, firstMismatchId: "117" });
  assert.equal(check(withOld, { texts: changed, skip: null }).mismatched, 1, "without the skip, the changed reason's old text is a mismatch: the skip is what allows it");
  // A skip id that is not on the page skips nothing.
  const offPage = check(screenOf(1, 50), { skip: { id: "200", oldText: "x" } });
  assert.deepEqual([offPage.exact, offPage.skipped, offPage.rowsChecked], [true, 0, 50]);
  // The pager line decides what is expected, and it must be a whole page of the issued reasons, in its finished form.
  const nav = (text, over = {}) => bad(`pager line '${text}'`, screenOf(1, 50, { nav: text, ...over }), {}, { membershipOk: false, nav: null });
  nav("Reasons 1 to 50 of at least 120");
  nav("Reasons 1 to 50 of 121");
  nav("Reasons 1 to 49 of 120");
  nav("Reasons 2 to 51 of 120");
  nav("Reasons 51 to 120 of 120");
  nav("Reasons 0 to 49 of 120");
  nav("Reasons 101 to 150 of 120");
  nav("Reasons 1 to 50");
  nav("Reasons 1 to 50 of 120", { navCount: 2 });
  nav("Reasons 1 to 50 of 120", { navCount: 0 });
  nav(null);
  assert.ok(check(screenOf(1, 50, { nav: "Reasons 1 to 50 of at least 120" })).why.includes("loading"));
  assert.equal(check(screenOf(1, 50, { nav: "Reasons 1 to 50 of 120" })).exact, true);
  assert.equal(expectedShownIds(screenOf(1, 50, { nav: "Reasons 3 to 52 of 120" }), ids, 50).ok, false);
  assert.equal(expectedShownIds(null, ids, 50).ok, false);
  assert.equal(shownRange("Reasons 1,001 to 1,050 of 1,200", 1, 1200, 50).ok, true, "the pager line groups thousands");
  // What cannot be compared at all is unchecked, never agreement.
  assert.deepEqual(check(null), { checked: false, why: "the rows on screen could not be read" });
  assert.deepEqual(check({ captured: false }), { checked: false, why: "the rows on screen could not be read" });
  assert.deepEqual(domTextAgreement(screenOf(1, 50), null), { checked: false, why: "no expected reason text was given" });
  assert.deepEqual(domTextAgreement(screenOf(1, 50), { orderedIds: ids, displayPage: 50, texts: null }), { checked: false, why: "no expected reason text was given" });
  // The judge's rule (qc1/lib.sh) is that rowsChecked + skipped is every row expected on the page, so a screen that skips what it should have compared is not exact.
  const partial = check(screenOf(1, 50));
  assert.equal(partial.rowsChecked + partial.skipped, partial.expectedRows);
}

// --- "Next changed reason" sampled repeatedly (#298) -----------------------------------------------------------------------------------------------------
// The fake panel gives each nextChanged press a time of its own (10, 11, 12 ... ms), so every figure below is arithmetic on known inputs. The host counters and the
// long-task time are faked on the same virtual clock; nothing here says anything about the real panel's speed.
{
  const pressTimes = () => {
    let k = 0;
    return (kind) => (kind === "nextChanged" ? 10 + k++ : 20);
  };
  const sampled = async (opts, extra = {}) => {
    clock = 0;
    const p = fakePanel(opts);
    return { p, r: await measurePanel(p, { expected: opts.expected, now, sleep, ...extra }) };
  };

  // the default is the single observation, unchanged: no sampling block and no load block
  {
    const { r } = await sampled({ expected: 1000 });
    assert.equal(r.nextChangedSampling, null);
    assert.equal(r.load, null);
    assert.equal(r.navigation.nextChanged.count, 1);
  }

  // a whole sampling run: 30 presses from three origins in turn, the first kept apart, moves to an origin not counted as navigation
  {
    const base = await sampled({ expected: 1000 });
    const { r, p } = await sampled({ expected: 1000, navMs: pressTimes() }, { nextChangedPresses: 30 });
    assert.equal(r.failed, null);
    const s = r.nextChangedSampling;
    assert.equal(s.requested, 30);
    assert.equal(s.performed, 30);
    assert.equal(r.navigation.nextChanged.count, 30, "every press is a nextChanged navigation sample");
    assert.equal(r.navigation.jump.count, base.r.navigation.jump.count, "moving to an origin is not a measured jump");
    assert.deepEqual(s.all, { count: 30, p50Ms: 24, p95Ms: 38, maxMs: 39 }, "10..39 ms: nearest-rank p50 is rank 15, p95 rank 29");
    assert.equal(s.firstPressMs, 10);
    assert.deepEqual(s.afterFirst, { count: 29, p50Ms: 25, p95Ms: 38, maxMs: 39 });
    assert.deepEqual(s.presses.slice(0, 4).map((x) => x.origin), ["first-page", "after-changed-page", "last-page", "first-page"]);
    assert.deepEqual(NEXT_CHANGED_ORIGINS, ["first-page", "after-changed-page", "last-page"]);
    assert.equal(s.changedOrdinal, 333);
    assert.equal(s.changedPage, 300);
    assert.deepEqual(Object.values(s.byOrigin).map((o) => o.count), [10, 10, 10]);
    assert.deepEqual(Object.values(s.byOrigin).map((o) => o.startOrdinal), [0, 350, 999]);
    assert.deepEqual(s.presses.map((x) => x.index), Array.from({ length: 30 }, (_, i) => i));
    assert.ok(s.presses.every((x, i) => i === 0 || x.atMs > s.presses[i - 1].atMs), "the presses are stamped in the order they ran");
    assert.equal(s.profile, null, "no profile unless asked for");
    // after the first press every press is a move to an origin and then the press
    const calls = p.state.navCalls;
    const firstPress = calls.indexOf("nextChanged");
    assert.deepEqual(calls.slice(firstPress + 1, firstPress + 5), ["goto", "nextChanged", "goto", "nextChanged"]);
    assert.equal(calls.filter((c) => c === "nextChanged").length, 30);
    // the refused press and the second walk are recorded exactly as in a single-press run
    assert.equal(r.press.status, "409");
    assert.equal(r.reload.unchosenAfterReload, 1);
  }

  // a review that fits one page has no origin to press from: it keeps the one press and says so instead of faking more
  {
    const { r } = await sampled({ expected: 50, navMs: pressTimes() }, { nextChangedPresses: 5 });
    assert.equal(r.nextChangedSampling.performed, 1);
    assert.equal(r.nextChangedSampling.requested, 5);
    assert.deepEqual(r.nextChangedSampling.origins, []);
  }
  // two pages: the changed reason's own page is never an origin
  {
    const { r } = await sampled({ expected: 100, navMs: pressTimes() }, { nextChangedPresses: 5 });
    assert.deepEqual(r.nextChangedSampling.origins, ["after-changed-page", "last-page"]);
    assert.equal(r.nextChangedSampling.performed, 5);
    assert.equal(r.nextChangedSampling.byOrigin["first-page"].count, 1, "only the first press is from the first page");
  }

  // a run that stops part-way keeps the presses it finished, and names the phase
  {
    let k = 0;
    const { r } = await sampled({ expected: 1000, navMs: (kind) => { if (kind === "nextChanged" && k++ === 5) throw new Error("the page stopped answering"); return 20; } }, { nextChangedPresses: 30 });
    assert.equal(r.failed.phase, "next-changed-presses");
    assert.equal(r.nextChangedSampling.performed, 5);
    assert.equal(r.nextChangedSampling.requested, 30);
    assert.equal(r.press.status, "409", "the refused press is still recorded");
    assert.equal(r.navigation.nextChanged.count, 5);
  }

  // load context: counters read at the edges of the run and of the presses; the deltas are arithmetic on those snapshots
  {
    let calls = 0;
    const snapshot = async () => {
      calls++;
      return { at: 1000 * calls, loadavg: { load1: 0.5 * calls, load5: 0, load15: 0, runnable: 1, tasks: 100 }, cpu: { total: 1000 * calls, steal: 50 * calls, iowait: 20 * calls, idle: 400 * calls }, cpus: 2, cpuPressureSomeAvg10: null };
    };
    const { r } = await sampled({ expected: 1000, navMs: pressTimes() }, { nextChangedPresses: 3, loadSnapshot: snapshot });
    assert.equal(calls, 4, "start, before the presses, after the presses, end");
    assert.deepEqual(Object.keys(r.load).sort(), ["afterPresses", "beforePresses", "end", "presses", "run", "start"]);
    assert.deepEqual(r.load.presses, loadDelta(r.load.beforePresses, r.load.afterPresses));
    assert.equal(r.load.presses.stealPercent, 5);
    assert.equal(r.load.presses.iowaitPercent, 2);
    assert.equal(r.load.presses.idlePercent, 40);
    assert.equal(r.load.presses.seconds, 1);
    assert.equal(r.load.presses.load1Start, 1);
    assert.equal(r.load.presses.load1End, 1.5);
    assert.equal(r.load.run.seconds, 3);
  }
  // a counter that cannot be read is an error entry, never a failed run and never a guess
  {
    const { r } = await sampled({ expected: 1000 }, { loadSnapshot: async () => { throw new Error("no /proc"); } });
    assert.equal(r.failed, null);
    assert.equal(r.load.start.error, "no /proc");
    assert.equal(r.load.presses, null);
  }

  // long-task time around each press (read just outside its interval); the first press has none
  {
    let ms = 0;
    let count = 0;
    const longTasks = async () => ({ ms: (ms += 3.5), count: ++count });
    clock = 0;
    const p = fakePanel({ expected: 1000, navMs: pressTimes() });
    p.longTasks = longTasks;
    const r = await measurePanel(p, { expected: 1000, now, sleep, nextChangedPresses: 4 });
    const s = r.nextChangedSampling;
    assert.equal(s.presses[0].longTaskMs, null);
    assert.deepEqual(s.presses.slice(1).map((x) => [x.longTaskMs, x.longTaskCount]), [[3.5, 1], [3.5, 1], [3.5, 1]]);
  }

  // the profiler: started before the repeated presses, stopped after, its file recorded; a profiler that will not start is an error entry and the presses still run
  {
    const seen = [];
    clock = 0;
    const p = fakePanel({ expected: 1000, navMs: pressTimes() });
    p.profile = async (action) => { seen.push(action); return action === "stop" ? { file: "/out/x.cpuprofile" } : { started: true }; };
    const r = await measurePanel(p, { expected: 1000, now, sleep, nextChangedPresses: 4, profileNextChanged: true });
    assert.deepEqual(seen, ["start", "stop"]);
    assert.deepEqual(r.nextChangedSampling.profile, { requested: true, file: "/out/x.cpuprofile", error: null });
    clock = 0;
    const q = fakePanel({ expected: 1000, navMs: pressTimes() });
    q.profile = async () => { throw new Error("no CDP"); };
    const r2 = await measurePanel(q, { expected: 1000, now, sleep, nextChangedPresses: 4, profileNextChanged: true });
    assert.equal(r2.nextChangedSampling.performed, 4);
    assert.equal(r2.nextChangedSampling.profile.file, null);
    assert.ok(r2.nextChangedSampling.profile.error.includes("did not start"));
    // not asked for: the driver's profiler is never touched
    seen.length = 0;
    clock = 0;
    const t = fakePanel({ expected: 1000, navMs: pressTimes() });
    t.profile = async (a) => { seen.push(a); return {}; };
    await measurePanel(t, { expected: 1000, now, sleep, nextChangedPresses: 4 });
    assert.deepEqual(seen, []);
  }

  // a number of presses that is not a whole number of at least 1 is refused before anything runs
  for (const bad of [0, -1, 1.5, "30", NaN]) {
    clock = 0;
    await assert.rejects(measurePanel(fakePanel({ expected: 1000 }), { expected: 1000, now, sleep, nextChangedPresses: bad }), /nextChangedPresses must be a whole number/);
  }
}

// --- the host counters' parsers (#298) ---------------------------------------------------------------------------------------------------------------------
assert.deepEqual(parseLoadavg("0.52 0.58 0.59 2/713 12345\n"), { load1: 0.52, load5: 0.58, load15: 0.59, runnable: 2, tasks: 713 });
assert.equal(parseLoadavg("not a loadavg"), null);
assert.equal(parseLoadavg(null), null);
const statText = "cpu  100 5 50 800 20 3 4 6 0 0\ncpu0 50 2 25 400 10 1 2 3 0 0\nintr 12345\n";
assert.deepEqual(parseProcStatCpu(statText), { user: 100, nice: 5, system: 50, idle: 800, iowait: 20, irq: 3, softirq: 4, steal: 6, total: 988 });
assert.equal(parseProcStatCpu("cpu  1 2 3 4 5 6 7\n"), null, "a kernel without the steal column gives no figure, not zero");
assert.equal(parseProcStatCpu("cpu  1 2 x 4 5 6 7 8\n"), null);
assert.equal(parseProcStatCpu(""), null);
assert.equal(parsePressureCpu("some avg10=1.25 avg60=0.50 avg300=0.20 total=12345\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"), 1.25);
assert.equal(parsePressureCpu(null), null);
{
  const a = { at: 0, loadavg: { load1: 1 }, cpu: { total: 1000, steal: 10, iowait: 0, idle: 500 } };
  const b = { at: 2500, loadavg: { load1: 3 }, cpu: { total: 2000, steal: 110, iowait: 50, idle: 900 } };
  assert.deepEqual(loadDelta(a, b), { seconds: 2.5, cpuTicks: 1000, stealPercent: 10, iowaitPercent: 5, idlePercent: 40, load1Start: 1, load1End: 3, load1Max: 3 });
  const none = loadDelta(a, { at: 3000 });
  assert.deepEqual([none.stealPercent, none.cpuTicks, none.load1End, none.load1Max], [null, null, null, null], "a missing side is missing, not zero");
  assert.equal(loadDelta(a, { ...b, cpu: { ...b.cpu, total: 1000 } }).stealPercent, null, "no ticks passed: no share");
  assert.equal(loadDelta(null, b).seconds, null);
}

console.log("qc1 panel fixtures: ok");
