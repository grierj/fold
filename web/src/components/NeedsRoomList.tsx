import { useEffect, useState } from "react";
import { SeriesRooms, Site } from "../api";
import { errorText } from "../lib/errors";
import { formatDayLong } from "../lib/when";
import type { NeedsRoom } from "../types";
import { Button, ButtonLink } from "./ui/Button";
import { FormError } from "./ui/Field";

// "Dates that need a room" on the event page (M2.1c, DEC-103): dates whose
// room was taken when they moved, or was busy when the series was booked.
// Each row opens that date's "Edit this date" to pick another room. It shows
// nothing while rooms are off or when every date has its room.
export function NeedsRoomList({ eventId, revision, hasDate, onPick }: {
  eventId: number;
  // Changes whenever a room or date on the page changes, to ask again.
  revision: number;
  // Whether the page lists this date, so its editor can open there.
  hasDate: (date: string) => boolean;
  onPick: (date: string) => void;
}) {
  const [needs, setNeeds] = useState<NeedsRoom[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [retry, setRetry] = useState(0);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        // A church with rooms off, or a site we can't read, sees no list at all.
        const site = await Site.get();
        if (!alive) return;
        if (!site.roomsEnabled) { setNeeds([]); setError(null); return; }
        const res = await SeriesRooms.list(eventId);
        if (!alive) return;
        setNeeds(res.needsRoom);
        setError(null);
      } catch (e) {
        if (alive) setError(errorText(e, "We couldn't load the dates that need a room."));
      }
    })();
    return () => { alive = false; };
  }, [eventId, revision, retry]);

  if (error) {
    return (
      <div className="mt-6 space-y-2">
        <FormError>{error}</FormError>
        <Button variant="secondary" onClick={() => setRetry((n) => n + 1)}>Try again</Button>
      </div>
    );
  }
  if (needs.length === 0) return null;
  return (
    <section aria-labelledby="needs-room-title" className="mt-6 rounded-md border border-warning-border bg-warning-subtle p-4">
      <h2 id="needs-room-title" className="text-lg font-bold text-warning-on-subtle">
        <span aria-hidden="true">⚠ </span>Dates that need a room
      </h2>
      <p className="mt-1 text-sm text-warning-on-subtle">These dates have no room right now. Pick another one for each.</p>
      <ul className="mt-3 space-y-3">
        {needs.map((n) => {
          const day = formatDayLong(n.date);
          return (
            <li key={n.date} className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0">
                <div className="font-semibold">{day}</div>
                <div className="text-sm text-warning-on-subtle wrap-anywhere">{n.roomName} isn't free then.</div>
              </div>
              {hasDate(n.date) ? (
                <Button variant="secondary" layout="w-full sm:w-auto" onClick={() => onPick(n.date)}>
                  Pick a room{" "}<span className="sr-only">for {day}</span>
                </Button>
              ) : (
                <ButtonLink to={`/events/${eventId}/dates/${n.date}`} variant="secondary" layout="w-full sm:w-auto">
                  Pick a room{" "}<span className="sr-only">for {day}</span>
                </ButtonLink>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
