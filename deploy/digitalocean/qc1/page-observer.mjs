// Every review page response the browser receives, observed from the moment it ARRIVES and persisted as it completes, through every walk (the first read and the
// read again after a refused press).
//
//   - A response is recorded as STARTED the moment its headers arrive (a "start" line is appended at once: its sequence number, walk and status), and as DONE when its
//     body has been read (a "done" line with the bytes, the request time and the Node-side parse). A page that never finishes is therefore still in the file, as a start
//     with no done, and the summary rebuilt from the file (qc1_pages_from_jsonl in lib.sh) shows it as an explicit missing entry, not as nothing.
//   - drain(timeoutMs) waits for the responses still in flight; the ones that do not complete in time are marked timed out (a "timeout" line with how long they had been
//     in flight). observationsWithMissing() returns every response that ever arrived, in every walk: completed ones with their figures, and the rest as explicit entries
//     with completed:false, null figures and the age they had reached.
//   - reconcile() counts started, completed and pending per walk, so the final collection can prove every response of the first read and of the reload is accounted for.
//
// `r` is a Playwright Response (url(), status(), body(), request().timing()); the fixtures give it a fake. `append(line)` persists a line; `sleep(ms)` waits.
import { countRunes, totalBodyBytes, walkOrigin } from "./response-accounting.mjs";

const PAGE_PATH = "/erasure-reasons/page";

export function pageObserver({ origin, append = () => {}, sleep = (ms) => new Promise((r) => setTimeout(r, ms)), clock = () => performance.now() }) {
  const done = [];
  const inflight = new Map(); // seq -> { rec, startedAt, timedOut }
  const started = {};
  const finished = {};
  let seq = 0;
  let walk = 1;

  async function onResponse(r) {
    const u = new URL(r.url());
    if (!u.pathname.endsWith(PAGE_PATH)) return;
    const rec = { seq: seq++, walk, status: r.status(), foreignOrigin: u.origin !== origin, bytes: null, runes: null, requestMs: null, nodeParseMs: null };
    started[rec.walk] = (started[rec.walk] ?? 0) + 1;
    inflight.set(rec.seq, { rec, startedAt: clock(), timedOut: false });
    append(JSON.stringify({ seq: rec.seq, walk: rec.walk, event: "start", status: rec.status, foreignOrigin: rec.foreignOrigin }) + "\n");
    try {
      const body = await r.body();
      const t = r.request().timing();
      const p0 = clock();
      let parsed = true;
      try {
        JSON.parse(body.toString("utf8"));
      } catch {
        parsed = false;
      }
      rec.nodeParseMs = parsed ? Math.round((clock() - p0) * 100) / 100 : null;
      rec.requestMs = t && t.responseEnd >= 0 && t.startTime >= 0 ? Math.round(t.responseEnd * 100) / 100 : null;
      rec.bytes = body.length;
      rec.runes = countRunes(body);
    } catch {
      // Keep the elapsed lower bound even if the body fails after a timeout.
      rec.completed = false;
      rec.ageMs = Math.round(clock() - inflight.get(rec.seq).startedAt);
    }
    inflight.delete(rec.seq);
    done.push(rec);
    finished[rec.walk] = (finished[rec.walk] ?? 0) + 1;
    append(JSON.stringify({ ...rec, event: "done" }) + "\n");
  }

  const missingEntry = ({ rec, startedAt }) => ({
    seq: rec.seq, walk: rec.walk, status: rec.status, foreignOrigin: rec.foreignOrigin, bytes: null, runes: null, requestMs: null, nodeParseMs: null, completed: false, ageMs: Math.round(clock() - startedAt),
  });

  return {
    onResponse,
    /** The next response belongs to walk n (called when Delete is pressed again). */
    startWalk(n) {
      walk = n;
    },
    /** Every review page response that has ARRIVED, completed or not: a fresh traversal shows as this growing. */
    responses: () => seq,
    /** The observations that have completed, in arrival order. */
    observations: () => [...done].sort((a, b) => a.seq - b.seq),
    /** Every response that ever arrived, in every walk, in arrival order: the completed ones, and an explicit entry (completed:false, null figures, age) for each still in flight. */
    observationsWithMissing: () => [...done, ...[...inflight.values()].map(missingEntry)].sort((a, b) => a.seq - b.seq),
    /** Started, completed and pending per walk. */
    reconcile() {
      const walks = [...new Set([...Object.keys(started), ...Object.keys(finished)])].map(Number).sort((a, b) => a - b);
      return walks.map((w) => ({ walk: w, started: started[w] ?? 0, completed: finished[w] ?? 0, pending: (started[w] ?? 0) - (finished[w] ?? 0) }));
    },
    /** Waits up to timeoutMs for the responses in flight; the rest are marked timed out (persisted once) and stay as missing entries. Returns how many are still pending. */
    async drain(timeoutMs = 10000) {
      for (let waited = 0; inflight.size > 0 && waited < timeoutMs; waited += 50) await sleep(50);
      for (const e of inflight.values()) {
        if (!e.timedOut) {
          e.timedOut = true;
          append(JSON.stringify({ seq: e.rec.seq, walk: e.rec.walk, event: "timeout", ageMs: Math.round(clock() - e.startedAt) }) + "\n");
        }
      }
      return inflight.size;
    },
    /** The first walk's figures once its responses have all completed (waits up to waitMs for stragglers; anything still pending makes the figures null). */
    async walkStats(n = 1, waitMs = 10000) {
      for (let waited = 0; (finished[n] ?? 0) < (started[n] ?? 0) && waited < waitMs; waited += 50) await sleep(50);
      const rows = done.filter((p) => p.walk === n);
      const complete = (finished[n] ?? 0) === (started[n] ?? 0);
      const sizes = rows.map((p) => p.bytes);
      const bad = rows.filter((p) => p.status !== 200).length;
      const foreignOrigin = rows.filter((p) => p.foreignOrigin).length;
      const bytes = complete ? totalBodyBytes(sizes) : null;
      return { pageRequests: started[n] ?? 0, pageResponseBytes: bytes, apiOrigin: complete ? walkOrigin({ requests: started[n] ?? 0, bad, foreignOrigin }, bytes, sizes.length) : "unverified" };
    },
  };
}
