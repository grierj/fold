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

// A group card's next-meeting line (DEC-99). When the meeting is today in the
// viewer's zone it leads with "Today" (or "Tonight" from 5 PM on) and a
// separate time, so the card can show the word in the success tone; any other
// day is formatWhen's text with `soon` false.
export function formatNextMeeting(iso: string, zone = browserTimeZone(), now = new Date()): { soon: boolean; text: string } {
  if (ymd(new Date(iso), zone) !== ymd(now, zone)) return { soon: false, text: formatWhen(iso, zone, now) };
  const hour = Number(fmt(iso, zone, { hour: "numeric", hourCycle: "h23" }));
  const lead = hour >= 17 ? "Tonight" : "Today";
  const time = formatTime(iso, zone);
  return { soon: true, text: `${lead} · ${time}` };
}

export function formatWhenRange(start: string, end: string, zone = browserTimeZone(), now = new Date()): string {
  if (ymd(new Date(start), zone) === ymd(new Date(end), zone)) {
    return `${formatWhen(start, zone, now)} – ${formatTime(end, zone)}`;
  }
  return `${formatWhen(start, zone, now)} – ${formatWhen(end, zone, now)}`;
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
export function formatPeriod(view: "week" | "month", first: string, last: string, referenceDay?: string): string {
  if (view === "month") return fmt(asDate(first), "UTC", { month: "long", year: "numeric" });
  const [fy, fm] = [first.slice(0, 4), first.slice(5, 7)];
  const [ly, lm] = [last.slice(0, 4), last.slice(5, 7)];
  const md = (d: string) => fmt(asDate(d), "UTC", { month: "short", day: "numeric" });
  if (fy !== ly) return `${md(first)}, ${fy} – ${md(last)}, ${ly}`;
  const year = referenceDay?.slice(0, 4) === ly ? "" : `, ${ly}`;
  if (fm !== lm) return `${md(first)} – ${md(last)}${year}`;
  return `${md(first)} – ${Number(last.slice(8))}${year}`;
}

// "Nov 7", for a stored calendar day such as a series RSVP result.
export function formatCalendarDay(day: string): string {
  return fmt(asDate(day), "UTC", { month: "short", day: "numeric" });
}

// "May 4, 2016", for a birth date stored as YYYY-MM-DD.
export function formatBirthDate(day: string): string {
  return fmt(asDate(day), "UTC", { month: "long", day: "numeric", year: "numeric" });
}

// Calendar-day filters use the church's clock, not the browser's. The history
// API's upper bound is exclusive: an inclusive Until day becomes the next
// calendar day's start. Never add 24 hours to a zoned instant (DST days vary).
export function historyDayBounds(from: string, until: string, zone: string): { from: string; until: string } | null {
  function utcDay(day: string): Date | null {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(day)) return null;
    const date = new Date(`${day}T00:00:00.000Z`);
    return Number.isFinite(date.getTime()) && date.toISOString().slice(0, 10) === day ? date : null;
  }
  try {
    const calendar = new Intl.DateTimeFormat("en-US", {
      timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit", era: "short",
    });
    const keyAt = (at: number): number => {
      const parts = calendar.formatToParts(at);
      const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((p) => p.type === type)?.value ?? "";
      const year = Number(part("year"));
      return (part("era") === "BC" ? 1 - year : year) * 10000 + Number(part("month")) * 100 + Number(part("day"));
    };
    function start(day: string): string | null {
      const date = utcDay(day);
      if (!date) return null;
      const key = Number(day.replace(/-/g, ""));
      // Find the boundary of the church-local date rather than assuming
      // midnight exists: some zones start a spring-forward day at 01:00.
      let low = date.getTime() - 36 * 60 * 60 * 1000;
      let high = date.getTime() + 36 * 60 * 60 * 1000;
      while (low < high) {
        const middle = Math.floor((low + high) / 2);
        if (keyAt(middle) < key) low = middle + 1;
        else high = middle;
      }
      return keyAt(low) === key ? new Date(low).toISOString() : null;
    }
    if (from && until && from > until) return null;
    const lower = from ? start(from) : "";
    let upper: string | null = "";
    if (until) {
      const next = utcDay(until);
      if (!next) return null;
      next.setUTCDate(next.getUTCDate() + 1);
      upper = start(next.toISOString().slice(0, 10));
    }
    return lower === null || upper === null ? null : { from: lower, until: upper };
  } catch {
    return null;
  }
}

// --- The event page's "when" line (DEC-92) ---
// Pieces for "Thursday, Nov 5 · 5:30–8:00 PM" and "Every Wednesday · … · next
// Nov 4". repeat.ts assembles them; the formatting stays here.

// "Wednesday" for a weekday index (0 = Sunday), in the browser's language.
export function weekdayLongName(index: number): string {
  // 2026-10-18 is a Sunday.
  return fmt(asDate(`2026-10-${String(18 + index).padStart(2, "0")}`), "UTC", { weekday: "long" });
}

// "Thursday, Nov 5", with the year only when it isn't the viewer's current one.
export function formatEventDay(iso: string, zone = browserTimeZone(), now = new Date()): string {
  const otherYear = dayOf(iso, zone).slice(0, 4) !== ymd(now, zone).slice(0, 4);
  return fmt(iso, zone, { weekday: "long", month: "short", day: "numeric", ...(otherYear ? { year: "numeric" } : {}) });
}

// "Nov 4", or "Nov 4, 2027" in another year.
export function formatShortDay(iso: string, zone = browserTimeZone(), now = new Date()): string {
  const otherYear = dayOf(iso, zone).slice(0, 4) !== ymd(now, zone).slice(0, 4);
  return fmt(iso, zone, { month: "short", day: "numeric", ...(otherYear ? { year: "numeric" } : {}) });
}

// "5:30–8:00 PM": the shared AM/PM is said once. A start with nothing after
// it (a 24-hour clock) or an end on another day keeps both full times.
export function formatTimeRange(start: string, end: string, zone = browserTimeZone()): string {
  const from = formatTime(start, zone);
  if (!end || end === start) return from;
  const to = formatTime(end, zone);
  if (dayOf(start, zone) !== dayOf(end, zone)) return `${from}–${to}`;
  const tail = (s: string) => s.match(/\D+$/)?.[0] ?? "";
  const shared = tail(from) !== "" && tail(from) === tail(to);
  return `${shared ? from.slice(0, from.length - tail(from).length) : from}–${to}`;
}

// The home page's greeting by the viewer's local clock (DEC-94): morning
// before noon, afternoon before 5 PM, evening after.
export function greeting(now = new Date()): string {
  const hour = now.getHours();
  return hour < 12 ? "Good morning" : hour < 17 ? "Good afternoon" : "Good evening";
}

// The zone on an event form's When line: "Mountain time", or "UTC" (the
// generic name of UTC reads oddly). Empty until the zone is known (DEC-95).
export function zoneTimeName(tzid: string): string {
  if (!tzid) return "";
  return tzid === "UTC" || tzid === "Etc/UTC" ? "UTC" : zoneName(tzid);
}

// Minutes from midnight of `day` to the instant, on `zone`'s wall clock,
// clamped to the day (0 to 1440): a booking that began the evening before
// starts at 0, one that runs past midnight ends at 1440. The by-room view
// places its blocks with it.
export function minuteOfDay(iso: string, day: string, zone: string): number {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23",
  }).formatToParts(new Date(iso));
  const get = (t: string) => parts.find((p) => p.type === t)?.value ?? "00";
  const at = `${get("year")}-${get("month")}-${get("day")}`;
  if (at < day) return 0;
  if (at > day) return 1440;
  return Number(get("hour")) * 60 + Number(get("minute"));
}

// "7 AM", "12 PM": an hour line's label in the by-room view.
export function formatHour(hour: number): string {
  return fmt(`2026-01-01T${String(hour % 24).padStart(2, "0")}:00:00Z`, "UTC", { hour: "numeric" });
}

// --- A wall clock in a named zone (the composer's "Pick a date and time") ---

function wallKey(instant: number, zone: string): string {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23",
  }).formatToParts(instant);
  const get = (t: string) => parts.find((p) => p.type === t)?.value ?? "00";
  return `${get("year")}-${get("month")}-${get("day")}T${get("hour")}:${get("minute")}`;
}

// The day and time ("2026-11-03", "09:00") an instant reads as in `zone`.
export function wallClock(iso: string, zone: string): { day: string; time: string } {
  const key = wallKey(new Date(iso).getTime(), zone);
  return { day: key.slice(0, 10), time: key.slice(11) };
}

// The UTC instant (ISO) when `zone`'s clock reads `day` at `time`, or null
// for a malformed day or time or a zone this browser doesn't know. A time a
// spring-forward skips comes out an hour to one side of the change, and a
// time a fall-back repeats is one of its two instants (as Go's time.Date).
export function wallToInstant(day: string, time: string, zone: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  const t = /^(\d{2}):(\d{2})$/.exec(time);
  if (!m || !t) return null;
  const guess = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]), Number(t[1]), Number(t[2]));
  if (!Number.isFinite(guess) || new Date(guess).toISOString().slice(0, 10) !== day) return null;
  try {
    // Two passes find the offset in force at the answer, across a change.
    const offsetAt = (at: number) => Date.parse(`${wallKey(at, zone)}:00Z`) - at;
    let at = guess - offsetAt(guess);
    at = guess - offsetAt(at);
    return new Date(at).toISOString();
  } catch {
    return null;
  }
}
