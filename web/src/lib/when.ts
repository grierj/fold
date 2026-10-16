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
export function zoneName(tzid: string): string {
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
