import { Link } from "react-router-dom";
import type { CalendarOccurrence } from "../../types";
import { formatTime } from "../../lib/when";
import { CheckIcon, ClockIcon } from "../icons";
import { RemoteBadge } from "../RemoteBadge";

const STATUS = { going: "Going", maybe: "Maybe", waitlisted: "Waitlisted" } as const;

// My answer, never by color alone: always the word, and Going and
// Waitlisted have an icon too.
function StatusBadge({ status }: { status: CalendarOccurrence["myStatus"] }) {
  if (status === "going") {
    return (
      <span className="inline-flex items-center gap-1 rounded-sm bg-primary-subtle px-2 text-xs font-semibold text-primary-on-subtle">
        <CheckIcon className="w-3 h-3" />
        Going
      </span>
    );
  }
  if (status === "maybe") {
    return <span className="rounded-sm bg-sunken px-2 text-xs font-semibold text-text-muted">Maybe</span>;
  }
  if (status === "waitlisted") {
    return (
      <span className="inline-flex items-center gap-1 rounded-sm bg-sunken px-2 text-xs font-semibold text-text-muted">
        <ClockIcon className="w-3 h-3" />
        Waitlisted
      </span>
    );
  }
  return null;
}

// One occurrence as a full-width link (Agenda, Week on phones, day panel).
// No attendance and no RSVP here: the event page has those.
export function EventRow({ occ }: { occ: CalendarOccurrence }) {
  return (
    <Link
      to={`/events/${occ.eventId}`}
      className="block min-h-11 rounded-lg border border-border bg-surface p-4 hover:border-border-strong"
    >
      <span className="flex flex-wrap items-center gap-2">
        <span className="tabular-nums text-sm text-text-muted">
          {formatTime(occ.startsAt)} – {formatTime(occ.endsAt)}
        </span>
        <StatusBadge status={occ.myStatus} />
        {occ.remoteAvailable && <RemoteBadge />}
        {occ.childcareAvailable && <span className="rounded-sm bg-sunken px-2 text-xs font-semibold text-text-muted">Childcare</span>}
      </span>
      <span className="mt-1 block font-semibold text-text">{occ.title || occ.groupName}</span>
      <span className="mt-1 flex min-w-0 items-center gap-1 text-sm text-text-muted">
        {occ.churchWide ? (
          <span className="shrink-0 rounded-full bg-inverse px-2 py-0.5 text-xs font-bold uppercase tracking-wide text-inverse-foreground">
            Church-wide
          </span>
        ) : (
          <>
            <span className="shrink-0">{occ.groupName ?? `Hosted by ${occ.hostedBy}`}</span>
            {occ.groupDeleted && (
              <span className="shrink-0 rounded-full bg-sunken px-2 py-0.5 text-xs font-bold uppercase tracking-wide text-text-muted">
                Deleted group
              </span>
            )}
          </>
        )}
        {occ.location && <span className="truncate"> · {occ.location}</span>}
      </span>
    </Link>
  );
}

// A compact link for the week and month grids. Its accessible name says
// what the chip abbreviates. In the month grid the chips are a pointer
// shortcut (tabIndex -1): keyboard users move by day and use the panel.
// `stacked` (the week view) puts the time on its own line and lets the
// title run to two lines.
export function EventChip({ occ, pointerOnly, stacked }: { occ: CalendarOccurrence; pointerOnly?: boolean; stacked?: boolean }) {
  const time = formatTime(occ.startsAt);
  const title = occ.title || occ.groupName || "";
  const name = [`${time} ${title}`, occ.churchWide ? "Church-wide" : occ.groupName, occ.groupDeleted && "deleted group", occ.remoteAvailable && "Remote", occ.childcareAvailable && "Childcare", occ.myStatus && STATUS[occ.myStatus]]
    .filter(Boolean)
    .join(", ");
  return (
    <Link
      to={`/events/${occ.eventId}`}
      title={name}
      aria-label={name}
      tabIndex={pointerOnly ? -1 : undefined}
      className={
        stacked
          ? "block rounded-sm bg-primary-subtle px-2 py-1.5 text-primary-on-subtle hover:underline"
          : "flex min-h-6 items-center gap-1 truncate rounded-sm bg-primary-subtle px-2 py-1 text-xs text-primary-on-subtle hover:underline"
      }
    >
      {stacked ? (
        <>
          <span className="flex items-center gap-1 text-xs text-text-muted tabular-nums">
            {occ.myStatus === "going" && <CheckIcon className="w-3 h-3 shrink-0" />}
            {time}
          </span>
          <span className="line-clamp-2 text-sm">{title}</span>
        </>
      ) : (
        <>
          {occ.myStatus === "going" && <CheckIcon className="w-3 h-3 shrink-0" />}
          <span className="truncate">
            <span className="tabular-nums">{time}</span> {title}
          </span>
        </>
      )}
      {occ.remoteAvailable && <RemoteBadge />}
        {occ.childcareAvailable && <span className="rounded-sm bg-sunken px-2 text-xs font-semibold text-text-muted">Childcare</span>}
    </Link>
  );
}

// A day's heading and its rows; empty says so.
export function DaySection({ heading, occs, sticky }: { heading: string; occs: CalendarOccurrence[]; sticky?: boolean }) {
  return (
    <section>
      <h3 className={`py-2 font-semibold text-text ${sticky ? "sticky top-16 z-0 bg-page sm:static" : ""}`}>{heading}</h3>
      {occs.length === 0 ? (
        <p className="text-sm text-text-muted">Nothing on this day.</p>
      ) : (
        <ul className="space-y-2">
          {occs.map((o) => (
            <li key={`${o.eventId}-${o.date}`}>
              <EventRow occ={o} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
