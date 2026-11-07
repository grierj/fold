// Daemon-free fixtures for the QC-1 rendered-panel measurement (qc1/panel-core.mjs): a fake driver models the paginated deletion panel on a
// VIRTUAL clock (no browser, no sleeping), so the sequence, the readiness rule, the click and navigation bookkeeping and the finding the judge
// reads are tested here. Run by qc1-fixtures.sh; exits non-zero with a line on stderr on the first failure.
import assert from "node:assert/strict";
import { measurePanel, navLine, parseNavLine, parseUnchosen, percentile, summarize, DISPLAY_PAGE, FIRST_CLICK_MS } from "./qc1/panel-core.mjs";

let clock = 0;
const now = () => clock;
const sleep = async (ms) => {
  clock += ms;
};

// A model of the panel: the first page is on screen at firstPageAt, the walk ends at walkEndAt; a refused press starts a second walk only after
// restartDelay (the old walk's "ended" state is still on screen until then, as it is in the real panel), and the page responses it receives are counted.
function fakePanel({ expected, firstPageAt = 700, walkEndAt = 9000, clickMs = () => 12, navMs = () => 20, canChange = true, neverEnds = false, restartDelay = 800, walk2Ms = 2500, failAt = null, hook = true, subtleBytes = null }) {
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
    async open() {
      s.tOpen = clock;
      clock += 300;
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
      return {
        pageRequests: pages,
        pageResponseBytes: expected * 310,
        apiOrigin: "real",
        pageList: Array.from({ length: pages }, (_, i) => ({ bytes: 30000 + i, requestMs: 10 + (i % 7), nodeParseMs: 1.5, pageReadMs: 2, pageParseMs: 3 + (i % 3) })),
      };
    },
    async metrics() {
      return { heapUsedBytes: 9e6, heapAfterGcBytes: 6e6, domNodes: 4200, longTaskMs: 120, longTaskCount: 3 };
    },
  };
  if (hook) {
    // The phase hook: absolute WAL bytes and audit_logs (rows, bytes, rows by action) that grow with the virtual clock.
    d.phase = async (name) => {
      s.marks.push(name);
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
  assert.equal(r.pages.count, 10);
  assert.equal(r.pages.responseBytesMax, 30009);
  assert.equal(r.pages.requestMsMax, 16);
  assert.equal(r.pages.readMsMax, 2);
  assert.equal(r.pages.parseMsMax, 5);
  assert.equal(r.pages.parseMsTotal, 39);
  assert.equal(r.pages.readMsTotal, 20);
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
  assert.equal(r.pages, null);
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
await assert.rejects(measurePanel(fakePanel({ expected: 1 }), { expected: 5 }), /not given a clock/);

console.log("qc1 panel fixtures: ok");
