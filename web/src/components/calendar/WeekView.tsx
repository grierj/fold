import type { CalendarOccurrence } from "../../types";
import { formatDayHeading, formatWeekday } from "../../lib/when";
import { DaySection, EventChip } from "./EventRow";

// A week as a list of 7 days, not an hour grid (DEC-67): 7 columns of chips
// from sm up; on phones, the days that have events, stacked like Agenda.
// From sm up an empty day shrinks to a narrow column (DEC-99) so the busy
// days keep the room: flex-1 with a zero basis shares what the empty days
// leave, and min-w-0 keeps long titles from forcing sideways scroll.
export function WeekView({ days, byDay, today }: { days: string[]; byDay: Map<string, CalendarOccurrence[]>; today: string }) {
  return (
    <>
      <div className="hidden sm:flex gap-0 border-y border-border">
        {days.map((d) => {
          const occs = byDay.get(d) ?? [];
          const empty = occs.length === 0;
          return (
            <section
              key={d}
              data-empty={empty || undefined}
              className={`min-w-0 border-r border-border bg-surface p-2 last:border-r-0 ${empty ? "w-20 flex-none text-center" : "flex-1 basis-0"}`}
            >
              <h3 className={`text-sm font-semibold ${empty ? "" : "mb-2"} ${d === today ? "text-primary" : "text-text"}`}>
                {formatWeekday(d)}
                {d === today && <span className="block text-xs font-semibold">Today</span>}
              </h3>
              {empty ? (
                <p className="text-sm text-text-muted">
                  <span aria-hidden="true">–</span>
                  <span className="sr-only">No events</span>
                </p>
              ) : (
                <ul className="space-y-1">
                  {occs.map((o) => (
                    <li key={`${o.eventId}-${o.date}`}>
                      <EventChip occ={o} stacked />
                    </li>
                  ))}
                </ul>
              )}
            </section>
          );
        })}
      </div>
      <div className="space-y-4 sm:hidden">
        {days
          .filter((d) => byDay.has(d))
          .map((d) => (
            <DaySection key={d} heading={formatDayHeading(d)} occs={byDay.get(d)!} sticky />
          ))}
      </div>
    </>
  );
}

// "1 event this week", "4 events this week" or "No events this week": one
// line above the week on every width (the phone view said the last already).
export function weekCountText(n: number): string {
  return n === 0 ? "No events this week" : `${n} ${n === 1 ? "event" : "events"} this week`;
}
