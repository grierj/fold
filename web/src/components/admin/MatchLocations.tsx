import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { AdminRooms } from "../../api";
import { errorText } from "../../lib/errors";
import { roomName } from "../../lib/place";
import { formatDayLong } from "../../lib/when";
import type { MatchBusy, Room, TypedLocation } from "../../types";
import { ErrorNotice } from "../ErrorNotice";
import { Button } from "../ui/Button";
import { EmptyState } from "../ui/EmptyState";
import { Field, FormError, Select } from "../ui/Field";
import { Skeleton } from "../ui/Skeleton";

const count = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

// "12 upcoming dates across 3 events" or "1 upcoming date in 1 event".
function countsLine(l: TypedLocation): string {
  const dates = count(l.dates, "upcoming date", "upcoming dates");
  return l.events === 1 ? `${dates} in 1 event` : `${dates} across ${l.events} events`;
}

// The room a typed place plainly names: one active room whose name is the
// place, ignoring case and outer spaces. Two rooms with that name is no match.
function guess(location: string, rooms: Room[]): string {
  const want = location.trim().toLowerCase();
  const same = rooms.filter((r) => r.name.trim().toLowerCase() === want);
  return same.length === 1 ? String(same[0].id) : "";
}

interface Result {
  n: number;
  room: string;
  booked: number;
  series: number;
  busy: MatchBusy[];
  busyTotal: number;
}

function summary(r: Result): string {
  const parts: string[] = [];
  if (r.booked > 0) parts.push(`Booked ${r.room} for ${count(r.booked, "date", "dates")}.`);
  else parts.push(`No dates were booked in ${r.room}.${r.busyTotal === 0 ? " They already have rooms." : ""}`);
  if (r.series > 0) {
    parts.push(r.series === 1 ? `1 repeating event now uses ${r.room} for its later dates too.` : `${r.series} repeating events now use ${r.room} for their later dates too.`);
  }
  if (r.busyTotal > 0) {
    parts.push(r.busyTotal === 1 ? "1 date was taken, so it was left alone:" : `${r.busyTotal} dates were taken, so they were left alone:`);
  }
  return parts.join(" ");
}

// What a booking did. Only the summary is announced; the dates left alone
// follow as a plain list the reader can walk.
function ResultNote({ result: r, summaryRef }: { result: Result; summaryRef: React.Ref<HTMLParagraphElement> }) {
  return (
    <div className="rounded-lg border border-border bg-sunken px-4 py-3 text-sm space-y-2">
      <p ref={summaryRef} role="status" tabIndex={-1} className="focus:outline-none">{summary(r)}</p>
      {r.busyTotal > 0 && (
        <>
          <ul className="list-disc pl-5 space-y-1">
            {r.busy.map((b) => (
              <li key={`${b.eventId}-${b.date}`}>
                <Link to={`/events/${b.eventId}`} className="text-link">{b.eventTitle || "Church event"}</Link> · {formatDayLong(b.date)}
              </li>
            ))}
          </ul>
          {r.busyTotal > r.busy.length && <p>and {r.busyTotal - r.busy.length} more</p>}
          <p className="text-text-muted">Open an event to pick another room for its date.</p>
        </>
      )}
    </div>
  );
}

function LocationRow({ location: l, index, rooms, value, onChange, busy, locked, error, onBook }: {
  location: TypedLocation;
  index: number;
  rooms: Room[];
  value: string;
  onChange: (roomId: string) => void;
  // This row's booking is in flight.
  busy: boolean;
  // Some row's booking is in flight, so none can start.
  locked: boolean;
  error: string | null;
  onBook: () => void;
}) {
  const room = rooms.find((r) => String(r.id) === value);
  return (
    <li className="bg-surface rounded-lg border border-border p-5 space-y-3" data-location={l.location}>
      <div>
        <p className="font-bold wrap-anywhere">{l.location}</p>
        <p className="text-sm text-text-muted">{countsLine(l)}</p>
      </div>
      {error && <FormError>{error}</FormError>}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="min-w-0 sm:flex-1">
          <Field id={`match-room-${index}`} label={`Room for “${l.location}”`}>
            <Select value={value} onChange={(e) => onChange(e.target.value)}>
              <option value="">Choose a room</option>
              {rooms.map((r) => <option key={r.id} value={String(r.id)}>{roomName(r)}</option>)}
            </Select>
          </Field>
        </div>
        <Button variant="secondary" layout="w-full sm:w-auto" disabled={!room || locked} onClick={onBook}>
          {busy ? "Booking…" : room ? (
            <>Book {count(l.dates, "date", "dates")} in {roomName(room)}{" "}<span className="sr-only">for “{l.location}”</span></>
          ) : (
            <>Pick a room{" "}<span className="sr-only">for “{l.location}”</span></>
          )}
        </Button>
      </div>
    </li>
  );
}

// "Match old locations" on the admin Rooms tab (M2.1e, PLAN 9.1). Before
// rooms, places were typed; here an admin says which room each typed place
// means and its upcoming dates are booked there. Typed places are never
// changed. Booking happens only on the row's button, never on a change. With
// nothing typed to match, the section is not shown.
export function MatchLocations({ rooms }: { rooms: Room[] | null }) {
  const active = (rooms ?? []).filter((r) => r.active);
  const has = active.length > 0;
  const [locations, setLocations] = useState<TypedLocation[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // The room picked for each typed place, by its text.
  const [picked, setPicked] = useState<Record<string, string>>({});
  const [busyFor, setBusyFor] = useState<string | null>(null);
  const [rowError, setRowError] = useState<{ location: string; text: string } | null>(null);
  const [result, setResult] = useState<Result | null>(null);
  const summaryRef = useRef<HTMLParagraphElement>(null);
  const rootRef = useRef<HTMLElement>(null);
  const roomsRef = useRef(active);
  roomsRef.current = active;

  const load = useCallback(async () => {
    setError(null);
    try {
      const { locations } = await AdminRooms.locations();
      setLocations(locations);
      // A place keeps what was picked for it; a new one is guessed from its name.
      setPicked((prev) => Object.fromEntries(locations.map((l) => [l.location, prev[l.location] ?? guess(l.location, roomsRef.current)])));
    } catch (e) {
      setError(errorText(e, "We couldn't load the typed places."));
    }
  }, []);
  useEffect(() => {
    if (has) void load();
  }, [has, load]);

  // The row may be gone after booking, so the cursor moves to what happened.
  useEffect(() => {
    if (result) summaryRef.current?.focus();
  }, [result]);

  // A failed booking leaves the cursor on that row's button, ready to try again.
  useEffect(() => {
    if (!rowError) return;
    const li = Array.from(rootRef.current?.querySelectorAll<HTMLElement>("li[data-location]") ?? []).find((x) => x.dataset.location === rowError.location);
    li?.querySelector<HTMLElement>("button")?.focus();
  }, [rowError]);

  async function book(l: TypedLocation) {
    const roomId = Number(picked[l.location]);
    const room = active.find((r) => r.id === roomId);
    if (!room) return;
    setBusyFor(l.location);
    setRowError(null);
    try {
      const res = await AdminRooms.matchLocation(l.location, roomId);
      setResult((prev) => ({ n: (prev?.n ?? 0) + 1, room: roomName(room), ...res }));
      await load();
    } catch (e) {
      setRowError({ location: l.location, text: errorText(e, "We couldn't book those dates.") });
    } finally {
      setBusyFor(null);
    }
  }

  if (!has) return null;
  // Nothing typed to match and nothing just booked: nothing to say.
  if (locations !== null && locations.length === 0 && !result && !error) return null;

  return (
    <section ref={rootRef} aria-labelledby="match-locations-heading" className="space-y-3">
      <h2 id="match-locations-heading" className="text-xl font-bold">Match old locations</h2>
      <p className="text-sm text-text-muted max-w-prose">
        Before rooms, people typed a place for each event. Pick the room each one means, and its upcoming dates are booked there. Typed places stay as they are.
      </p>
      {result && <ResultNote key={result.n} result={result} summaryRef={summaryRef} />}
      {error && <ErrorNotice message={error} onRetry={() => void load()} />}
      {!error && locations === null && <Skeleton kind="lines" />}
      {!error && locations !== null && locations.length === 0 && (
        <EmptyState
          title="No typed places left to match"
          body="Places typed into upcoming events will show up here until they have a room."
        />
      )}
      {!error && locations !== null && locations.length > 0 && (
        <ul className="space-y-3">
          {locations.map((l, i) => (
            <LocationRow
              key={l.location}
              location={l}
              index={i}
              rooms={active}
              value={picked[l.location] ?? ""}
              onChange={(v) => setPicked((p) => ({ ...p, [l.location]: v }))}
              busy={busyFor === l.location}
              locked={busyFor !== null}
              error={rowError?.location === l.location ? rowError.text : null}
              onBook={() => void book(l)}
            />
          ))}
        </ul>
      )}
    </section>
  );
}
