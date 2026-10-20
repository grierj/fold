import type { CalendarOccurrence } from "../../types";
import { formatDayHeading, formatDayLong, weekdayNames } from "../../lib/when";
import { Button } from "../ui/Button";
import { DayButton } from "../ui/DayButton";
import { DaySection, EventChip } from "./EventRow";

const MAX_CHIPS = 3;

// A month as a 6-week table, Sunday first. From sm up each cell lists up to
// 3 chips and "+N more"; on phones each cell is a 44px day button with dots.
// Either way the chosen day's events show in the panel under the grid.
export function MonthView({
  weeks,
  month,
  byDay,
  today,
  selected,
  onSelect,
  caption,
}: {
  weeks: string[][];
  month: string;
  byDay: Map<string, CalendarOccurrence[]>;
  today: string;
  selected: string;
  onSelect: (day: string) => void;
  caption: string;
}) {
  const names = weekdayNames();
  return (
    <>
      <table className="w-full table-fixed border-collapse">
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr>
            {names.map((n) => (
              <th key={n} scope="col" className="pb-1 text-xs font-semibold text-text-muted">
                {n}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {weeks.map((w) => (
            <tr key={w[0]}>
              {w.map((d) => {
                const occs = byDay.get(d) ?? [];
                const inMonth = d.slice(0, 7) === month;
                const isToday = d === today;
                const label = `${formatDayLong(d)}${isToday ? ", today" : ""}, ${occs.length} ${occs.length === 1 ? "event" : "events"}`;
                const number = (
                  <span
                    className={`inline-flex h-7 w-7 items-center justify-center text-sm ${isToday ? "rounded-full bg-primary text-primary-foreground" : ""}`}
                  >
                    {Number(d.slice(8))}
                  </span>
                );
                return (
                  <td key={d} className={`border border-border p-0 align-top sm:p-1 ${inMonth ? "bg-surface text-text" : "bg-page text-text-muted"}`}>
                    <DayButton
                      label={label}
                      pressed={d === selected}
                      onClick={() => onSelect(d)}
                      layout="flex min-h-11 w-full flex-col items-center justify-center gap-0.5 sm:hidden"
                    >
                      {number}
                      <span aria-hidden="true" className="flex h-1.5 gap-0.5">
                        {occs.slice(0, MAX_CHIPS).map((o) => (
                          <span key={`${o.eventId}-${o.date}`} className="h-1.5 w-1.5 rounded-full bg-primary" />
                        ))}
                      </span>
                    </DayButton>
                    <div className="hidden min-h-28 sm:block">
                      <DayButton label={label} pressed={d === selected} onClick={() => onSelect(d)} layout="inline-flex">
                        {number}
                      </DayButton>
                      <ul className="mt-1 space-y-1">
                        {occs.slice(0, MAX_CHIPS).map((o) => (
                          <li key={`${o.eventId}-${o.date}`}>
                            <EventChip occ={o} />
                          </li>
                        ))}
                      </ul>
                      {occs.length > MAX_CHIPS && (
                        <Button variant="link" layout="px-1" onClick={() => onSelect(d)}>
                          +{occs.length - MAX_CHIPS} more
                        </Button>
                      )}
                    </div>
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="mt-6">
        <DaySection heading={formatDayHeading(selected)} occs={byDay.get(selected) ?? []} />
      </div>
    </>
  );
}
