import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import type { CalendarOccurrence } from "../../types";
import { addDays, addMonths } from "../../lib/calendar";
import { formatDayHeading, formatDayLong, weekdayNames } from "../../lib/when";
import { Button } from "../ui/Button";
import { DayButton } from "../ui/DayButton";
import { calendarState, calendarStateLabel, DaySection, EventChip } from "./EventRow";

const MAX_CHIPS = 3;

// A month as a 6-week table, Sunday first. From sm up each cell lists up to
// 3 chips and "+N more"; on phones each cell is a 44px day button with dots.
// Either way the chosen day's events show in the panel under the grid.
// Keyboard: one day is in the Tab order (a roving tabindex); arrows move by
// day and week, Home/End to the week's edges, PageUp/PageDown by month, and
// Enter picks the day. Moving off the grid turns the month (onNavigate).
// Desktop event links and "+N more" also stay in the ordinary Tab order.
export function MonthView({
  weeks,
  month,
  byDay,
  today,
  selected,
  onSelect,
  onNavigate,
  caption,
}: {
  weeks: string[][];
  month: string;
  byDay: Map<string, CalendarOccurrence[]>;
  today: string;
  selected: string;
  onSelect: (day: string) => void;
  onNavigate: (day: string) => void;
  caption: string;
}) {
  const names = weekdayNames();
  const [focusDay, setFocusDay] = useState(selected);
  const moved = useRef(false);
  const table = useRef<HTMLTableElement>(null);
  useEffect(() => setFocusDay(selected), [selected]);
  useEffect(() => {
    if (!moved.current) return;
    moved.current = false;
    const cells = [...(table.current?.querySelectorAll<HTMLButtonElement>(`[data-day="${focusDay}"]`) ?? [])];
    (cells.find((c) => c.getClientRects().length > 0) ?? cells[0])?.focus();
  }, [focusDay, weeks]);

  function onKey(e: KeyboardEvent<HTMLButtonElement>, d: string) {
    const week = weeks.find((w) => w.includes(d))!;
    const target = {
      ArrowLeft: addDays(d, -1),
      ArrowRight: addDays(d, 1),
      ArrowUp: addDays(d, -7),
      ArrowDown: addDays(d, 7),
      Home: week[0],
      End: week[6],
      PageUp: addMonths(d, -1),
      PageDown: addMonths(d, 1),
    }[e.key];
    if (!target) return;
    e.preventDefault();
    moved.current = true;
    if (weeks.some((w) => w.includes(target)) && !e.key.startsWith("Page")) setFocusDay(target);
    else onNavigate(target);
  }
  return (
    <>
      <table ref={table} className="w-full table-fixed border-collapse">
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
                const stateCounts = new Map<string, number>();
                for (const occ of occs) {
                  const label = calendarStateLabel(occ);
                  if (label) stateCounts.set(label.toLowerCase(), (stateCounts.get(label.toLowerCase()) ?? 0) + 1);
                }
                const states = [...stateCounts].map(([state, count]) => `${count} ${state}`).join(", ");
                const label = `${formatDayLong(d)}${isToday ? ", today" : ""}, ${occs.length} ${occs.length === 1 ? "event" : "events"}${states ? `, ${states}` : ""}`;
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
                      onKeyDown={(e) => onKey(e, d)}
                      day={d}
                      focusable={d === focusDay}
                      layout="flex min-h-11 w-full flex-col items-center justify-center gap-0.5 sm:hidden"
                    >
                      {number}
                      <span aria-hidden="true" className="flex h-1.5 gap-0.5">
                        {occs.slice(0, MAX_CHIPS).map((o) => {
                          const state = calendarState(o);
                          const dot = state === "going"
                            ? "border-primary bg-primary"
                            : state === "maybe" || state === "waitlisted" || state === "not_answered"
                              ? "border-primary bg-transparent"
                              : state === "declined"
                                ? "border-border-strong bg-transparent"
                                : "border-border-strong bg-border-strong";
                          return <span key={`${o.eventId}-${o.date}`} className={`h-1.5 w-1.5 rounded-full border ${dot}`} />;
                        })}
                      </span>
                    </DayButton>
                    <div className="hidden min-h-28 sm:block">
                      <DayButton
                        label={label}
                        pressed={d === selected}
                        onClick={() => onSelect(d)}
                        onKeyDown={(e) => onKey(e, d)}
                        day={d}
                        focusable={d === focusDay}
                        layout="inline-flex"
                      >
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
      <div className="mt-3 flex flex-wrap gap-x-4 gap-y-2 text-xs text-text-muted sm:hidden" role="group" aria-label="Calendar key">
        <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className="h-1.5 w-1.5 rounded-full border border-primary bg-primary" />Going</span>
        <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className="h-1.5 w-1.5 rounded-full border border-primary bg-transparent" />Not answered, Maybe or Waitlisted</span>
        <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className="h-1.5 w-1.5 rounded-full border border-border-strong bg-transparent" />Can't go</span>
        <span className="inline-flex items-center gap-1.5"><span aria-hidden="true" className="h-1.5 w-1.5 rounded-full border border-border-strong bg-border-strong" />Other events</span>
      </div>
      <div className="mt-6">
        <DaySection heading={formatDayHeading(selected)} occs={byDay.get(selected) ?? []} />
      </div>
    </>
  );
}
