import { useEffect, useRef, useState } from "react";
import { Messages } from "../../messages-api";
import type { Message, MessageHistoryRecipient, MessageRecipientCounts, MessageRecipients } from "../../types";
import { failedLine, HISTORY_WORDS, historyLine, pushBadge } from "../../lib/messages";
import { Badge } from "../ui/Badge";
import { Button } from "../ui/Button";
import { FormError } from "../ui/Field";

// A sent message's history (M3.5b, PLAN 7.1): who it reached, counted by
// status and reason, a list of names with Teen badges (never addresses,
// DEC-31), and "Try again" for the people a send failed for (DEC-131). A
// leader or host sees each person's status in words and guardian copies
// as a count; only an admin gets each person's reason and each guardian's
// copy by name (the API decides, DEC-132).

const POLL_MS = 5000;
// Checks stop after this long with nothing changing; the page offers to
// check again.
const QUIET_MS = 30 * 60_000;
// The sender may pick a message up a little early by the browser's clock.
const EARLY_MS = 60_000;
// The longest single wait for a scheduled time; a later one waits again.
const MAX_WAIT_MS = 24 * 60 * 60_000;

// When a scheduled message is due to go out, by the browser's clock.
const dueAt = (m: Message) => (m.status === "scheduled" && m.scheduledAt ? new Date(m.scheduledAt).getTime() - EARLY_MS : null);

// Messages that have a history to show.
const hasHistory = (m: Message) => m.status === "sending" || m.status === "sent" || m.status === "failed";

// useMessageHistory loads the history of a sent message, and while it is
// going out (or due to) checks every 5 seconds: the message, then its
// counts. A message that changed is handed to onMessage, which settles the
// page. The checks wait while the tab is hidden, start when a scheduled
// time arrives with the page open, and stop when it settles, the page goes
// away, or nothing has changed for 30 minutes (stale). announce says once
// that it settled, for screen readers.
export function useMessageHistory(message: Message | null, onMessage: (m: Message) => void) {
  const [loaded, setLoaded] = useState<{ id: number; history: MessageRecipients } | null>(null);
  const [failed, setFailed] = useState(false);
  const [stale, setStale] = useState(false);
  const [announce, setAnnounce] = useState("");
  const [reload, setReload] = useState(0);
  const [clock, setClock] = useState(0);
  const latest = useRef(onMessage);
  useEffect(() => { latest.current = onMessage; });
  const wasLive = useRef<{ id: number; live: boolean } | null>(null);

  const id = message?.id;
  const status = message?.status;
  const updatedAt = message?.updatedAt;
  const shows = !!message && hasHistory(message);
  const at = message ? dueAt(message) : null;
  const live = !!message && (message.status === "sending" || (at !== null && at <= Date.now()));

  // A scheduled message that comes due while the page is open starts the checks.
  useEffect(() => {
    if (at === null || live) return;
    const timer = window.setTimeout(() => setClock((n) => n + 1), Math.min(Math.max(at - Date.now(), 0), MAX_WAIT_MS));
    return () => window.clearTimeout(timer);
  }, [at, live, clock]);

  useEffect(() => {
    setFailed(false);
    setStale(false);
    const settledNow = !!id && wasLive.current?.id === id && wasLive.current.live && !live;
    wasLive.current = id ? { id, live } : null;
    // Checks starting again (after Try again): the settle is said afresh.
    if (live) setAnnounce("");
    if (!id || (!shows && !live)) return;
    let alive = true;
    let timer: number | undefined;
    let paused = false;
    let lastChange = Date.now();
    let seen = "";
    async function refresh(first: boolean) {
      try {
        if (!first) {
          const { message: m } = await Messages.get(id!);
          if (!alive) return;
          if (m.status !== status || m.updatedAt !== updatedAt) {
            latest.current(m); // this effect runs again for the new state
            return;
          }
        }
        if (shows) {
          const h = await Messages.recipients(id!);
          if (!alive) return;
          const counts = JSON.stringify(h.counts);
          if (counts !== seen) {
            seen = counts;
            lastChange = Date.now();
          }
          setLoaded({ id: id!, history: h });
          setFailed(false);
          if (first && settledNow) setAnnounce(status === "failed" ? "This message didn't go out" : historyLine(h, { status: status!, sentAt: null }));
        }
      } catch {
        if (alive) setFailed(true);
      }
      next();
    }
    function next() {
      if (!alive || !live) return;
      if (Date.now() - lastChange > QUIET_MS) {
        setStale(true);
        return;
      }
      if (document.hidden) {
        paused = true;
        return;
      }
      timer = window.setTimeout(() => void refresh(false), POLL_MS);
    }
    function onVisible() {
      if (alive && paused && !document.hidden) {
        paused = false;
        // Time hidden isn't time with nothing changing.
        lastChange = Date.now();
        void refresh(false);
      }
    }
    document.addEventListener("visibilitychange", onVisible);
    void refresh(true);
    return () => {
      alive = false;
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [id, status, updatedAt, shows, live, reload]);

  return {
    history: shows && loaded && loaded.id === id ? loaded.history : null,
    failed,
    stale,
    announce,
    retryLoad: () => setReload((n) => n + 1),
  };
}

// SendProblem sits under the page title when a send failed, or failed for
// some people: what happened in words and the error, announced (an alert
// when nothing went out, a status when some did), then Try again as the
// view's one primary action when it can go out again. A leader's numbers
// match the list: people, with guardian copies apart. It waits for the
// history, so what is announced is said once and doesn't change.
export function SendProblem({ message, history, busy, error, onRetry }: {
  message: Message;
  history: MessageRecipients | null;
  busy: boolean;
  error: string;
  onRetry: () => void;
}) {
  const failedAll = message.status === "failed";
  if (!history || (!failedAll && !(message.status === "sent" && message.failedCount > 0))) return null;
  const canRetry = history.canRetry;
  const title = failedAll ? "This message didn't go out" : failedLine(history.counts.failed, history.counts.guardianCopiesFailed);
  const box = failedAll
    ? "border-danger-border bg-danger-subtle text-danger-on-subtle" // danger-ok: a send that failed is an error
    : "border-warning-border bg-warning-subtle text-warning-on-subtle";
  return (
    <div className={`space-y-3 rounded-lg border p-4 ${box}`}>
      <div role={failedAll ? "alert" : "status"} className="space-y-1">
        <p className="font-semibold">{title}</p>
        {message.error && <p className="text-sm">{message.error}</p>}
      </div>
      {canRetry && (
        <Button variant="primary" layout="w-full sm:w-auto" disabled={busy} onClick={onRetry}>
          {busy ? "Sending…" : "Try again"}
        </Button>
      )}
      {error && <FormError>{error}</FormError>}
    </div>
  );
}

// Everyone the list could show: the people it was for, and for an admin
// the guardian copies too (DEC-132). A push is on its person's row.
const totalOf = (c: MessageRecipientCounts, withCopies: boolean) =>
  c.sent + c.delivered + c.bounced + c.complained + c.failed + c.skipped + c.queued + (withCopies ? c.guardianCopies : 0);

// WhoGotIt is the "See who got it" list: the first 200 by name. Only an
// admin's has guardian copies (withCopies); a leader's counts them above.
export function WhoGotIt({ history, withCopies }: { history: MessageRecipients; withCopies: boolean }) {
  const total = totalOf(history.counts, withCopies);
  if (history.recipients.length === 0) return null;
  return (
    <details>
      <summary className="inline-flex min-h-11 cursor-pointer items-center text-sm font-semibold text-primary">See who got it</summary>
      <ul className="mt-2 divide-y divide-border rounded-md border border-border bg-surface text-sm">
        {history.recipients.map((r, i) => <HistoryRow key={`${r.name}-${i}`} r={r} />)}
        {history.more && <li className="px-3 py-2 text-text-muted">And {total - history.recipients.length} more</li>}
      </ul>
    </details>
  );
}

// One person: their name, a Teen badge (DEC-20), "Copy for Sam" on a
// guardian's copy (DEC-19), the email's status in words with the reason
// for an admin, and their push (M3.13d2) as a badge in words: "Push sent",
// "Push waiting until 8:00 AM". One row a person, however they got it.
function HistoryRow({ r }: { r: MessageHistoryRecipient }) {
  const missed = r.status === "missed";
  return (
    <li className="flex flex-wrap items-center gap-2 px-3 py-2">
      <span className={missed ? "text-text-muted" : "text-text"}>{r.name}</span>
      {r.teen && <Badge>Teen</Badge>}
      {r.copyOf && <Badge>Copy for {r.copyOf}</Badge>}
      <span className="text-text-muted">· {HISTORY_WORDS[r.status]}{r.reason ? `: ${r.reason}` : ""}</span>
      {r.push && <Badge>{pushBadge(r.push)}</Badge>}
    </li>
  );
}
