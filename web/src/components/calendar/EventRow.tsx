import { Link } from "react-router-dom";
import type { CalendarOccurrence } from "../../types";
import { formatTime } from "../../lib/when";
import { calendarState, calendarStateLabel, ghosted, occurrenceLink } from "../../lib/calendarState";
import { CheckIcon } from "../icons";
import { StatusBadge } from "./StatusBadge";
import { RemoteBadge } from "../RemoteBadge";
import { ChildcareBadge } from "../ChildcareBadge";

// One occurrence as a full-width link (Agenda, Week on phones, day panel).
// Own-scope state is communicated by border/fill and by an explicit label;
// text is never faded with opacity.
export function EventRow({ occ }: { occ: CalendarOccurrence }) {
  const state = calendarState(occ);
  const stateClass = state === "going"
    ? "border-primary bg-primary-subtle text-text"
    : ghosted(state)
      ? "border-border border-dashed bg-sunken text-text"
      : "border-border bg-surface text-text";
  return (
    <Link
      to={occurrenceLink(occ)}
      className={`grid min-h-11 gap-x-6 gap-y-1 border-l-2 px-4 py-3 hover:border-border-strong sm:grid-cols-[12rem_minmax(0,1fr)] ${stateClass}`}
    >
      <span className="flex flex-col items-start gap-1 sm:row-span-2">
        <span className="tabular-nums text-sm text-text-muted">
          {formatTime(occ.startsAt)} to {formatTime(occ.endsAt)}
        </span>
        <StatusBadge state={state} />
        {(occ.remoteAvailable || occ.childcareAvailable) && <span className="flex flex-wrap gap-2">
          {occ.remoteAvailable && <RemoteBadge />}
          {occ.childcareAvailable && <ChildcareBadge />}
        </span>}
      </span>
      <span className="min-w-0 wrap-anywhere font-semibold text-text">{occ.title || occ.groupName}</span>
      <span className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-sm text-text-muted">
        {occ.churchWide ? (
          <span className="shrink-0 rounded-sm bg-inverse px-2 py-0.5 text-xs font-semibold text-inverse-foreground">
            Church-wide
          </span>
        ) : (
          <>
            {(occ.groupName || occ.hostedBy) && <span className="min-w-0 wrap-anywhere">{occ.groupName || `Hosted by ${occ.hostedBy}`}</span>}
            {occ.groupDeleted && (
              <span className="shrink-0 rounded-sm bg-sunken px-2 py-0.5 text-xs font-semibold text-text-muted">
                Deleted group
              </span>
            )}
          </>
        )}
        {occ.location && <span className="min-w-0 wrap-anywhere">{occ.churchWide || occ.groupName || occ.hostedBy ? " · " : ""}{occ.location}</span>}
      </span>
    </Link>
  );
}

// Compact week/month entry. RSVP state remains visible text and part of the
// accessible name; outside-own-scope entries keep the ordinary treatment.
export function EventChip({ occ, stacked }: { occ: CalendarOccurrence; stacked?: boolean }) {
  const time = formatTime(occ.startsAt);
  const title = occ.title || occ.groupName || "";
  const state = calendarState(occ);
  const label = calendarStateLabel(occ);
  // Keep the visible words in the same order in the accessible name, then
  // append context that a compact chip does not have room to render.
  const name = [`${time} ${title}`, label, occ.remoteAvailable && "Remote", occ.childcareAvailable && "Childcare", occ.churchWide ? "Church-wide" : occ.groupName, occ.groupDeleted && "deleted group"]
    .filter(Boolean)
    .join(", ");
  const stateClass = state === "going"
    ? "border-primary bg-primary-subtle text-text"
    : ghosted(state)
      ? "border-border border-dashed bg-sunken text-text"
      : state === "declined"
        ? "border-border bg-surface text-text-muted"
        : "border-transparent bg-primary-subtle text-primary-on-subtle";
  return (
    <Link
      to={occurrenceLink(occ)}
      title={name}
      className={`block min-h-11 ${stacked ? "" : "text-xs"} rounded-sm border px-2 py-1.5 hover:underline ${stateClass}`}
    >
      {stacked ? (
        <>
          <span className="flex items-center gap-1 text-xs tabular-nums text-text-muted">
            {state === "going" && <CheckIcon className="h-3 w-3 shrink-0" />}
            {time}
          </span>
          <span className="line-clamp-2 text-sm">{title}</span>
          {label && <span className="mt-1 block text-xs font-semibold">{label}</span>}
        </>
      ) : (
        <>
          <span className="flex items-center gap-1">
            {state === "going" && <CheckIcon className="h-3 w-3 shrink-0" />}
            <span className="truncate"><span className="tabular-nums">{time}</span> {title}</span>
          </span>
          {(label || occ.remoteAvailable || occ.childcareAvailable) && (
            <span className="mt-0.5 flex flex-wrap items-center gap-1">
              {label && <span className="font-semibold">{label}</span>}
              {occ.remoteAvailable && <RemoteBadge />}
              {occ.childcareAvailable && <ChildcareBadge />}
            </span>
          )}
        </>
      )}
      {stacked && occ.remoteAvailable && <> <RemoteBadge /></>}
      {stacked && occ.childcareAvailable && <> <ChildcareBadge /></>}
    </Link>
  );
}

export function DaySection({ heading, occs, sticky }: { heading: string; occs: CalendarOccurrence[]; sticky?: boolean }) {
  return (
    <section>
      <h2 className={`mb-2 py-2 font-semibold text-text ${sticky ? "sticky top-[var(--fold-header-height,4rem)] z-0 bg-page sm:static" : ""}`}>{heading}</h2>
      {occs.length === 0 ? (
        <p className="text-sm text-text-muted">Nothing on this day.</p>
      ) : (
        <ul className="fold-list space-y-1">
          {occs.map((o) => <li key={`${o.eventId}-${o.date}`}><EventRow occ={o} /></li>)}
        </ul>
      )}
    </section>
  );
}
