// Every review page response the browser receives, observed AS IT COMPLETES and persisted at once, through every walk (the first read and the read again after a
// refused press). The measurement process used to keep a list that was summarised only after the first walk, so a run that stopped part-way, or a slow request
// in the reload, left nothing behind. Here each observation is appended to a file (one JSON line) the moment its body has been read; the summary can be rebuilt
// from the file even if the process died (qc1_pages_from_jsonl in lib.sh does it), and a page whose body could not be read is recorded with null fields (absent),
// never dropped and never zero.
//
// `r` is a Playwright Response (url(), status(), body(), request().timing()); the fixtures give it a fake. `append(line)` persists a line; `sleep(ms)` waits.
import { totalBodyBytes, walkOrigin } from "./response-accounting.mjs";

const PAGE_PATH = "/erasure-reasons/page";

export function pageObserver({ origin, append = () => {}, sleep = (ms) => new Promise((r) => setTimeout(r, ms)), clock = () => performance.now() }) {
  const done = [];
  const started = {};
  const finished = {};
  let seq = 0;
  let walk = 1;

  async function onResponse(r) {
    const u = new URL(r.url());
    if (!u.pathname.endsWith(PAGE_PATH)) return;
    const rec = { seq: seq++, walk, status: r.status(), foreignOrigin: u.origin !== origin, bytes: null, requestMs: null, nodeParseMs: null };
    started[walk] = (started[walk] ?? 0) + 1;
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
    } catch {
      /* the body could not be read: the fields stay null (absent) */
    }
    done.push(rec);
    finished[rec.walk] = (finished[rec.walk] ?? 0) + 1;
    append(JSON.stringify(rec) + "\n");
  }

  return {
    onResponse,
    /** The next response belongs to walk n (called when Delete is pressed again). */
    startWalk(n) {
      walk = n;
    },
    /** Every review page response seen so far, completed or not: a fresh traversal shows as this growing. */
    responses: () => seq,
    /** The observations that have completed, in arrival order. */
    observations: () => [...done].sort((a, b) => a.seq - b.seq),
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
