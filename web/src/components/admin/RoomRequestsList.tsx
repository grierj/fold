import { useEffect, useRef, useState } from "react";
import { ApiError, RoomRequests } from "../../api";
import { errorText } from "../../lib/errors";
import { roomName } from "../../lib/place";
import { formatDayLong, formatWhen } from "../../lib/when";
import type { RoomRequest } from "../../types";
import { ErrorNotice } from "../ErrorNotice";
import { Button } from "../ui/Button";
import { EmptyState } from "../ui/EmptyState";
import { Field, FormError, Textarea } from "../ui/Field";
import { Skeleton } from "../ui/Skeleton";

const REASON_MAX = 500;

const dateCount = (n: number) => `${n} ${n === 1 ? "date" : "dates"}`;

// "1 date, Tuesday, November 10" or "Every date from Tuesday, November 10 (40 dates)".
function datesLine(r: RoomRequest): string {
  const first = formatDayLong(r.firstDate);
  return r.series ? `Every date from ${first} (${dateCount(r.dates)})` : `${dateCount(r.dates)}, ${first}`;
}

function busyLine(r: RoomRequest): string {
  if (r.dates === 1) return "This date is now taken.";
  return `${r.busyDates} of these dates ${r.busyDates === 1 ? "is" : "are"} now taken.`;
}

// What a confirm did, in words.
function confirmedMessage(room: string, res: { confirmed: number; overlapped: number; lost: string[] }): string {
  const total = res.confirmed + res.overlapped;
  const back = res.lost.length;
  const backText = `${dateCount(back)} went back to the leader.`;
  if (total === 0) return `Nothing was booked: ${backText}`;
  const base = res.overlapped > 0
    ? `Confirmed ${room} for ${dateCount(total)}, ${res.overlapped} of them double-booked.`
    : `Confirmed ${room} for ${dateCount(total)}.`;
  return back > 0 ? `${base} ${backText}` : base;
}

// One open request: Confirm and Not approved. A request with busy dates
// explains what Confirm does to them first (DEC-106), and Not approved takes
// an optional reason. Either opens inline, not in a dialog.
function RequestRow({ request: r, onDone, onStale }: {
  request: RoomRequest;
  // The decision went through: the message to announce.
  onDone: (id: number, message: string) => void;
  // Someone else decided it first: the message to announce, and a fresh list.
  onStale: (id: number, message: string) => void;
}) {
  const [mode, setMode] = useState<"idle" | "confirm" | "reject">("idle");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const panel = useRef<HTMLDivElement>(null);
  const room = roomName({ name: r.roomName, building: r.building });

  // An opened panel takes the cursor, in its reason field.
  useEffect(() => {
    if (mode !== "idle") panel.current?.querySelector<HTMLElement>("textarea")?.focus();
  }, [mode]);

  function open(next: "confirm" | "reject") {
    setReason("");
    setError(null);
    setMode(next);
  }

  async function run(action: () => Promise<string>) {
    setBusy(true);
    setError(null);
    try {
      onDone(r.id, await action());
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // Lost the race: not an error, someone else handled it.
        const outcome = typeof e.data.resolution === "string" ? e.data.resolution : "handled";
        const by = typeof e.data.resolvedBy === "string" && e.data.resolvedBy ? e.data.resolvedBy : "someone";
        onStale(r.id, `Already ${outcome} by ${by}.`);
        return;
      }
      setError(errorText(e, mode === "reject" ? "We couldn't send that." : "We couldn't confirm that."));
      setBusy(false);
    }
  }

  const confirm = () => run(async () => {
    const res = await RoomRequests.confirm(r.id, reason.trim() || undefined);
    return confirmedMessage(room, res);
  });
  const reject = () => run(async () => {
    await RoomRequests.reject(r.id, reason.trim() || undefined);
    return `Didn't approve ${room} for ${r.eventTitle || "the event"}. The leaders were told.`;
  });

  const idBase = `room-request-${r.id}`;
  return (
    <li className="bg-surface rounded-lg border border-border p-5 space-y-3" data-request={r.id}>
      <div>
        <h3 className="font-bold wrap-anywhere">{r.eventTitle || "Church event"}</h3>
        {r.groupName && <p className="text-sm text-text-muted wrap-anywhere">{r.groupName}</p>}
      </div>
      <p>Wants <span className="font-semibold">{room}</span></p>
      <p>{datesLine(r)}</p>
      <p className="text-sm text-text-muted">Asked by {r.requestedBy} · {formatWhen(r.createdAt)}</p>
      {r.busyDates > 0 && (
        <p className="text-sm text-warning-on-subtle"><span aria-hidden="true">⚠ </span>{busyLine(r)}</p>
      )}
      {error && <FormError>{error}</FormError>}
      {mode === "idle" && (
        <div className="flex flex-wrap gap-3">
          <Button variant="primary" disabled={busy} onClick={() => (r.busyDates > 0 ? open("confirm") : void confirm())}>Confirm</Button>
          <Button variant="danger-outline" disabled={busy} onClick={() => open("reject")}>Not approved</Button>
        </div>
      )}
      {mode === "confirm" && (
        <div ref={panel} className="space-y-3 rounded-md border border-warning-border bg-warning-subtle p-3">
          <p className="text-sm text-warning-on-subtle">
            {busyLine(r)} Without a reason, {r.busyDates === 1 ? "it goes" : "they go"} back to the leader to pick another room. Give a reason to book {r.busyDates === 1 ? "it" : "them"} anyway.
          </p>
          <Field id={`${idBase}-reason`} label="Double-book reason" optional hint={`Up to ${REASON_MAX} characters. It goes in the admin record.`}>
            <Textarea rows={2} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} />
          </Field>
          <div className="flex flex-wrap gap-3">
            <Button variant="primary" disabled={busy} onClick={() => void confirm()}>{busy ? "Confirming…" : "Confirm"}</Button>
            <Button variant="secondary" disabled={busy} onClick={() => setMode("idle")}>Back</Button>
          </div>
        </div>
      )}
      {mode === "reject" && (
        <div ref={panel} className="space-y-3">
          <Field id={`${idBase}-why`} label="Tell the leader why" optional hint={`Up to ${REASON_MAX} characters.`}>
            <Textarea rows={2} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} />
          </Field>
          <div className="flex flex-wrap gap-3">
            <Button variant="danger-outline" disabled={busy} onClick={() => void reject()}>{busy ? "Sending…" : "Send"}</Button>
            <Button variant="secondary" disabled={busy} onClick={() => setMode("idle")}>Cancel</Button>
          </div>
        </div>
      )}
    </li>
  );
}

// Whether the list has anything to say: approval is on, requests are open, or
// loading them failed.
export function roomRequestsShown(requests: RoomRequest[] | null, needApproval: boolean | null, error: string | null): boolean {
  return (requests?.length ?? 0) > 0 || needApproval === true || error !== null;
}

// "Needs a room" on the admin Rooms tab (M2.1d, DEC-105): bookings waiting
// for an admin's yes or no, and nothing else. Dates that lost their room stay
// the leader's job. It shows while approval is on or requests are open.
export function RoomRequestsList({ requests, needApproval, error, reload, onRemoved }: {
  // Null until loaded.
  requests: RoomRequest[] | null;
  needApproval: boolean | null;
  error: string | null;
  reload: () => void;
  // A request was decided; the list drops it.
  onRemoved: (id: number) => void;
}) {
  const [status, setStatus] = useState("");
  const statusRef = useRef<HTMLParagraphElement>(null);

  // The row is gone after a decision, so the cursor moves to what happened.
  useEffect(() => {
    if (status) statusRef.current?.focus();
  }, [status]);

  if (!roomRequestsShown(requests, needApproval, error) && !status) return null;

  return (
    <section aria-labelledby="room-requests-heading" className="space-y-3">
      <h2 id="room-requests-heading" className="text-xl font-bold">Needs a room</h2>
      {/* Always rendered so screen readers announce the text when it arrives. */}
      <p ref={statusRef} role="status" tabIndex={-1} className={status ? "text-sm text-text-muted focus:outline-none" : "sr-only"}>{status}</p>
      {error && <ErrorNotice message={error} onRetry={reload} />}
      {!error && requests === null && <Skeleton kind="lines" />}
      {!error && requests !== null && requests.length === 0 && (
        <EmptyState
          title="No bookings are waiting for approval."
          body="When a leader or host books a room, you'll confirm or decline it here."
        />
      )}
      {!error && requests !== null && requests.length > 0 && (
        <ul className="space-y-3">
          {requests.map((r) => (
            <RequestRow
              key={r.id}
              request={r}
              onDone={(id, message) => { setStatus(message); onRemoved(id); }}
              onStale={(id, message) => { setStatus(message); onRemoved(id); reload(); }}
            />
          ))}
        </ul>
      )}
    </section>
  );
}
