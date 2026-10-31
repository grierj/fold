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
  location: string;
  booked: number;
  series: number;
  busy: MatchBusy[];
  busyTotal: number;
}

function ResultNote({ result: r, forwardedRef }: { result: Result; forwardedRef: React.Ref<HTMLDivElement> }) {
  const parts: string[] = [r.booked > 0 ? `Booked ${r.room} for ${count(r.booked, "date", "dates")}.` : `No dates were booked in ${r.room}.`];
  if (r.series > 0) {
    parts.push(r.series === 1 ? "1 series will book its later dates there too." : `${r.series} series will book their later dates there too.`);
  }
  if (r.busyTotal > 0) {
    parts.push(r.busyTotal === 1 ? "1 date was taken, so it was left alone:" : `${r.busyTotal} dates were taken, so they were left alone:`);
  }
  return (
    <div ref={forwardedRef} role="status" tabIndex={-1} className="rounded-lg border border-border bg-sunken px-4 py-3 text-sm focus:outline-none space-y-2">
      <p>{parts.join(" ")}</p>
      {r.busyTotal > 0 && (
        <ul className="list-disc pl-5 space-y-1">
          {r.busy.map((b) => (
            <li key={`${b.eventId}-${b.date}`}>
              <Link to={`/events/${b.eventId}`} className="text-link">{b.eventTitle || "Church event"}</Link> · {formatDayLong(b.date)}
            </li>
          ))}
          {r.busyTotal > r.busy.length && <li className="list-none -ml-5">and {r.busyTotal - r.busy.length} more</li>}
        </ul>
      )}
    </div>
  );
}

function LocationRow({ location: l, index, rooms, value, onChange, busy, error, onBook }: {
  location: TypedLocation;
  index: number;
  rooms: Room[];
  value: string;
  onChange: (roomId: string) => void;
  busy: boolean;
  error: string | null;
  onBook: () => void;
}) {
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
        <Button variant="secondary" layout="w-full sm:w-auto" disabled={value === "" || busy} onClick={onBook}>
          {busy ? "Booking…" : <>Book these dates{" "}<span className="sr-only">for “{l.location}”</span></>}
        </Button>
      </div>
    </li>
  );
}

// "Match old locations" on the admin Rooms tab (M2.1e, PLAN 9.1). Before
// rooms, places were typed; here an admin says which room each typed place
// means and its upcoming dates are booked there. Typed places are never
// changed. Booking happens only on the row's button, never on a change.
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
  const resultRef = useRef<HTMLDivElement>(null);
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
    if (result) resultRef.current?.focus();
  }, [result]);

  async function book(l: TypedLocation) {
    const roomId = Number(picked[l.location]);
    const room = active.find((r) => r.id === roomId);
    if (!room) return;
    setBusyFor(l.location);
    setRowError(null);
    try {
      const res = await AdminRooms.matchLocation(l.location, roomId);
      setResult((prev) => ({ n: (prev?.n ?? 0) + 1, room: roomName(room), location: l.location, ...res }));
      await load();
    } catch (e) {
      setRowError({ location: l.location, text: errorText(e, "We couldn't book those dates.") });
    } finally {
      setBusyFor(null);
    }
  }

  if (!has) return null;

  return (
    <section aria-labelledby="match-locations-heading" className="space-y-3">
      <h2 id="match-locations-heading" className="text-xl font-bold">Match old locations</h2>
      <p className="text-sm text-text-muted max-w-prose">
        Before rooms, places were typed. Pick the room each typed place means, and its upcoming dates are booked there. Typed places stay as they are.
      </p>
      {result && <ResultNote key={result.n} result={result} forwardedRef={resultRef} />}
      {error && <ErrorNotice message={error} onRetry={() => void load()} />}
      {!error && locations === null && <Skeleton kind="lines" />}
      {!error && locations !== null && locations.length === 0 && (
        <EmptyState
          title="No typed places left to match"
          body="Every upcoming date with a typed place has a room, or none has a place."
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
              error={rowError?.location === l.location ? rowError.text : null}
              onBook={() => void book(l)}
            />
          ))}
        </ul>
      )}
    </section>
  );
}
