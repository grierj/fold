import { useEffect, useState } from "react";
import { SeriesRooms } from "../api/rooms";
import { useAuth } from "../auth";
import { errorText } from "../lib/errors";
import { roomName } from "../lib/place";
import { busyDatesNote, NO_ROOM, seriesBookingMessage, seriesRoomLabel } from "../lib/roomLabels";
import { count } from "../lib/plural";
import type { OccurrenceRoom, SeriesRoom, RoomSuggestions } from "../types";
import { Field, FormError, Select } from "./ui/Field";
import { Button } from "./ui/Button";
import { DoubleBookPrompt, useOverlapReason } from "./DoubleBookPrompt";
import { RoomOptions } from "./RoomOptions";
import { RoomLoadStatus, useRoomPicker } from "./useRoomPicker";
import { AlertCircleIcon } from "./icons";

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
  // The select's choice, not yet booked.
  const [pick, setPick] = useState(NO_ROOM);
  // How many upcoming dates have a room that holds nothing (DEC-103).
  const [needsCount, setNeedsCount] = useState(0);
  // Upcoming dates waiting on an admin's approval of the series room (M2.1d).
  const [pendingCount, setPendingCount] = useState(0);
  const [rooms, setRooms] = useState<SeriesRoom[]>([]);
  const [suggestions, setSuggestions] = useState<RoomSuggestions | null>(null);
  const [dates, setDates] = useState(0);
  const [current, setCurrent] = useState<OccurrenceRoom | null>(null);
  const [status, setStatus] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // An admin's answer for dates that are busy: the room to resend with a reason.
  const [pending, setPending] = useState<{ roomId: number; dates: number } | null>(null);
  const overlap = useOverlapReason(reasonId);

  // A church with rooms off, or a site we can't read, sees no rooms control at all.
  const { enabled, alive, list, loading, loadError } = useRoomPicker(() => SeriesRooms.list(eventId), [eventId]);
  // Each answer replaces the form's state in the same render that first sees it.
  const loaded = list.data;
  const [applied, setApplied] = useState(loaded);
  if (loaded && loaded !== applied) {
    setApplied(loaded);
    setRooms(loaded.rooms);
    setDates(loaded.dates);
    setCurrent(loaded.defaultRoom);
    setSuggestions(loaded.suggestions ?? null);
    setPick(loaded.defaultRoom ? String(loaded.defaultRoom.id) : NO_ROOM);
    setNeedsCount(loaded.needsRoom.length);
    setPendingCount(loaded.pending);
  }

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
      setPick(res.defaultRoom ? String(res.defaultRoom.id) : NO_ROOM);
      setNeedsCount(res.defaultRoom ? res.lost.length : 0);
      setPendingCount(res.defaultRoom ? res.pending : 0);
      setStatus(seriesBookingMessage(res, over));
      overlap.reset();
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
    overlap.clearError();
  }

  function apply() {
    if (busy || pick === value) return;
    if (pick === NO_ROOM) {
      void send(null);
      return;
    }
    const room = rooms.find((r) => String(r.id) === pick);
    if (room) void send(room.id);
  }

  function bookAnyway() {
    if (!pending) return;
    const why = overlap.read();
    if (why === null) return;
    void send(pending.roomId, why, pending.dates);
  }

  if (!enabled && !loadError) return null;

  // A series room an admin set that this person can't list (a restricted room) still shows by name.
  const hidden = current && !rooms.some((r) => r.id === current.id) ? current : null;
  const value = current ? String(current.id) : NO_ROOM;
  const chosen = pick === NO_ROOM ? null : (rooms.find((r) => String(r.id) === pick) ?? null);

  return (
    <div className="space-y-2">
      <RoomLoadStatus loading={loading} error={loadError} onRetry={list.retry} />
      {!loading && !loadError && (
        <>
          <Field
            id={id}
            label="Room for every date"
            optional
            hint="Choose a room, then book it for every upcoming date. If bookings need an admin's approval, they wait for one. A date you gave its own room keeps it. “No series room” releases the rooms it booked."
          >
            <Select value={pick} aria-busy={busy || undefined} onChange={(e) => choose(e.target.value)}>
              <option value={NO_ROOM}>No series room</option>
              {hidden && <option value={String(hidden.id)}>{roomName(hidden)}</option>}
              <RoomOptions rooms={rooms} suggestions={suggestions} label={(r, reason) => seriesRoomLabel(r, dates, reason)} />
            </Select>
          </Field>
          <Button variant="secondary" disabled={busy || pick === value} onClick={apply}>
            {pick === NO_ROOM ? "Stop the series room" : "Book for every date"}
          </Button>
          <div role="status" className="space-y-2 empty:hidden">
            {chosen && pick !== value && busyDatesNote(chosen, dates, "edit") && (
              <p className="text-sm text-warning-on-subtle"><AlertCircleIcon className="mr-1 inline-block size-4 align-text-bottom" />{busyDatesNote(chosen, dates, "edit")}</p>
            )}
            {status && <p className="text-sm text-text-muted">{status}</p>}
            {!status && current && pendingCount > 0 && (
              <p className="text-sm text-text-muted">Waiting for an admin to approve this room for {count(pendingCount, "date")}.</p>
            )}
            {current && dates > 0 && needsCount >= dates && (
              <p className="text-sm text-warning-on-subtle"><AlertCircleIcon className="mr-1 inline-block size-4 align-text-bottom" />Not booked on any date yet: every date is busy.</p>
            )}
          </div>
          {error && <FormError>{error}</FormError>}
          {pending && (
            <DoubleBookPrompt reasonId={reasonId} prompt={overlap} busy={busy} onBookAnyway={bookAnyway}
              message={`${pending.dates === 1 ? "1 date is" : `${pending.dates} dates are`} in use. Give a reason to book ${pending.dates === 1 ? "it" : "them"} anyway.`} />
          )}
        </>
      )}
    </div>
  );
}
