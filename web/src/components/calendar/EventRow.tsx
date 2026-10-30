import { Link } from "react-router-dom";
import type { CalendarOccurrence } from "../../types";
import { formatTime } from "../../lib/when";
import { CheckIcon, ClockIcon } from "../icons";
import { RemoteBadge } from "../RemoteBadge";
import { ChildcareBadge } from "../ChildcareBadge";

export type CalendarState = "going" | "maybe" | "waitlisted" | "declined" | "not_answered" | null;

const STATUS: Record<Exclude<CalendarState, null>, string> = {
  going: "Going",
  maybe: "Maybe",
  waitlisted: "Waitlisted",
  declined: "Can't go",
  not_answered: "Not answered",
};

// Where a calendar row leads: its exact-date page when the server scopes it to
// one date (invitation or kept date), else the event page.
export function occurrenceLink(occ: CalendarOccurrence): string {
  return occ.dateUrl || `/events/${occ.eventId}`;
}

export function calendarState(occ: CalendarOccurrence): CalendarState {
  if (occ.myStatus) return occ.myStatus;
  return occ.ownScope ? "not_answered" : null;
}

export function calendarStateLabel(occ: CalendarOccurrence): string | null {
  const state = calendarState(occ);
  return state ? STATUS[state] : null;
}

function ghosted(state: CalendarState) {
  return state === "maybe" || state === "waitlisted" || state === "not_answered";
}

export function StatusBadge({ state, solid = false }: { state: CalendarState; solid?: boolean }) {
  if (!state) return null;
  const label = STATUS[state];
  const cls = solid
    ? "border border-primary-foreground text-primary-foreground"
    : state === "declined"
      ? "border border-border-strong bg-surface text-text"
      : "border border-primary bg-surface text-text";
  return (
    <span className={`inline-flex items-center gap-1 rounded-sm px-2 py-0.5 text-xs font-semibold ${cls}`} aria-label={label}>
      {state === "going" && <CheckIcon className="h-3 w-3" />}
      {state === "waitlisted" && <ClockIcon className="h-3 w-3" />}
      {label}
    </span>
  );
}

// One occurrence as a full-width link (Agenda, Week on phones, day panel).
// Own-scope state is communicated by border/fill and by an explicit label;
// text is never faded with opacity.
export function EventRow({ occ }: { occ: CalendarOccurrence }) {
  const state = calendarState(occ);
  const solid = state === "going";
  const stateClass = solid
    ? "border-primary bg-primary text-primary-foreground"
    : ghosted(state)
      ? "border-primary border-dashed bg-sunken text-text"
      : "border-border bg-surface text-text";
  return (
    <Link
      to={occurrenceLink(occ)}
      className={`block min-h-11 rounded-lg border p-4 hover:border-border-strong ${stateClass}`}
    >
      <span className="flex flex-wrap items-center gap-2">
        <span className={`tabular-nums text-sm ${solid ? "text-primary-foreground" : "text-text-muted"}`}>
          {formatTime(occ.startsAt)} – {formatTime(occ.endsAt)}
        </span>
        <StatusBadge state={state} solid={solid} />
        {occ.remoteAvailable && <RemoteBadge />}
        {occ.childcareAvailable && <ChildcareBadge />}
      </span>
      <span className={`mt-1 block font-semibold ${solid ? "text-primary-foreground" : "text-text"}`}>{occ.title || occ.groupName}</span>
      <span className={`mt-1 flex min-w-0 items-center gap-1 text-sm ${solid ? "text-primary-foreground" : "text-text-muted"}`}>
        {occ.churchWide ? (
          <span className="shrink-0 rounded-full bg-inverse px-2 py-0.5 text-xs font-bold uppercase tracking-wide text-inverse-foreground">
            Church-wide
          </span>
        ) : (
          <>
            {(occ.groupName || occ.hostedBy) && <span className="shrink-0">{occ.groupName || `Hosted by ${occ.hostedBy}`}</span>}
            {occ.groupDeleted && (
              <span className="shrink-0 rounded-full bg-sunken px-2 py-0.5 text-xs font-bold uppercase tracking-wide text-text-muted">
                Deleted group
              </span>
            )}
          </>
        )}
        {occ.location && <span className="truncate">{occ.churchWide || occ.groupName || occ.hostedBy ? " · " : ""}{occ.location}</span>}
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
    ? "border-primary bg-primary text-primary-foreground"
    : ghosted(state)
      ? "border-primary border-dashed bg-sunken text-text"
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
          <span className={`flex items-center gap-1 text-xs tabular-nums ${state === "going" ? "text-primary-foreground" : "text-text-muted"}`}>
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
      <h2 className={`py-2 font-semibold text-text ${sticky ? "sticky top-[var(--fold-header-height,4rem)] z-0 bg-page sm:static" : ""}`}>{heading}</h2>
      {occs.length === 0 ? (
        <p className="text-sm text-text-muted">Nothing on this day.</p>
      ) : (
        <ul className="space-y-2">
          {occs.map((o) => <li key={`${o.eventId}-${o.date}`}><EventRow occ={o} /></li>)}
        </ul>
      )}
    </section>
  );
}
