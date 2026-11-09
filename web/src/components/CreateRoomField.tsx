import { useEffect, useRef, useState } from "react";
import { SeriesRooms } from "../api/rooms";
import { useRoomsEnabled } from "../lib/useSite";
import { errorText } from "../lib/errors";
import { roomName } from "../lib/place";
import { busyDatesNote, NO_ROOM, seriesRoomLabel } from "../lib/roomLabels";
import type { WhenState } from "../lib/useWhenState";
import type { RoomSuggestions, SeriesRoom } from "../types";
import { seatsBody, seatsProblem, type SeatsValue } from "./SeatsField";
import { Field, Select } from "./ui/Field";
import { RoomOptions } from "./RoomOptions";
import { RoomLoadStatus } from "./useRoomPicker";
import { weeklyRule } from "../lib/rrule";
import { AlertCircleIcon } from "./icons";

const DEBOUNCE_MS = 400;

// The date fields a create form sends, and the Room field asks about before
// it does: one function builds them, so what is checked is what is booked.
export interface Schedule {
  startsLocal: string;
  durationMinutes: number;
  tzid: string;
  recurrenceRule: string;
  capacity: number | null;
}

export function scheduleFields(when: Pick<WhenState, "date" | "time" | "duration" | "recurring" | "days">, tzid: string, seats: SeatsValue): Schedule {
  return {
    startsLocal: `${when.date}T${when.time}`,
    durationMinutes: when.duration,
    tzid: tzid.trim(),
    recurrenceRule: when.recurring ? weeklyRule(when.days) : "",
    capacity: seatsBody(seats).capacity,
  };
}

// Whether the dates are filled in enough to ask which rooms are free.
export function scheduleReady(when: Pick<WhenState, "date" | "recurring" | "days">, tzid: string, seats: SeatsValue): boolean {
  return when.date !== "" && tzid.trim() !== "" && !(when.recurring && when.days.length === 0) && seatsProblem(seats) === null;
}

// The busy and size notes for the chosen room, in words.
function notes(room: SeriesRoom, dates: number): string[] {
  const busy = busyDatesNote(room, dates, "create");
  return [...(busy ? [busy] : []), ...(room.tooSmall ? ["This room may seat fewer people than the seat limit allows."] : [])];
}

// The Room control of both create forms (M2.1c, DEC-95: beside Location, not
// under More options). The rooms come from the dates being entered, so it asks
// the server again (after a short pause) when the time, repeat or seat limit
// change. The room is booked for every date when the event is scheduled. It
// shows nothing while rooms are off.
export function CreateRoomField({ id, schedule, groupId, visibility, roomId, onChange }: {
  id: string;
  // Null until the dates are filled in.
  schedule: Schedule | null;
  // The group the event is for; omitted for a church-wide event.
  groupId?: number;
  // The visibility chosen on the form: room suggestions count only uses it
  // allows (PLAN 9.3 Rule 1, DEC-110).
  visibility?: string;
  roomId: number | null;
  // The chosen room, or null. Also called with null when a fresh check no
  // longer lists the chosen room.
  onChange: (roomId: number | null) => void;
}) {
  // A church with rooms off, or a site we can't read, sees no rooms control at all.
  const enabled = useRoomsEnabled() === true;
  const [rooms, setRooms] = useState<SeriesRoom[]>([]);
  const [suggestions, setSuggestions] = useState<RoomSuggestions | null>(null);
  const [dates, setDates] = useState(0);
  const [checking, setChecking] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [retry, setRetry] = useState(0);
  // The chosen room, kept by name so it stays in view when the rooms can't be listed.
  const [kept, setKept] = useState<Pick<SeriesRoom, "id" | "name" | "building"> | null>(null);
  // A fresh check dropped the chosen room: say so.
  const [dropped, setDropped] = useState("");
  const seq = useRef(0);
  const latest = useRef({ roomId, onChange, kept });
  latest.current = { roomId, onChange, kept };

  const key = schedule ? JSON.stringify([schedule, groupId ?? null, visibility ?? null]) : "";
  useEffect(() => {
    if (!enabled) return;
    if (!key) {
      setRooms([]);
      setSuggestions(null);
      setDates(0);
      setError(null);
      setChecking(false);
      return;
    }
    const [fields, group, vis] = JSON.parse(key) as [Schedule, number | null, string | null];
    const mine = ++seq.current;
    setChecking(true);
    const timer = setTimeout(async () => {
      try {
        const res = await SeriesRooms.check({ ...fields, ...(group === null ? {} : { groupId: group }), ...(vis === null ? {} : { visibility: vis }) });
        if (seq.current !== mine) return;
        setRooms(res.rooms);
        setSuggestions(res.suggestions ?? null);
        setDates(res.dates);
        setError(null);
        const { roomId: chosen, onChange: report } = latest.current;
        if (chosen !== null) {
          const room = res.rooms.find((r) => r.id === chosen);
          if (room) {
            setKept(room);
            setDropped("");
          } else {
            const gone = latest.current.kept;
            setDropped(`${gone ? roomName(gone) : "That room"} isn't available for these dates any more, so no room is chosen.`);
            report(null);
          }
        }
      } catch (e) {
        if (seq.current === mine) {
          // Labels and notes about the previous dates would mislead, so none stay.
          setRooms([]);
          setSuggestions(null);
          setDates(0);
          setError(errorText(e, "We couldn't check which rooms are free."));
        }
      } finally {
        if (seq.current === mine) setChecking(false);
      }
    }, DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
      seq.current++;
    };
  }, [enabled, key, retry]);

  if (!enabled) return null;

  const chosen = rooms.find((r) => r.id === roomId) ?? null;
  // Rooms that can't be listed right now (dates unfinished, or a failed check) don't reset the choice.
  const shown = !chosen && roomId !== null && kept?.id === roomId ? kept : null;
  const value = chosen ? String(chosen.id) : shown ? String(shown.id) : NO_ROOM;
  const recurring = !!schedule && schedule.recurrenceRule !== "";
  const hint = !schedule
    ? "Finish the date, time and seat limit first to see which rooms are free."
    : recurring
      ? "Booked for every date when you schedule the event."
      : "Booked when you schedule the event.";
  const choose = (v: string) => {
    const room = rooms.find((r) => String(r.id) === v);
    setDropped("");
    if (room) setKept(room);
    onChange(room ? room.id : null);
  };

  return (
    <div className="space-y-2">
      <Field id={id} label="Room" optional hint={hint}>
        <Select value={value} disabled={!schedule} aria-busy={checking || undefined} onChange={(e) => choose(e.target.value)}>
          <option value={NO_ROOM}>No room</option>
          {shown && <option value={String(shown.id)}>{roomName(shown)}</option>}
          <RoomOptions rooms={rooms} suggestions={suggestions} label={(r, reason) => seriesRoomLabel(r, dates, reason)} />
        </Select>
      </Field>
      <div role="status" className="space-y-2 empty:hidden">
        {checking && <p className="text-sm text-text-muted">Checking which rooms are free…</p>}
        {dropped && <p className="text-sm text-warning-on-subtle"><AlertCircleIcon className="mr-1 inline-block size-4 align-text-bottom" />{dropped}</p>}
        {!checking && chosen && notes(chosen, dates).map((n) => (
          <p key={n} className="text-sm text-warning-on-subtle"><AlertCircleIcon className="mr-1 inline-block size-4 align-text-bottom" />{n}</p>
        ))}
      </div>
      <RoomLoadStatus loading={false} error={error} onRetry={() => setRetry((n) => n + 1)} />
    </div>
  );
}
