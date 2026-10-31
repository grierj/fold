import { useCallback, useEffect, useRef, useState } from "react";
import { SeriesRooms, Site } from "../api";
import { useAuth } from "../auth";
import { errorText } from "../lib/errors";
import { roomName } from "../lib/place";
import type { OccurrenceRoom, SeriesBooking, SeriesRoom } from "../types";
import { Field, FormError, Select, Textarea } from "./ui/Field";
import { Button } from "./ui/Button";
import { Skeleton } from "./ui/Skeleton";

const NONE = "none";
const REASON_MAX = 500;

// "1 date", "8 dates".
function dateCount(n: number): string {
  return `${n} ${n === 1 ? "date" : "dates"}`;
}

// A room as the whole-series picker words it, like RoomField's option: seats,
// then how many of the dates something else holds it on, then a size warning.
// `dates` is how many dates the booking would cover.
export function seriesRoomLabel(room: SeriesRoom, dates: number): string {
  const seats = room.capacity === null ? "" : ` (${room.capacity} ${room.capacity === 1 ? "seat" : "seats"})`;
  let busy = "";
  if (room.busyDates > 0) busy = dates <= 1 ? " — in use at this time" : ` — in use on ${room.busyDates} of ${dates} dates`;
  return `${roomName(room)}${seats}${busy}${room.tooSmall ? " — may be too small" : ""}`;
}

// What picking a room will do to dates it is busy on, said before it is booked.
function busyNote(room: SeriesRoom, dates: number): string | null {
  if (room.busyDates <= 0) return null;
  const name = roomName(room);
  const listed = "the busy dates will be listed under “Dates that need a room”.";
  if (dates <= 1) return `${name} is in use at this time, so the date will need a room; ${listed}`;
  if (room.busyDates >= dates) return `${name} is in use on all ${dates} dates, so none can be booked yet; ${listed}`;
  return `${name} is in use on ${room.busyDates} of ${dates} dates. It will be booked on the other ${dates - room.busyDates}, and ${listed}`;
}

// What a series booking did, in words. `over` is how many of the booked dates
// an admin double-booked.
function resultMessage(res: SeriesBooking, over = 0): string {
  const parts: string[] = [];
  if (res.defaultRoom === null) parts.push("Series room released.");
  else if (res.booked > 0) {
    parts.push(over > 0
      ? `Booked for ${dateCount(res.booked)}, ${over} of them over another booking.`
      : `Booked for ${dateCount(res.booked)}.`);
  }
  if (res.defaultRoom !== null && res.pending > 0) parts.push(`Sent to an admin for approval: ${dateCount(res.pending)}.`);
  const n = res.lost.length;
  if (n > 0) {
    parts.push(`${dateCount(n)} ${n === 1 ? "is" : "are"} busy; pick another room for ${n === 1 ? "it" : "them"} under “Dates that need a room” on the event page.`);
  }
  if (res.kept > 0) {
    parts.push(`${dateCount(res.kept)} ${res.kept === 1 ? "keeps" : "keep"} the room picked for ${res.kept === 1 ? "it" : "them"}.`);
  }
  if (parts.length === 0) parts.push("There are no upcoming dates to book.");
  return parts.join(" ");
}

// The Room control of "Edit series" for a recurring series (M2.1c, DEC-95:
// beside Location, not under More options). Booking is its own action and
// never waits for the form's Save. A date that is busy keeps its
// place on the event's "Dates that need a room" list (DEC-103). It shows
// nothing while rooms are off.
// Unlike RoomField it does not book when the select changes, but on its own
// button: arrow keys on a closed select fire change, and one keypress must
// never book or release dozens of dates.
export function SeriesRoomField({ eventId, idPrefix, onChanged }: {
  eventId: number;
  idPrefix: string;
  // The series room was booked, changed or released: the page behind may show new places.
  onChanged?: () => void;
}) {
  const { isAdmin } = useAuth();
  const id = `${idPrefix}-series-room`;
  const reasonId = `${id}-reason`;
  const [enabled, setEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  // The select's choice, not yet booked.
  const [pick, setPick] = useState(NONE);
  // How many upcoming dates have a room that holds nothing (DEC-103).
  const [needsCount, setNeedsCount] = useState(0);
  // Upcoming dates waiting on an admin's approval of the series room (M2.1d).
  const [pendingCount, setPendingCount] = useState(0);
  const [rooms, setRooms] = useState<SeriesRoom[]>([]);
  const [dates, setDates] = useState(0);
  const [current, setCurrent] = useState<OccurrenceRoom | null>(null);
  const [status, setStatus] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // An admin's answer for dates that are busy: the room to resend with a reason.
  const [pending, setPending] = useState<{ roomId: number; dates: number } | null>(null);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    // A church with rooms off, or a site we can't read, sees no rooms control at all.
    let site;
    try {
      site = await Site.get();
    } catch {
      if (alive.current) { setEnabled(false); setLoading(false); }
      return;
    }
    if (!alive.current) return;
    if (!site.roomsEnabled) {
      setEnabled(false);
      setLoading(false);
      return;
    }
    setEnabled(true);
    try {
      const res = await SeriesRooms.list(eventId);
      if (!alive.current) return;
      setRooms(res.rooms);
      setDates(res.dates);
      setCurrent(res.defaultRoom);
      setPick(res.defaultRoom ? String(res.defaultRoom.id) : NONE);
      setNeedsCount(res.needsRoom.length);
      setPendingCount(res.pending);
    } catch (e) {
      if (alive.current) setLoadError(errorText(e, "We couldn't load the rooms."));
    } finally {
      if (alive.current) setLoading(false);
    }
  }, [eventId]);
  useEffect(() => { void load(); }, [load]);

  // An admin's busy-dates answer moves focus to the reason it asks for.
  useEffect(() => {
    if (pending) document.getElementById(reasonId)?.focus();
  }, [pending, reasonId]);

  async function send(roomId: number | null, overlapReason?: string, over = 0) {
    setBusy(true);
    setError(null);
    setStatus("");
    try {
      const res = await SeriesRooms.set(eventId, overlapReason ? { roomId, overlapReason } : { roomId });
      if (!alive.current) return;
      setCurrent(res.defaultRoom);
      setPick(res.defaultRoom ? String(res.defaultRoom.id) : NONE);
      setNeedsCount(res.defaultRoom ? res.lost.length : 0);
      setPendingCount(res.defaultRoom ? res.pending : 0);
      setStatus(resultMessage(res, over));
      setReason("");
      setReasonError(null);
      // Only an admin may double-book what stayed busy, and only with a reason.
      setPending(isAdmin && roomId !== null && !overlapReason && res.lost.length > 0 ? { roomId, dates: res.lost.length } : null);
      onChanged?.();
    } catch (e) {
      if (!alive.current) return;
      setPending(null);
      setError(errorText(e, "We couldn't update the room."));
    } finally {
      if (alive.current) setBusy(false);
    }
  }

  function choose(value: string) {
    setPick(value);
    setPending(null);
    setReasonError(null);
  }

  function apply() {
    if (busy || pick === value) return;
    if (pick === NONE) {
      void send(null);
      return;
    }
    const room = rooms.find((r) => String(r.id) === pick);
    if (room) void send(room.id);
  }

  function bookAnyway() {
    if (!pending) return;
    const why = reason.trim();
    if (!why) {
      setReasonError("Say why you are double-booking this room.");
      document.getElementById(reasonId)?.focus();
      return;
    }
    if (why.length > REASON_MAX) {
      setReasonError(`Use ${REASON_MAX} characters or fewer.`);
      document.getElementById(reasonId)?.focus();
      return;
    }
    setReasonError(null);
    void send(pending.roomId, why, pending.dates);
  }

  if (!enabled && !loadError) return null;

  // A series room an admin set that this person can't list (a restricted room) still shows by name.
  const hidden = current && !rooms.some((r) => r.id === current.id) ? current : null;
  const value = current ? String(current.id) : NONE;
  const chosen = pick === NONE ? null : (rooms.find((r) => String(r.id) === pick) ?? null);

  return (
    <div className="space-y-2">
      {loading && <Skeleton kind="lines" count={1} label="Loading rooms…" />}
      {loadError && (
        <div className="space-y-2">
          <FormError>{loadError}</FormError>
          <Button variant="secondary" onClick={() => void load()}>Try again</Button>
        </div>
      )}
      {!loading && !loadError && (
        <>
          <Field
            id={id}
            label="Room for every date"
            optional
            hint="Choose a room, then book it for every upcoming date. A date you gave its own room keeps it. “No series room” releases the rooms it booked."
          >
            <Select value={pick} aria-busy={busy || undefined} onChange={(e) => choose(e.target.value)}>
              <option value={NONE}>No series room</option>
              {hidden && <option value={String(hidden.id)}>{roomName(hidden)}</option>}
              {rooms.map((r) => (
                <option key={r.id} value={String(r.id)}>{seriesRoomLabel(r, dates)}</option>
              ))}
            </Select>
          </Field>
          <Button variant="secondary" disabled={busy || pick === value} onClick={apply}>
            {pick === NONE ? "Stop the series room" : "Book for every date"}
          </Button>
          <div role="status" className="space-y-2 empty:hidden">
            {chosen && pick !== value && busyNote(chosen, dates) && (
              <p className="text-sm text-warning-on-subtle"><span aria-hidden="true">⚠ </span>{busyNote(chosen, dates)}</p>
            )}
            {status && <p className="text-sm text-text-muted">{status}</p>}
            {!status && current && pendingCount > 0 && (
              <p className="text-sm text-text-muted">Waiting for an admin to approve this room for {dateCount(pendingCount)}.</p>
            )}
            {current && dates > 0 && needsCount >= dates && (
              <p className="text-sm text-warning-on-subtle"><span aria-hidden="true">⚠ </span>Not booked on any date yet: every date is busy.</p>
            )}
          </div>
          {error && <FormError>{error}</FormError>}
          {pending && (
            <div className="space-y-2 rounded-md border border-warning-border bg-warning-subtle p-3">
              <p role="alert" className="text-sm text-warning-on-subtle">
                <span aria-hidden="true">⚠ </span>
                {pending.dates === 1 ? "1 date is" : `${pending.dates} dates are`} in use. Give a reason to book {pending.dates === 1 ? "it" : "them"} anyway.
              </p>
              <Field id={reasonId} label="Reason for double-booking" error={reasonError}>
                <Textarea rows={2} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} />
              </Field>
              <Button variant="secondary" disabled={busy} onClick={bookAnyway}>Book anyway</Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
