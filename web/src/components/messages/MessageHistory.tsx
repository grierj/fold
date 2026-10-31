import { useEffect, useRef, useState } from "react";
import { Messages } from "../../messages-api";
import type { Message, MessageHistoryRecipient, MessageRecipientCounts, MessageRecipients } from "../../types";
import { HISTORY_WORDS } from "../../lib/messages";
import { Badge } from "../ui/Badge";
import { Button } from "../ui/Button";
import { FormError } from "../ui/Field";

// A sent message's history (M3.5b, PLAN 7.1): who it reached, counted by
// status and reason, a list of names with Teen badges and guardian-copy
// marks (never addresses, DEC-31), and "Try again" for the people a send
// failed for (DEC-131). A leader or host sees each person's status in
// words; only an admin gets each person's reason (the API decides).

const POLL_MS = 5000;

// A message the sender should pick up any moment: Send now, or a time
// reached or under a minute away (the browser's clock may be behind).
const due = (m: Message) => m.status === "scheduled" && !!m.scheduledAt && new Date(m.scheduledAt).getTime() <= Date.now() + 60_000;

// Messages that have a history to show.
const hasHistory = (m: Message) => m.status === "sending" || m.status === "sent" || m.status === "failed";

// useMessageHistory loads the history of a sent message, and while it is
// going out (or about to) checks every 5 seconds: the message, then its
// counts. A message that changed is handed to onMessage, which settles the
// page; the polling stops when it settles or the page goes away.
export function useMessageHistory(message: Message | null, onMessage: (m: Message) => void) {
  const [history, setHistory] = useState<MessageRecipients | null>(null);
  const [failed, setFailed] = useState(false);
  const [reload, setReload] = useState(0);
  const latest = useRef(onMessage);
  useEffect(() => { latest.current = onMessage; });

  const id = message?.id;
  const status = message?.status;
  const updatedAt = message?.updatedAt;
  const shows = !!message && hasHistory(message);
  const live = !!message && (message.status === "sending" || due(message));

  useEffect(() => {
    if (!id || (!shows && !live)) {
      setHistory(null);
      return;
    }
    let alive = true;
    let timer: number | undefined;
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
          setHistory(h);
          setFailed(false);
        }
      } catch {
        if (alive) setFailed(true);
      }
      if (alive && live) timer = window.setTimeout(() => void refresh(false), POLL_MS);
    }
    void refresh(true);
    return () => {
      alive = false;
      window.clearTimeout(timer);
    };
  }, [id, status, updatedAt, shows, live, reload]);

  return { history: shows ? history : null, failed, retryLoad: () => setReload((n) => n + 1) };
}

const people = (n: number) => `${n} ${n === 1 ? "person" : "people"}`;

// SendProblem sits under the page title when a send failed, or failed for
// some people: what happened in words, the error, and Try again as the
// view's one primary action when it can go out again.
export function SendProblem({ message, history, busy, error, onRetry }: {
  message: Message;
  history: MessageRecipients | null;
  busy: boolean;
  error: string;
  onRetry: () => void;
}) {
  const failedAll = message.status === "failed";
  // Every failed send, guardian copies included: Try again resends them all.
  const some = message.status === "sent" ? message.failedCount : 0;
  if (!failedAll && !some) return null;
  const canRetry = !!history?.canRetry;
  const title = failedAll ? "This message didn't go out" : `${people(some)} didn't get this`;
  const box = failedAll
    ? "border-danger-border bg-danger-subtle text-danger-on-subtle" // danger-ok: a send that failed is an error
    : "border-warning-border bg-warning-subtle text-warning-on-subtle";
  return (
    <div role="status" className={`space-y-3 rounded-lg border p-4 ${box}`}>
      <p className="font-semibold">{title}</p>
      {message.error && <p className="text-sm">{message.error}</p>}
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
// the guardian copies too (DEC-132).
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
// guardian's copy (DEC-19), and their status in words, with the reason for
// an admin.
function HistoryRow({ r }: { r: MessageHistoryRecipient }) {
  const missed = r.status === "missed";
  return (
    <li className="flex flex-wrap items-center gap-2 px-3 py-2">
      <span className={missed ? "text-text-muted" : "text-text"}>{r.name}</span>
      {r.teen && <Badge>Teen</Badge>}
      {r.copyOf && <Badge>Copy for {r.copyOf}</Badge>}
      <span className="text-text-muted">· {HISTORY_WORDS[r.status]}{r.reason ? `: ${r.reason}` : ""}</span>
    </li>
  );
}
