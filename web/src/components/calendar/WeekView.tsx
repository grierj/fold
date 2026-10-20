import type { CalendarOccurrence } from "../../types";
import { formatDayHeading, formatWeekday } from "../../lib/when";
import { DaySection, EventChip } from "./EventRow";

// A week as a list of 7 days, not an hour grid (DEC-67): 7 columns of chips
// from sm up; on phones, the days that have events, stacked like Agenda.
export function WeekView({ days, byDay, today }: { days: string[]; byDay: Map<string, CalendarOccurrence[]>; today: string }) {
  return (
    <>
      <div className="hidden sm:grid grid-cols-7 gap-2">
        {days.map((d) => {
          const occs = byDay.get(d) ?? [];
          return (
            <section key={d} className="min-w-0 rounded-lg border border-border bg-surface p-2">
              <h3 className={`mb-2 text-sm font-semibold ${d === today ? "text-primary" : "text-text"}`}>
                {formatWeekday(d)}
                {d === today && <span className="sr-only">, today</span>}
              </h3>
              {occs.length === 0 ? (
                <p className="text-sm text-text-subtle">
                  <span aria-hidden="true">—</span>
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
