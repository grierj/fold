import { dayOf } from "./when";

// Pure date math for the calendar (M1.6, DEC-67). Days are "YYYY-MM-DD"
// strings in the viewer's zone, stepped as UTC dates so daylight saving
// never moves a day. Weeks start on Sunday.

export type View = "agenda" | "week" | "month";

export function addDays(day: string, n: number): string {
  const [y, m, d] = day.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d + n)).toISOString().slice(0, 10);
}

function weekday(day: string): number {
  const [y, m, d] = day.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d)).getUTCDay();
}

export function addMonths(day: string, n: number): string {
  const [y, m] = day.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1 + n, 1)).toISOString().slice(0, 10);
}

// The 7 days, Sunday to Saturday, of the week holding day.
export function weekRange(day: string): string[] {
  const start = addDays(day, -weekday(day));
  return Array.from({ length: 7 }, (_, i) => addDays(start, i));
}

// The month holding day as 6 weeks of 7 days (always 6, so the grid never
// changes height from month to month).
export function monthGrid(day: string): string[][] {
  const first = `${day.slice(0, 7)}-01`;
  const start = addDays(first, -weekday(first));
  return Array.from({ length: 6 }, (_, w) => Array.from({ length: 7 }, (_, d) => addDays(start, w * 7 + d)));
}

// The days a view fetches. Agenda runs from today for `agendaDays`.
export function rangeFor(view: View, day: string, agendaDays = 30): { from: string; to: string } {
  if (view === "week") {
    const w = weekRange(day);
    return { from: w[0], to: w[6] };
  }
  if (view === "month") {
    const g = monthGrid(day);
    return { from: g[0][0], to: g[5][6] };
  }
  return { from: day, to: addDays(day, agendaDays - 1) };
}

// Occurrences bucketed by the viewer's day of their start, keeping their order.
export function groupByDay<T extends { startsAt: string }>(items: T[], zone?: string): Map<string, T[]> {
  const out = new Map<string, T[]>();
  for (const it of items) {
    const day = dayOf(it.startsAt, zone);
    out.set(day, [...(out.get(day) ?? []), it]);
  }
  return out;
}
