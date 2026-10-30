import { browserTimeZone, dayOf, formatEventDay, formatShortDay, formatTimeRange, weekdayLongName } from "./when";

// The event page's "when" line (DEC-92), built from the series rule and the
// dates the page already loaded. Fold refuses sub-daily rules (DEC-89), so
// the rule only ever needs daily, weekly and monthly wording; anything more
// exotic reads "Repeats" rather than saying something wrong.

const CODES = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"];
// Weekdays read Monday first ("Tuesday and Thursday").
const READING_ORDER = [1, 2, 3, 4, 5, 6, 0];
const ORDINALS: Record<number, string> = { 1: "First", 2: "Second", 3: "Third", 4: "Fourth" };
// Rule parts that don't change the wording (an end, a week start).
const NEUTRAL = new Set(["UNTIL", "COUNT", "WKST"]);

export interface RepeatDate {
  // The date's own calendar day (YYYY-MM-DD), in the event's zone.
  date: string;
  startsAt: string;
}

function parseRule(rule: string): Map<string, string> {
  const parts = new Map<string, string>();
  for (const part of rule.replace(/^RRULE:/i, "").split(";")) {
    const [key, value] = part.split("=");
    if (key && value !== undefined) parts.set(key.toUpperCase(), value);
  }
  return parts;
}

function weekdayOf(day: string): number {
  return new Date(`${day}T12:00:00Z`).getUTCDay();
}

function joinNames(names: string[]): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

function ordinalDay(n: number): string {
  const teen = n % 100 >= 11 && n % 100 <= 13;
  const suffix = teen ? "th" : ({ 1: "st", 2: "nd", 3: "rd" } as Record<number, string>)[n % 10] ?? "th";
  return `${n}${suffix}`;
}

// How far the viewer's zone moves the event's weekdays (0, 1 or 6 days).
// Read off the real dates and take the commonest, so one date edited onto
// another day or a clock change near midnight can't flip the whole line.
function weekdayShift(dates: RepeatDate[], zone: string): number {
  const counts = new Map<number, number>();
  for (const d of dates) {
    const shift = (weekdayOf(dayOf(d.startsAt, zone)) - weekdayOf(d.date) + 7) % 7;
    counts.set(shift, (counts.get(shift) ?? 0) + 1);
  }
  let best = 0;
  let bestCount = 0;
  for (const [shift, count] of counts) {
    if (count > bestCount) { best = shift; bestCount = count; }
  }
  return best;
}

// "Every Wednesday", "First Saturday of each month", … or "Repeats". Null for
// a rule that is empty (a single date).
export function repeatPattern(rule: string, dates: RepeatDate[], zone = browserTimeZone()): string | null {
  if (!rule.trim()) return null;
  const parts = parseRule(rule);
  const first = dates[0];
  const interval = Number(parts.get("INTERVAL") ?? "1");
  const unsupported = [...parts.keys()].some((k) => !NEUTRAL.has(k) && !["FREQ", "INTERVAL", "BYDAY", "BYMONTHDAY", "BYSETPOS"].includes(k));
  if (!first || unsupported || !Number.isInteger(interval) || interval < 1) return "Repeats";
  const freq = parts.get("FREQ");
  const byday = (parts.get("BYDAY") ?? "").split(",").filter(Boolean);

  if (freq === "WEEKLY" && interval <= 2 && !parts.has("BYMONTHDAY") && !parts.has("BYSETPOS")) {
    const wall = byday.length > 0
      ? byday.map((d) => CODES.indexOf(d.slice(-2)))
      : [weekdayOf(first.date)];
    if (wall.some((i) => i < 0)) return "Repeats";
    const shift = weekdayShift(dates, zone);
    const days = new Set(wall.map((i) => (i + shift) % 7));
    if (days.size === 7) return interval === 1 ? "Every day" : "Repeats";
    const names = READING_ORDER.filter((i) => days.has(i)).map(weekdayLongName);
    return `Every ${interval === 2 ? "other " : ""}${joinNames(names)}`;
  }

  if (freq === "DAILY" && interval <= 2 && byday.length === 0 && !parts.has("BYMONTHDAY") && !parts.has("BYSETPOS")) {
    return interval === 2 ? "Every other day" : "Every day";
  }

  if (freq === "MONTHLY" && interval === 1) {
    const zoneDay = dayOf(first.startsAt, zone);
    if (byday.length === 1) {
      // "1SA" / "-1FR", or a bare weekday with BYSETPOS.
      const m = byday[0].match(/^([+-]?\d+)?([A-Z]{2})$/);
      const n = Number(m?.[1] ?? parts.get("BYSETPOS") ?? NaN);
      if (!m || CODES.indexOf(m[2]) < 0 || !Number.isInteger(n) || n < -1 || n === 0 || n > 4) return "Repeats";
      // A zone that moves the evening onto the next weekday makes "first
      // Saturday" a moving "first or second Sunday", so say less, not wrong.
      if (weekdayOf(zoneDay) !== CODES.indexOf(m[2])) return "Repeats";
      return `${n === -1 ? "Last" : ORDINALS[n]} ${weekdayLongName(CODES.indexOf(m[2]))} of each month`;
    }
    if (byday.length > 1 || parts.has("BYSETPOS")) return "Repeats";
    const monthDays = (parts.get("BYMONTHDAY") ?? "").split(",").filter(Boolean);
    if (monthDays.length > 1) return "Repeats";
    if (monthDays[0] === "-1") return "Monthly on the last day";
    if (monthDays[0] && !/^\d+$/.test(monthDays[0])) return "Repeats";
    return `Monthly on the ${ordinalDay(Number(zoneDay.slice(8)))}`;
  }

  return "Repeats";
}

// "Thursday, Nov 5 · 5:30–8:00 PM" for one date, in the viewer's zone.
export function dateLine(startsAt: string, endsAt: string, zone = browserTimeZone(), now = new Date()): string {
  return `${formatEventDay(startsAt, zone, now)} · ${formatTimeRange(startsAt, endsAt, zone)}`;
}

// The line under an event's title: a single date, or the pattern with the
// next date. Null when there is no upcoming date to describe.
export function whenLine(
  rule: string,
  dates: (RepeatDate & { endsAt: string })[],
  zone = browserTimeZone(),
  now = new Date(),
): string | null {
  const next = dates[0];
  if (!next) return null;
  const pattern = repeatPattern(rule, dates, zone);
  if (pattern === null) return dateLine(next.startsAt, next.endsAt, zone, now);
  const nextDay = `next ${formatShortDay(next.startsAt, zone, now)}`;
  if (pattern === "Repeats") return `${pattern} · ${nextDay}`;
  return `${pattern} · ${formatTimeRange(next.startsAt, next.endsAt, zone)} · ${nextDay}`;
}
