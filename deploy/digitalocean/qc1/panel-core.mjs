// QC-1 item 10, the rendered deletion panel: WHAT is measured and in WHAT order, with no browser in it. panel-measure.mjs drives
// the real panel in Chromium through panel-driver.mjs; this module takes any `driver` with the same methods, which is how
// qc1-fixtures.sh tests the sequence, the readiness rule, the click and navigation bookkeeping and the finding it assembles without a
// browser (a fake driver models the panel's states on a virtual clock).
//
// The panel (T07B-2 scaling F6/F7): the whole review is read page by page (100 requests a page) into a store; only 50 reasons are on
// the screen at a time ("Reasons 1 to 50 of at least 10,000" while the walk runs, "of 10,000" once it ended), with Next 50 / Previous 50,
// "Go to reason number", "Next reason without a choice" and "Next changed reason". Nothing is preselected.
//
// Readiness (engineering proposal of the design, replacing "every reason rendered"):
//   renderReadyMs = max( the walk ended with the displayed total equal to the seeded count and the Checking line gone,
//                        the first 50-reason page is rendered and its first radio accepts a real click and shows chosen within 200 ms )
// All pages loaded is part of readiness; all DOM nodes are not (they are O(50) by design).
//
// Clicks are REAL pointer clicks, timed in the page from the pointerdown's own timestamp to the frame after the commit (the Event Timing
// idea, at millisecond resolution, with no Playwright round trip inside the interval): 30 of them, in four places: the first page while
// the walk still runs, mid-list, the last page, and after the walk. Navigation is timed the same way from the click to the page or the
// focus the control promises. The harness chooses with the panel's own per-reason handler, one radio at a time; there is no bulk control.
//
// Budgets are judged in lib.sh (qc1_judge), not here. This module only records. Numbers it uses are the design's: 30 clicks, 200 ms for
// the first interactive click, 50 reasons a page.

export const DISPLAY_PAGE = 50;
export const FIRST_CLICK_MS = 200;
export const CLICKS_DEFAULT = 30;
export const PRE_WALK_CLICKS = 8;
export const MID_CLICKS = 8;
export const LAST_CLICKS = 7;

/** Nearest-rank percentile of a list of numbers (null for an empty list). */
export function percentile(samples, p) {
  if (!Array.isArray(samples) || samples.length === 0) return null;
  const s = [...samples].sort((a, b) => a - b);
  const rank = Math.ceil((p / 100) * s.length);
  return s[Math.min(s.length, Math.max(1, rank)) - 1];
}

export function summarize(samples) {
  if (samples.length === 0) return { count: 0, p50Ms: null, p95Ms: null, maxMs: null };
  return { count: samples.length, p50Ms: percentile(samples, 50), p95Ms: percentile(samples, 95), maxMs: Math.max(...samples) };
}

/** The pager line the panel shows. */
export function navLine(from, total, loading) {
  const last = Math.min(from + DISPLAY_PAGE, total);
  return `Reasons ${fmt(from + 1)} to ${fmt(last)} ${loading ? "of at least " : "of "}${fmt(total)}`;
}
const fmt = (n) => new Intl.NumberFormat("en-US").format(n);

/** Reads a pager line: {from (0-based), last, total, loading} or null. */
export function parseNavLine(text) {
  const m = /^Reasons ([\d,]+) to ([\d,]+) (of at least |of )([\d,]+)$/.exec((text ?? "").trim());
  if (!m) return null;
  const n = (s) => Number(s.replace(/,/g, ""));
  return { from: n(m[1]) - 1, last: n(m[2]), total: n(m[4]), loading: m[3] === "of at least " };
}

/** How many reasons the progress line says still need a choice, or null when it says none of that. */
export function parseUnchosen(text) {
  const m = /Choose Keep text or Blank text for (1 more reason|[\d,]+ more reasons)\./.exec(text ?? "");
  if (!m) return null;
  return m[1].startsWith("1 ") ? 1 : Number(m[1].replace(/,/g, "").split(" ")[0]);
}


const never = () => {
  throw new Error("the measurement was not given a clock");
};

const num = (v) => (typeof v === "number" && Number.isFinite(v) ? v : null);

/**
 * The F7 per-page figures over EVERY review page response the page received, in every walk (the first read, and the second read after a refused press), summarised from the
 * observations that COMPLETED. Each observation is persisted by the measurement process as it completes, so a run that stops part-way still has every page it saw, slow ones
 * included. A maximum is taken over the observations that have the figure; a figure that some observation lacks is COUNTED (absent), never replaced by zero and never allowed to
 * hide the maximum of the rest. `walks` repeats the summary per walk, so a slow request in the reload is seen as the reload's.
 */
export function summarizePages(list) {
  if (!Array.isArray(list) || list.length === 0) return null;
  const nums = (rows, k) => rows.map((p) => p?.[k]).filter((v) => num(v) !== null);
  const maxIf = (rows, k) => (nums(rows, k).length > 0 ? Math.max(...nums(rows, k)) : null);
  const sumIf = (rows, k) => (nums(rows, k).length > 0 ? nums(rows, k).reduce((a, b) => a + b, 0) : null);
  const absent = (rows, k) => rows.length - nums(rows, k).length;
  const incomplete = (rows) => rows.filter((p) => p?.completed === false);
  const summary = (rows) => ({
    // count is every response that ARRIVED (started); completed and incomplete reconcile it. An incomplete one has null figures, so it also shows in every absent count.
    count: rows.length,
    started: rows.length,
    completed: rows.length - incomplete(rows).length,
    incomplete: incomplete(rows).length,
    pendingAgeMsMax: nums(incomplete(rows), "ageMs").length > 0 ? Math.max(...nums(incomplete(rows), "ageMs")) : null,
    responseBytesMax: maxIf(rows, "bytes"),
    responseBytesTotal: sumIf(rows, "bytes"),
    requestMsMax: maxIf(rows, "requestMs"),
    requestMsP95: nums(rows, "requestMs").length > 0 ? percentile(nums(rows, "requestMs"), 95) : null,
    readMsMax: maxIf(rows, "pageReadMs"),
    readMsTotal: sumIf(rows, "pageReadMs"),
    parseMsMax: maxIf(rows, "pageParseMs"),
    parseMsTotal: sumIf(rows, "pageParseMs"),
    nodeParseMsMax: maxIf(rows, "nodeParseMs"),
    absent: { bytes: absent(rows, "bytes"), requestMs: absent(rows, "requestMs"), readMs: absent(rows, "pageReadMs"), parseMs: absent(rows, "pageParseMs"), nodeParseMs: absent(rows, "nodeParseMs") },
  });
  const walkNumbers = [...new Set(list.map((p) => p?.walk ?? 1))].sort((a, b) => a - b);
  return {
    ...summary(list),
    walks: walkNumbers.map((w) => ({ walk: w, ...summary(list.filter((p) => (p?.walk ?? 1) === w)) })),
    parseKind: "readMs is the page's own Response.text() and parseMs its own JSON.parse of that text, per page response, as web/src/api.ts does them; nodeParseMs is JSON.parse of the decoded body alone, in Node; requestMs is Playwright's own request timing",
  };
}

/** The difference between two marks taken by the phase hook (absolute WAL bytes, audit rows, audit table bytes, audit rows by action). */
export function phaseDelta(a, b) {
  if (!a || !b || num(a.wal) === null || num(b.wal) === null || typeof a.auditActions !== "object" || typeof b.auditActions !== "object" || a.auditActions === null || b.auditActions === null) return null;
  const keys = [...new Set([...Object.keys(a.auditActions), ...Object.keys(b.auditActions)])].sort();
  return {
    walBytes: b.wal - a.wal,
    auditActionsDelta: Object.fromEntries(keys.map((k) => [k, (b.auditActions[k] ?? 0) - (a.auditActions[k] ?? 0)])),
    auditRows: { before: a.auditRows ?? null, after: b.auditRows ?? null },
    // The rows contact_requests held at each end of the phase (background plus the scenario rows): the phase's own count, not one taken later.
    tableRows: { before: num(a.tableRows), after: num(b.tableRows) },
    auditBytes: { before: a.auditBytes ?? null, after: b.auditBytes ?? null, delta: num(a.auditBytes) !== null && num(b.auditBytes) !== null ? b.auditBytes - a.auditBytes : null },
    note: "cluster-wide counters read by the harness between the two marks; the marks are the phase boundaries, and the hook's own queries fall at the edges",
  };
}

export async function measurePanel(d, opts) {
  const { expected, now = never, sleep = never, pollMs = 50, timeoutMs = 900000, clicksTotal = CLICKS_DEFAULT } = opts;
  if (!Number.isInteger(expected) || expected < 1) throw new Error("expected must be the whole number of reasons seeded");
  const firstPageItems = Math.min(DISPLAY_PAGE, expected);
  const chosen = new Set(); // ordinals this run has chosen
  const clicks = { beforeWalkEnd: [], midList: [], lastPage: [], afterWalk: [] };
  const nav = { nextPage: [], jump: [], nextUnchosen: [], nextChanged: [] };
  const marks = {};
  const R = { pageObs: null, firstRows: null, firstPageMs: null, env: null, firstInteractiveMs: null, walkEndMs: null, walkTotal: null, walkStats: null, metrics: null, afterInteractions: null, press: null, reload: null, digest: null };
  let phase = "open";
  let failed = null;
  const mark = async (name) => {
    if (typeof d.phase === "function") marks[name] = await d.phase(name);
  };
  // Time zero is NOT taken here. Every interval of the first load is measured by the PAGE, on its own clock, from the pointerdown of the Delete-menu action
  // (menuAt) to the moment it saw the thing happen (first page rendered, walk ended, first interactive click completed), each timestamped where it occurred and
  // independently of this loop. So the phase hook, the sign-in, the navigation and the user search before the menu, and the clicks and navigation made while the
  // walk runs, add nothing to a figure and cannot postpone it.
  let timeline = null;

  async function until(what, test) {
    const limit = now() + timeoutMs;
    for (;;) {
      const v = await test();
      if (v) return v;
      if (now() > limit) throw new Error(`timed out waiting for ${what}`);
      await sleep(pollMs);
    }
  }
  const walkEnded = (s) => {
    const l = parseNavLine(s.navLine);
    return l !== null && !l.loading && l.total === expected && !s.checking;
  };

  try {
    await mark("open");
    await d.open();
    R.firstRows = (await until("the first page of reasons", async () => ((await d.snapshot()).rows >= firstPageItems ? { rows: (await d.snapshot()).rows } : null))).rows;
    R.env = await d.env();

    // The first radio must accept a real click and show chosen within 200 ms; a click that is slower does not count as ready, and the next
    // reason is tried (at most five), so a slow first frame cannot hide behind a later fast one.
    phase = "first-interactive";
    let next = 0;
    for (let attempt = 0; attempt < 5 && next < firstPageItems; attempt++, next++) {
      const ended = walkEnded(await d.snapshot());
      const ms = await d.chooseAt(next);
      chosen.add(next);
      (ended ? clicks.afterWalk : clicks.beforeWalkEnd).push(ms);
      if (ms <= FIRST_CLICK_MS) {
        R.firstInteractiveEnd = await d.lastClickEnd();
        next++;
        break;
      }
    }
    // More clicks on the first page while the walk is still going.
    while (next < firstPageItems && clicks.beforeWalkEnd.length < PRE_WALK_CLICKS && !walkEnded(await d.snapshot())) {
      clicks.beforeWalkEnd.push(await d.chooseAt(next));
      chosen.add(next++);
    }

    phase = "walk";
    await until("the walk to end", async () => walkEnded(await d.snapshot()));
    timeline = await d.timeline();
    R.walkTotal = parseNavLine((await d.snapshot()).navLine)?.total ?? null;
    await mark("walk-end");
    R.walkStats = await d.walkStats();
    try { R.pageObs = await d.pageObservations(); } catch { /* the final collection below tries again */ }
    // Heap, DOM and long tasks as they stand when the walk has ended: what the walk cost the page.
    R.metrics = await d.metrics();

    // Mid-list, last page, then back to the first page: navigation by "Go to reason number" is itself a measured navigation.
    phase = "interactions";
    const rowsFrom = async (start, count, group) => {
      let done = 0;
      const rows = (await d.snapshot()).rows;
      for (let i = 0; i < rows && done < count; i++) {
        if (chosen.has(start + i)) continue;
        group.push(await d.chooseAt(i));
        chosen.add(start + i);
        done++;
      }
      return done;
    };
    let cur = 0; // the display page's first ordinal
    const goto = async (ordinal) => {
      const start = Math.floor(ordinal / DISPLAY_PAGE) * DISPLAY_PAGE;
      nav.jump.push(await d.navigate("goto", ordinal + 1, { line: navLine(start, expected, false), heading: ordinal }));
      cur = start;
      return start;
    };
    const mid = Math.floor(expected / 2);
    const midStart = await goto(mid);
    await rowsFrom(midStart, MID_CLICKS, clicks.midList);
    const lastOrdinal = expected - 1;
    const lastStart = await goto(lastOrdinal);
    await rowsFrom(lastStart, LAST_CLICKS, clicks.lastPage);
    await goto(0);
    const countClicks = () => clicks.beforeWalkEnd.length + clicks.midList.length + clicks.lastPage.length + clicks.afterWalk.length;
    // Top up to the total on the first page (after the walk), so the total is always the design's 30 whatever the walk's speed.
    await rowsFrom(0, Math.max(0, clicksTotal - countClicks()), clicks.afterWalk);

    // Navigation: next and previous page, then "Next reason without a choice".
    // (cur is 0 here: the last goto went to the first page.) Next is offered only while another page exists, Previous only after one was left.
    const page = async (kind) => {
      cur += kind === "next" ? DISPLAY_PAGE : -DISPLAY_PAGE;
      nav.nextPage.push(await d.navigate(kind, null, { line: navLine(cur, expected, false) }));
    };
    if (expected > DISPLAY_PAGE) await page("next");
    if (expected > 2 * DISPLAY_PAGE) await page("next");
    if (cur > 0) await page("prev");
    await goto(0);
    const firstUnchosen = (fromOrdinal) => {
      for (let k = 0; k < expected; k++) {
        const at = (fromOrdinal + k) % expected;
        if (!chosen.has(at)) return at;
      }
      return -1;
    };
    for (let k = 0; k < 3; k++) {
      const at = firstUnchosen(0);
      if (at < 0) break;
      const start = Math.floor(at / DISPLAY_PAGE) * DISPLAY_PAGE;
      nav.nextUnchosen.push(await d.navigate("nextUnchosen", null, { line: navLine(start, expected, false), heading: at }));
      await goto(0);
    }
    R.afterInteractions = await d.metrics();
    await mark("interactions-end");

    // The changed-reason path: choose everything with the same per-reason handler, change one reason behind the page, press Delete (the server
    // refuses with reason_review_changed), let the panel read the review again, and use "Next changed reason". Nothing is deleted.
    R.press = { observed: false, reason: "the harness has no change hook" };
    if (d.canChange) {
      phase = "choose-all";
      const chooseAllStart = now();
      const all = await d.chooseAllRemaining(chosen);
      const chooseAllMs = Math.round(now() - chooseAllStart);
      const changedOrdinal = Math.floor(expected / 3);
      await goto(changedOrdinal);
      const changedId = await d.idAtRow(changedOrdinal - Math.floor(changedOrdinal / DISPLAY_PAGE) * DISPLAY_PAGE);
      await d.changeRow(changedId);
      phase = "press";
      // A fresh traversal is PROVEN, not assumed: the old walk's "ended" state is still on screen when Delete is pressed, so the second wait accepts
      // the new walk only after at least one whole walk's worth of page responses has arrived since the press (the count is the page's own, never reset).
      const pagesPerWalk = Math.ceil(expected / 100);
      const before = await d.pageResponseCount();
      await mark("press-start");
      const pressed = await d.press();
      await until("a fresh traversal after the refusal", async () => ((await d.pageResponseCount()) - before >= 1 ? true : null));
      await until("the second walk to end", async () => {
        const s = await d.snapshot();
        return (await d.pageResponseCount()) - before >= pagesPerWalk && walkEnded(s) ? s : null;
      });
      timeline = await d.timeline();
      const reloadMs = num(timeline && timeline.walk2DoneAt) !== null && num(timeline.pressAt) !== null ? Math.round(timeline.walk2DoneAt - timeline.pressAt) : null;
      const secondWalkPages = (await d.pageResponseCount()) - before;
      await mark("press-end");
      const after = await d.snapshot();
      const unchosen = parseUnchosen(after.progress);
      R.digest = typeof d.digest === "function" ? await d.digest() : null;
      await goto(0);
      const changedLat = await d.navigate("nextChanged", null, { heading: changedOrdinal });
      nav.nextChanged.push(changedLat);
      R.press = {
        observed: true,
        status: String(pressed.status),
        bodyBytes: pressed.bodyBytes,
        pressToRequestMs: pressed.pressToRequestMs,
        responseMs: pressed.responseMs ?? null,
        chooseAllMs,
        chosenByHarness: all.chosen,
      };
      R.reload = { reloadAfterRefusalMs: reloadMs, secondWalkPageResponses: secondWalkPages, unchosenAfterReload: unchosen, choicesSurvived: unchosen === 1 ? expected - 1 : null, changedOrdinal };
    }
  } catch (e) {
    // Keep what was measured: the phases that finished carry real figures (and real budget failures), and the failure names where the run stopped.
    failed = { phase, error: String(e && e.message ? e.message : e).slice(0, 400) };
  }

  const all = [...clicks.beforeWalkEnd, ...clicks.midList, ...clicks.lastPage, ...clicks.afterWalk];
  const navSummary = Object.fromEntries(Object.entries(nav).map(([k, v]) => [k, { ...summarize(v), observed: v.length > 0, samples: v }]));
  if (timeline === null) {
    try { timeline = await d.timeline(); } catch { /* the page is gone: every interval stays null */ }
  }
  // Every review page observation that COMPLETED, in every walk, whatever stopped the run: the measurement process persists each one as it finishes, so a slow page seen
  // before a failure (or in the reload) is still here. If the page is gone, what was already collected is used.
  let finalObs = R.pageObs ?? null;
  try {
    if (typeof d.pageObservations === "function") finalObs = await d.pageObservations();
  } catch { /* keep what the walk end collected */ }
  // The page-clock intervals, all from the Delete-menu action. A missing stamp is null (missing), never a guess from this loop's clock.
  const since = (stamp) => (timeline && num(timeline.menuAt) !== null && num(stamp) !== null ? Math.round((stamp - timeline.menuAt) * 10) / 10 : null);
  R.firstPageMs = since(timeline && timeline.firstPageAt);
  R.walkEndMs = since(timeline && timeline.walkDoneAt);
  R.firstInteractiveMs = R.firstInteractiveEnd !== undefined ? since(R.firstInteractiveEnd) : null;
  const readyCandidates = [R.walkEndMs, R.firstInteractiveMs];
  const preimageBytes = 35 + 40 * expected;
  const hasSubtle = R.env ? R.env.hasSubtle : null;
  // The digest path that ACTUALLY ran: crypto.subtle.digest was called over (at least) the whole preimage, or it was not and the bundled implementation ran.
  // (The self-test call the product makes is a few bytes and does not count towards the preimage.)
  const digest = R.digest
    ? {
        path: R.digest.subtleBytes >= preimageBytes ? "webcrypto" : "fallback",
        subtleCalls: R.digest.subtleCalls,
        subtleBytes: R.digest.subtleBytes,
        subtleMs: R.digest.subtleMs,
        preimageBytes,
        // What the environment predicts: crypto.subtle present and a preimage that fits the product's one-call limit. A fallback in a secure context is legitimate
        // (a preimage above 16 MiB, or the known-answer self-test failing), so a difference is RECORDED, never a failure.
        expectedPath: hasSubtle === true && preimageBytes <= 16 * 1024 * 1024 ? "webcrypto" : "fallback",
        // Press-to-request is an UPPER BOUND on building the digest: it also includes minting the attempt id and serialising the body.
        buildMs: R.press && num(R.press.pressToRequestMs) !== null ? R.press.pressToRequestMs : null,
        buildMsKind: "press-to-request, an upper bound (it includes minting the attempt id and serialising the body); subtleMs is the crypto.subtle time alone, measured on the webcrypto path only",
        matchesExpectation: null,
      }
    : null;
  if (digest) digest.matchesExpectation = digest.path === digest.expectedPath;
  const phases = {
    walk: phaseDelta(marks.open, marks["walk-end"]),
    interactions: phaseDelta(marks["walk-end"], marks["interactions-end"]),
    press: phaseDelta(marks["press-start"], marks["press-end"]),
  };
  return {
    expected,
    failed,
    secureContext: R.env ? R.env.secureContext : null,
    hasSubtle,
    // What the page would use, inferred from the environment; the observed path is digest.path.
    digestPathInferred: hasSubtle === null ? null : hasSubtle && preimageBytes <= 16 * 1024 * 1024 ? "webcrypto" : "fallback",
    digest,
    firstPage: { itemsRendered: R.firstRows, expectedItems: firstPageItems, firstPageMs: R.firstPageMs, firstInteractiveMs: R.firstInteractiveMs },
    walkEndMs: R.walkEndMs,
    timeline, // the page-clock stamps behind every interval above (null until they happened)
    walkTotal: R.walkTotal,
    pageRequests: R.walkStats ? R.walkStats.pageRequests : null,
    pageResponseBytes: R.walkStats ? R.walkStats.pageResponseBytes : null,
    pageResponseBytesKind: "decoded response body bytes",
    pages: summarizePages(finalObs),
    apiOrigin: R.walkStats ? R.walkStats.apiOrigin : null,
    renderReadyMs: readyCandidates.some((v) => v === null) ? null : Math.max(...readyCandidates),
    ...(R.metrics ?? { heapUsedBytes: null, heapAfterGcBytes: null, domNodes: null, longTaskMs: null, longTaskCount: null }),
    afterInteractions: R.afterInteractions
      ? { heapUsedBytes: R.afterInteractions.heapUsedBytes, heapAfterGcBytes: R.afterInteractions.heapAfterGcBytes, domNodes: R.afterInteractions.domNodes, longTaskMs: R.afterInteractions.longTaskMs, longTaskCount: R.afterInteractions.longTaskCount }
      : null,
    clicks: { ...summarize(all), beforeWalkEnd: clicks.beforeWalkEnd.length, midList: clicks.midList.length, lastPage: clicks.lastPage.length, afterWalk: clicks.afterWalk.length, samples: all },
    navigation: navSummary,
    press: R.press,
    reload: R.reload,
    phases,
  };
}
