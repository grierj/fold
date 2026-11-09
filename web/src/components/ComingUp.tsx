import { useEffect } from "react";
import { Link } from "react-router-dom";
import { Calendar } from "../api/calendar";
import type { CalendarOccurrence } from "../types";
import { DateBadge } from "./DateBadge";
import { ErrorNotice } from "./ErrorNotice";
import { StatusBadge } from "./calendar/StatusBadge";
import { calendarState, occurrenceLink } from "../lib/calendarState";
import { EmptyState } from "./ui/EmptyState";
import { Skeleton } from "./ui/Skeleton";
import { addDays } from "../lib/days";
import { errorText } from "../lib/errors";
import { useLoad } from "../lib/useLoad";
import { browserTimeZone, dayOf, formatNextMeeting } from "../lib/when";

const SHOWN = 4;
const WINDOW_DAYS = 60;

// The signed-in Home's lead (DEC-94): the viewer's next few dates from the
// calendar's Just mine scope, each with their own answer. It stays inside
// Just mine, so it never lists another group's unanswered public events
// (DEC-76). It loads and fails on its own, so the groups below still work.
export function ComingUp({ onReady }: { onReady?: () => void } = {}) {
  const load = useLoad(async () => {
    const now = new Date();
    const zone = browserTimeZone();
    const from = dayOf(now.toISOString(), zone);
    const { occurrences } = await Calendar.list({ from, to: addDays(from, WINDOW_DAYS), tz: zone, mine: true });
    // The range starts at the beginning of today, so drop what has ended.
    // A date the person can't go to is not "coming up for" them.
    return occurrences
      .filter((o) => Date.parse(o.endsAt || o.startsAt) > now.getTime() && o.myStatus !== "declined")
      .sort((a, b) => Date.parse(a.startsAt) - Date.parse(b.startsAt))
      .slice(0, SHOWN);
  }, []);
  const rows = load.data ?? null;
  const error = load.error ? errorText(load.error, "We couldn't load your calendar.") : null;

  useEffect(() => {
    if (rows !== null || error) onReady?.();
  }, [rows, error, onReady]);

  return (
    <section aria-labelledby="coming-up-heading">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-x-4 gap-y-1">
        <h2 id="coming-up-heading" className="text-xl font-bold">Coming up for you</h2>
        <Link to="/calendar?mine=1" className="inline-flex min-h-11 items-center font-semibold text-primary hover:underline">
          See calendar
        </Link>
      </div>
      {error ? (
        <ErrorNotice message={error} onRetry={load.retry} />
      ) : rows === null ? (
        <Skeleton kind="lines" count={3} label="Loading your dates…" />
      ) : rows.length === 0 ? (
        <EmptyState title="Nothing coming up in the next two months" body="Dates from your groups, and events you answer, show up here." />
      ) : (
        <ul className="grid gap-x-6 border-t border-border sm:grid-cols-2 lg:grid-cols-4">
          {rows.map((o) => (
            <li key={`${o.eventId}-${o.date}`} className="min-w-0 border-b border-border">
              <ComingUpRow occ={o} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function ComingUpRow({ occ }: { occ: CalendarOccurrence }) {
  const state = calendarState(occ);
  const where = [occ.churchWide ? "Church-wide" : occ.groupName || (occ.hostedBy ? `Hosted by ${occ.hostedBy}` : ""), occ.location]
    .filter(Boolean)
    .join(" · ");
  return (
    // The same link the calendar's rows use: an exact-date invitation has its
    // own address, anything else opens the event.
    <Link
      to={occurrenceLink(occ)}
      className="fold-list-row flex min-h-11 gap-3 rounded-sm hover:bg-sunken"
    >
      <DateBadge at={occ.startsAt} />
      <span className="min-w-0 flex-1">
        <span className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
          <span className="font-semibold text-text wrap-anywhere">{occ.title || occ.groupName}</span>
          {state && <StatusBadge state={state} />}
        </span>
        <span className="mt-1 block text-sm text-text-muted">{formatNextMeeting(occ.startsAt).text}</span>
        {where && <span className="mt-1 block text-sm text-text-muted wrap-anywhere">{where}</span>}
      </span>
    </Link>
  );
}
