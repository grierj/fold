import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Calendar } from "../api";
import type { CalendarOccurrence } from "../types";
import { DateBadge } from "./DateBadge";
import { ErrorNotice } from "./ErrorNotice";
import { calendarStateLabel, occurrenceLink } from "./calendar/EventRow";
import { EmptyState } from "./ui/EmptyState";
import { Skeleton } from "./ui/Skeleton";
import { addDays } from "../lib/calendar";
import { errorText } from "../lib/errors";
import { browserTimeZone, dayOf, formatNextMeeting } from "../lib/when";

const SHOWN = 4;
const WINDOW_DAYS = 60;

// The signed-in Home's lead (DEC-94): the viewer's next few dates from the
// calendar's Just mine scope, each with their own answer. It stays inside
// Just mine, so it never lists another group's unanswered public events
// (DEC-76). It loads and fails on its own, so the groups below still work.
export function ComingUp() {
  const [rows, setRows] = useState<CalendarOccurrence[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [retry, setRetry] = useState(0);

  useEffect(() => {
    let alive = true;
    setRows(null);
    setError(null);
    const now = new Date();
    const zone = browserTimeZone();
    const from = dayOf(now.toISOString(), zone);
    Calendar.list({ from, to: addDays(from, WINDOW_DAYS), tz: zone, mine: true })
      .then(({ occurrences }) => {
        if (!alive) return;
        // The range starts at the beginning of today, so drop what has ended.
        // A date the person can't go to is not "coming up for" them.
        const upcoming = occurrences
          .filter((o) => Date.parse(o.endsAt || o.startsAt) > now.getTime() && o.myStatus !== "declined")
          .sort((a, b) => Date.parse(a.startsAt) - Date.parse(b.startsAt));
        setRows(upcoming.slice(0, SHOWN));
      })
      .catch((e) => {
        if (alive) setError(errorText(e, "We couldn't load your calendar."));
      });
    return () => {
      alive = false;
    };
  }, [retry]);

  return (
    <section aria-labelledby="coming-up-heading">
      <div className="mb-4 flex items-center justify-between gap-3">
        <h2 id="coming-up-heading" className="text-2xl font-bold">Coming up for you</h2>
        <Link to="/calendar?mine=1" className="inline-flex min-h-11 items-center font-semibold text-primary hover:underline">
          See calendar
        </Link>
      </div>
      {error ? (
        <ErrorNotice message={error} onRetry={() => setRetry((n) => n + 1)} />
      ) : rows === null ? (
        <Skeleton kind="lines" count={3} label="Loading your dates…" />
      ) : rows.length === 0 ? (
        <EmptyState title="Nothing on your calendar yet" body="Join a group below and its dates show up here." />
      ) : (
        <ul className="space-y-3">
          {rows.map((o) => (
            <li key={`${o.eventId}-${o.date}`}>
              <ComingUpRow occ={o} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function ComingUpRow({ occ }: { occ: CalendarOccurrence }) {
  const label = calendarStateLabel(occ);
  const where = [occ.churchWide ? "Church-wide" : occ.groupName || (occ.hostedBy ? `Hosted by ${occ.hostedBy}` : ""), occ.location]
    .filter(Boolean)
    .join(" · ");
  return (
    // The same link the calendar's rows use: an exact-date invitation has its
    // own address, anything else opens the event.
    <Link
      to={occurrenceLink(occ)}
      className="flex min-h-11 gap-4 rounded-lg border border-border bg-surface p-4 hover:border-border-strong"
    >
      <DateBadge at={occ.startsAt} size="sm" />
      <span className="min-w-0 flex-1">
        <span className="block font-semibold text-text">{occ.title || occ.groupName}</span>
        <span className="block text-sm text-text-muted">{formatNextMeeting(occ.startsAt).text}</span>
        {where && <span className="block truncate text-sm text-text-muted">{where}</span>}
        {label && (
          <span className="mt-1 inline-flex rounded-sm border border-primary bg-surface px-2 py-0.5 text-xs font-semibold text-text">
            {label}
          </span>
        )}
      </span>
    </Link>
  );
}
