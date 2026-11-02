import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { ApiError, RoomRequests } from "../../api";
import { errorText } from "../../lib/errors";
import { roomName } from "../../lib/place";
import { dayOf, formatDayLong, formatTime, formatWhen } from "../../lib/when";
import type { RoomRequest } from "../../types";
import { ErrorNotice } from "../ErrorNotice";
import { Button } from "../ui/Button";
import { EmptyState } from "../ui/EmptyState";
import { Field, FormError, Textarea } from "../ui/Field";
import { Skeleton } from "../ui/Skeleton";
import { AlertCircleIcon } from "../icons";

const REASON_MAX = 500;
const WHO = "the people running it";

const dateCount = (n: number) => `${n} ${n === 1 ? "date" : "dates"}`;

// "Tuesday, November 10 at 7:00 PM" or "Every date from Tuesday, November 10 at 7:00 PM (40 dates)",
// in the viewer's zone.
function datesLine(r: RoomRequest): string {
  const first = `${formatDayLong(dayOf(r.firstStartsAt))} at ${formatTime(r.firstStartsAt)}`;
  return r.series ? `Every date from ${first} (${dateCount(r.dates)})` : first;
}

function busyLine(r: RoomRequest): string {
  if (r.dates === 1) return "This date is now taken.";
  return `${r.busyDates} of these dates ${r.busyDates === 1 ? "is" : "are"} now taken.`;
}

// What an approve did, in words.
function approvedMessage(room: string, title: string, res: { confirmed: number; overlapped: number; lost: string[] }): string {
  const total = res.confirmed + res.overlapped;
  const back = res.lost.length;
  const backText = `${dateCount(back)} went back to ${WHO}.`;
  if (total === 0) return `Nothing was booked for ${title}: ${backText}`;
  const base = res.overlapped > 0
    ? `Approved ${room} for ${title}: ${dateCount(total)}, ${res.overlapped} of them double-booked.`
    : `Approved ${room} for ${title}: ${dateCount(total)}.`;
  return back > 0 ? `${base} ${backText}` : base;
}

// The server's outcome words, in the verbs of this page.
const OUTCOME: Record<string, string> = { confirmed: "approved", rejected: "rejected", withdrawn: "withdrawn" };

// One open request: Approve and Reject. A request with busy dates explains
// what Approve does to them first (DEC-106), and Reject takes an optional
// reason. Either opens inline, not in a dialog.
function RequestRow({ request: r, onDone, onStale }: {
  request: RoomRequest;
  // The decision went through: the message to announce.
  onDone: (id: number, message: string) => void;
  // Someone else decided it first: the message to announce, and a fresh list.
  onStale: (id: number, message: string) => void;
}) {
  const [mode, setMode] = useState<"idle" | "approve" | "reject">("idle");
  // The button whose panel just closed, which takes the cursor back.
  const [returnTo, setReturnTo] = useState<"approve" | "reject" | null>(null);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const root = useRef<HTMLLIElement>(null);
  const room = roomName({ name: r.roomName, building: r.building });
  const title = r.eventTitle || "Church event";

  // An opened panel takes the cursor, in its reason field.
  useEffect(() => {
    if (mode !== "idle") root.current?.querySelector<HTMLElement>("[data-panel] textarea")?.focus();
  }, [mode]);
  useEffect(() => {
    if (mode !== "idle" || !returnTo) return;
    root.current?.querySelector<HTMLElement>(`[data-opener="${returnTo}"]`)?.focus();
    setReturnTo(null);
  }, [mode, returnTo]);

  function open(next: "approve" | "reject") {
    setReason("");
    setError(null);
    setMode(next);
  }
  function close() {
    if (mode !== "idle") setReturnTo(mode);
    setMode("idle");
  }

  async function run(action: () => Promise<string>) {
    setBusy(true);
    setError(null);
    try {
      onDone(r.id, await action());
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // Lost the race: not an error, someone else handled it.
        const raw = typeof e.data.resolution === "string" ? e.data.resolution : "";
        const outcome = OUTCOME[raw] ?? (raw || "handled");
        const by = typeof e.data.resolvedBy === "string" && e.data.resolvedBy ? e.data.resolvedBy : "someone";
        onStale(r.id, `Already ${outcome} by ${by}.`);
        return;
      }
      setError(errorText(e, mode === "reject" ? "We couldn't send that." : "We couldn't approve that."));
      setBusy(false);
    }
  }

  const approve = () => run(async () => approvedMessage(room, title, await RoomRequests.confirm(r.id, reason.trim() || undefined)));
  const reject = () => run(async () => {
    await RoomRequests.reject(r.id, reason.trim() || undefined);
    return `Rejected ${room} for ${title}. The people running it were told.`;
  });

  // Names for a screen reader, which hears these buttons in a list of cards.
  const forTitle = <>{" "}<span className="sr-only">for {title}</span></>;
  const idBase = `room-request-${r.id}`;
  return (
    <li ref={root} className="bg-surface rounded-lg border border-border p-6 space-y-3" data-request={r.id}>
      <div>
        <h3 className="font-bold wrap-anywhere"><Link to={`/events/${r.eventId}`} className="text-link">{title}</Link></h3>
        {r.groupName && <p className="text-sm text-text-muted wrap-anywhere">{r.groupName}</p>}
      </div>
      <p className="wrap-anywhere">Wants <span className="font-semibold">{room}</span></p>
      <p>{datesLine(r)}</p>
      <p className="text-sm text-text-muted">Asked by {r.requestedBy} · {formatWhen(r.createdAt)}</p>
      {r.busyDates > 0 && mode !== "approve" && (
        <p className="text-sm text-warning-on-subtle"><AlertCircleIcon className="mr-1 inline-block size-4 align-text-bottom" />{busyLine(r)}</p>
      )}
      {error && <FormError>{error}</FormError>}
      {mode === "idle" && (
        <div className="flex flex-wrap gap-3">
          <Button variant="primary" data-opener="approve" disabled={busy} onClick={() => (r.busyDates > 0 ? open("approve") : void approve())}>Approve{forTitle}</Button>
          <Button variant="danger-outline" data-opener="reject" disabled={busy} onClick={() => open("reject")}>Reject{forTitle}</Button>
        </div>
      )}
      {mode === "approve" && (
        <div data-panel className="space-y-3 rounded-md border border-warning-border bg-warning-subtle p-3">
          <p className="text-sm text-warning-on-subtle">
            <AlertCircleIcon className="mr-1 inline-block size-4 align-text-bottom" />{busyLine(r)} Without a reason, {r.busyDates === 1 ? "it goes" : "they go"} back to {WHO} to pick another room. Give a reason to book {r.busyDates === 1 ? "it" : "them"} anyway.
          </p>
          <Field id={`${idBase}-reason`} label="Double-book reason" optional hint={`Up to ${REASON_MAX} characters. It goes in the admin record.`}>
            <Textarea rows={2} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} />
          </Field>
          <div className="flex flex-wrap gap-3">
            <Button variant="primary" disabled={busy} onClick={() => void approve()}>{busy ? "Approving…" : <>Approve{forTitle}</>}</Button>
            <Button variant="secondary" disabled={busy} onClick={close}>Back{forTitle}</Button>
          </div>
        </div>
      )}
      {mode === "reject" && (
        <div data-panel className="space-y-3">
          <Field id={`${idBase}-why`} label="Tell them why" optional hint={`Up to ${REASON_MAX} characters.`}>
            <Textarea rows={2} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} />
          </Field>
          <div className="flex flex-wrap gap-3">
            <Button variant="danger-outline" disabled={busy} onClick={() => void reject()}>{busy ? "Sending…" : <>Reject and tell them{forTitle}</>}</Button>
            <Button variant="secondary" disabled={busy} onClick={close}>Cancel{forTitle}</Button>
          </div>
        </div>
      )}
    </li>
  );
}

// "Needs a room" on the admin Rooms tab (M2.1d, DEC-105): bookings waiting
// for an admin's yes or no, and nothing else. Dates that lost their room stay
// the leader's job. It shows while approval is on, requests are open, or the
// admin has just decided one. The room catalog comes in as `children`, so
// this is the one place that decides whether it needs a "Rooms" heading.
export function RoomRequestsList({ requests, needApproval, error, reload, onRemoved, children }: {
  // Null until loaded.
  requests: RoomRequest[] | null;
  needApproval: boolean | null;
  error: string | null;
  reload: () => void;
  // A request was decided; the list drops it.
  onRemoved: (id: number) => void;
  children: ReactNode;
}) {
  // n makes a repeat of the same words still count as a new announcement.
  const [status, setStatus] = useState({ text: "", n: 0 });
  const statusRef = useRef<HTMLParagraphElement>(null);

  // The row is gone after a decision, so the cursor moves to what happened.
  useEffect(() => {
    if (status.n > 0) statusRef.current?.focus();
  }, [status.n]);

  const say = (text: string) => setStatus((s) => ({ text, n: s.n + 1 }));
  const shown = (requests?.length ?? 0) > 0 || needApproval === true || error !== null || status.n > 0;
  return (
    <div className="space-y-8">
      {shown && <section aria-labelledby="room-requests-heading" className="space-y-3">
        <h2 id="room-requests-heading" className="text-xl font-bold">Needs a room</h2>
        <p key={status.n} ref={statusRef} role="status" tabIndex={-1} className={status.text ? "text-sm text-text-muted focus:outline-none" : "sr-only"}>{status.text}</p>
        {error && <ErrorNotice message={error} onRetry={reload} />}
        {!error && requests === null && <Skeleton kind="lines" />}
        {!error && requests !== null && requests.length === 0 && needApproval === true && (
          <EmptyState
            title="No bookings are waiting for approval."
            body="When a leader or host books a room, you'll approve or reject it here."
          />
        )}
        {!error && requests !== null && requests.length > 0 && (
          <ul className="space-y-3">
            {requests.map((r) => (
              <RequestRow
                key={r.id}
                request={r}
                onDone={(id, message) => { say(message); onRemoved(id); }}
                onStale={(id, message) => { say(message); onRemoved(id); reload(); }}
              />
            ))}
          </ul>
        )}
      </section>}
      <div className="space-y-3">
        {shown && <h2 className="text-xl font-bold">Rooms</h2>}
        {children}
      </div>
    </div>
  );
}
