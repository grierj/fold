import { useEffect, useRef, useState } from "react";
import { SeriesRooms, Site } from "../api";
import { errorText } from "../lib/errors";
import { formatDayLong } from "../lib/when";
import type { NeedsRoom } from "../types";
import { RoomField } from "./RoomField";
import { Button } from "./ui/Button";
import { FormError } from "./ui/Field";
import { AlertCircleIcon } from "./icons";

// A long list shows its first rows; the rest wait behind one button.
const SHOWN = 10;

// "Dates that need a room" on the event page (M2.1c, DEC-103): dates whose
// room was taken when they moved, or was busy when the series was booked.
// "Pick a room" opens that date's "Edit this date" when the page lists the
// date, and otherwise the one-date Room control right under the row (the page
// lists only the next few months; this list reaches further). It shows
// nothing while rooms are off or when every date has its room.
export function NeedsRoomList({ eventId, revision, hasDate, onPick }: {
  eventId: number;
  // Changes whenever a room or date on the page changes, to ask again.
  revision: number;
  // Whether the page lists this date, so its editor can open there.
  hasDate: (date: string) => boolean;
  onPick: (date: string) => void;
}) {
  // Null until the site settings are read; they don't change while this is open.
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [needs, setNeeds] = useState<NeedsRoom[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [retry, setRetry] = useState(0);
  const [own, setOwn] = useState(0);
  const [all, setAll] = useState(false);
  // The off-page date whose Room control is open under its row.
  const [open, setOpen] = useState<string | null>(null);
  // What the inline control just did, so the row that leaves the list is not silent.
  const [notice, setNotice] = useState("");
  const watching = useRef<string | null>(null);
  const noticeRef = useRef<HTMLParagraphElement>(null);

  useEffect(() => {
    let alive = true;
    // A church with rooms off, or a site we can't read, sees no list at all.
    Site.get().then((site) => { if (alive) setEnabled(site.roomsEnabled === true); }, () => { if (alive) setEnabled(false); });
    return () => { alive = false; };
  }, []);

  useEffect(() => {
    if (!enabled) return;
    let alive = true;
    SeriesRooms.list(eventId).then((res) => {
      if (!alive) return;
      setNeeds(res.needsRoom);
      setError(null);
      const was = watching.current;
      if (was && !res.needsRoom.some((n) => n.date === was)) {
        watching.current = null;
        setOpen(null);
        setNotice(`${formatDayLong(was)} has a room now.`);
      }
    }, (e) => {
      if (alive) setError(errorText(e, "We couldn't load the dates that need a room."));
    });
    return () => { alive = false; };
  }, [enabled, eventId, revision, retry, own]);

  useEffect(() => {
    if (notice) noticeRef.current?.focus();
  }, [notice]);

  const notes = notice && <p ref={noticeRef} tabIndex={-1} role="status" className="mt-6 text-sm text-text-muted outline-none">{notice}</p>;

  if (error) {
    return (
      <div className="mt-6 space-y-2">
        <FormError>{error}</FormError>
        <Button variant="secondary" onClick={() => setRetry((n) => n + 1)}>Try again</Button>
      </div>
    );
  }
  if (needs.length === 0) return <>{notes}</>;
  const rows = all ? needs : needs.slice(0, SHOWN);
  return (
    <>
      {notes}
      <section aria-labelledby="needs-room-title" className="mt-6 rounded-md border border-warning-border bg-warning-subtle p-4">
        <h2 id="needs-room-title" className="text-lg font-bold text-warning-on-subtle">
          <AlertCircleIcon className="mr-1 inline-block size-4 align-text-bottom" />Dates that need a room ({needs.length})
        </h2>
        <p className="mt-1 text-sm text-warning-on-subtle">These dates have no room right now. Pick another one for each.</p>
        <ul className="mt-3 space-y-3">
          {rows.map((n) => {
            const day = formatDayLong(n.date);
            const inline = !hasDate(n.date);
            const panel = `needs-room-${n.date}`;
            return (
              <li key={n.date} className="space-y-2">
                <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                  <div className="min-w-0">
                    <div className="font-semibold">{day}</div>
                    <div className="text-sm text-warning-on-subtle wrap-anywhere">{n.status === "rejected" ? `An admin didn't approve ${n.roomName} for this date.` : `${n.roomName} isn't free then.`}</div>
                  </div>
                  <Button
                    variant="secondary"
                    layout="w-full sm:w-auto"
                    aria-expanded={inline ? open === n.date : undefined}
                    aria-controls={inline ? panel : undefined}
                    onClick={() => {
                      if (!inline) { onPick(n.date); return; }
                      setNotice("");
                      watching.current = open === n.date ? null : n.date;
                      setOpen(open === n.date ? null : n.date);
                    }}
                  >
                    Pick a room{" "}<span className="sr-only">for {day}</span>
                  </Button>
                </div>
                {inline && open === n.date && (
                  <div id={panel} className="rounded-md border border-border bg-surface p-3">
                    <RoomField eventId={eventId} date={n.date} idPrefix={`needs-${n.date}`} focusOnLoad onChanged={() => setOwn((c) => c + 1)} />
                  </div>
                )}
              </li>
            );
          })}
        </ul>
        {!all && needs.length > SHOWN && (
          <div className="mt-3">
            <Button variant="link" onClick={() => setAll(true)}>Show all {needs.length}</Button>
          </div>
        )}
      </section>
    </>
  );
}
