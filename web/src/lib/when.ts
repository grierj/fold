import { shortZoneName, zoneGenericName } from "./eventForm";

// Every event time a person reads is shown in their browser's zone (DEC-57),
// so nobody converts in their head. The source is always the UTC instant
// (startsAt), never the event's wall clock. Edit forms are the exception:
// they edit the event's own clock and name its zone (zoneNote).

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function fmt(iso: string, zone: string, opts: Intl.DateTimeFormatOptions): string {
  return new Intl.DateTimeFormat(undefined, { timeZone: zone, ...opts }).format(new Date(iso));
}

function ymd(instant: Date, zone: string): string {
  return new Intl.DateTimeFormat("en-CA", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit" }).format(instant);
}

function nextDay(day: string): string {
  const [y, m, d] = day.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d + 1)).toISOString().slice(0, 10);
}

// "7:00 PM"
export function formatTime(iso: string, zone = browserTimeZone()): string {
  return fmt(iso, zone, { hour: "numeric", minute: "2-digit" });
}

// "Today at 7:00 PM", "Tomorrow at 7:00 PM", or "Sat, Oct 17 at 7:00 PM".
export function formatWhen(iso: string, zone = browserTimeZone(), now = new Date()): string {
  const day = ymd(new Date(iso), zone);
  const today = ymd(now, zone);
  const time = formatTime(iso, zone);
  if (day === today) return `Today at ${time}`;
  if (day === nextDay(today)) return `Tomorrow at ${time}`;
  return `${fmt(iso, zone, { weekday: "short", month: "short", day: "numeric" })} at ${time}`;
}

// The month and day for a date badge, in the viewer's zone.
export function badgeParts(iso: string, zone = browserTimeZone()): { month: string; day: string } {
  return { month: fmt(iso, zone, { month: "short" }), day: fmt(iso, zone, { day: "numeric" }) };
}

// "Pacific time", from the zone's generic name; the IANA id when the browser
// can't name it.
function zoneName(tzid: string): string {
  const generic = zoneGenericName(tzid);
  if (!generic || /^GMT/.test(generic)) return tzid;
  return `${shortZoneName(generic)} time`;
}

// The event page's quiet line when the event's clock differs from the
// viewer's: "Held in Pacific time (6:00 PM PDT)". Null when both clocks read
// the same, so Boise and Denver viewers see nothing extra.
export function heldIn(iso: string, tzid: string, zone = browserTimeZone()): string | null {
  if (!tzid) return null;
  try {
    return heldInZone(iso, tzid, zone);
  } catch {
    // A zone this browser's ICU doesn't know: the converted time still
    // shows; only the extra line is skipped.
    return null;
  }
}

function heldInZone(iso: string, tzid: string, zone: string): string | null {
  const opts: Intl.DateTimeFormatOptions = { year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric" };
  if (fmt(iso, tzid, opts) === fmt(iso, zone, opts)) return null;
  const sameDay = ymd(new Date(iso), tzid) === ymd(new Date(iso), zone);
  const clock = fmt(iso, tzid, {
    ...(sameDay ? {} : { weekday: "short" as const }),
    hour: "numeric",
    minute: "2-digit",
    timeZoneName: "short",
  });
  return `Held in ${zoneName(tzid)} (${clock})`;
}

// What an edit form says about its times: "Times are in Pacific time."
export function zoneNote(tzid: string): string {
  return `Times are in ${zoneName(tzid)}.`;
}

// A zone's UTC offset right now, as "UTC-7", "UTC+5:30" or "UTC". Used to
// show and search time zones by offset.
export function zoneOffset(tz: string, at: Date = new Date()): string {
  try {
    const part = new Intl.DateTimeFormat("en-US", { timeZone: tz, timeZoneName: "shortOffset" })
      .formatToParts(at)
      .find((p) => p.type === "timeZoneName");
    const v = part?.value ?? "";
    return v.replace(/^GMT/, "UTC");
  } catch {
    return "";
  }
}

// The viewer's calendar day of an instant, "2026-10-21" (the calendar's
// bucket key).
export function dayOf(iso: string, zone = browserTimeZone()): string {
  return ymd(new Date(iso), zone);
}

// A calendar day ("2026-10-21") read as a UTC date, so formatting it never
// shifts it across midnight.
function asDate(day: string): string {
  return `${day}T12:00:00Z`;
}

// "Today", "Tomorrow", or "Wednesday, Oct 21".
export function formatDayHeading(day: string, zone = browserTimeZone(), now = new Date()): string {
  const today = ymd(now, zone);
  if (day === today) return "Today";
  if (day === nextDay(today)) return "Tomorrow";
  return fmt(asDate(day), "UTC", { weekday: "long", month: "short", day: "numeric" });
}

// "Wednesday, October 21", for a day cell's accessible name.
export function formatDayLong(day: string): string {
  return fmt(asDate(day), "UTC", { weekday: "long", month: "long", day: "numeric" });
}

// "Mon 19", a week column heading.
export function formatWeekday(day: string): string {
  return `${fmt(asDate(day), "UTC", { weekday: "short" })} ${Number(day.slice(8))}`;
}

// "Sun" … "Sat", for the month grid's column headings (Sunday first).
export function weekdayNames(): string[] {
  return ["2026-10-18", "2026-10-19", "2026-10-20", "2026-10-21", "2026-10-22", "2026-10-23", "2026-10-24"].map((d) =>
    fmt(asDate(d), "UTC", { weekday: "short" })
  );
}

// The period title: "October 2026" for a month, "Oct 18 – 24, 2026" for a
// week (both ends named when the week crosses a month or year).
export function formatPeriod(view: "week" | "month", first: string, last: string): string {
  if (view === "month") return fmt(asDate(first), "UTC", { month: "long", year: "numeric" });
  const [fy, fm] = [first.slice(0, 4), first.slice(5, 7)];
  const [ly, lm] = [last.slice(0, 4), last.slice(5, 7)];
  const md = (d: string) => fmt(asDate(d), "UTC", { month: "short", day: "numeric" });
  if (fy !== ly) return `${md(first)}, ${fy} – ${md(last)}, ${ly}`;
  if (fm !== lm) return `${md(first)} – ${md(last)}, ${ly}`;
  return `${md(first)} – ${Number(last.slice(8))}, ${ly}`;
}

// "May 4, 2016", for a birth date stored as YYYY-MM-DD.
export function formatBirthDate(day: string): string {
  return fmt(asDate(day), "UTC", { month: "long", day: "numeric", year: "numeric" });
}
