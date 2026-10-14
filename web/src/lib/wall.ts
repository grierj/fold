// Wall-clock display. startsLocal is YYYY-MM-DDTHH:MM in the event's zone
// and must not be parsed as a UTC instant: a 7:00 pm Boise event is the
// next UTC day, and the badge would show the wrong date.

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone;
  } catch {
    return "UTC";
  }
}

export function wallDate(local: string): Date {
  const [y, m, d] = local.slice(0, 10).split("-").map(Number);
  return new Date(y, (m || 1) - 1, d || 1);
}

export function formatWallClock(local: string): string {
  const hm = (local.split("T")[1] ?? "00:00").slice(0, 5);
  const [h, m] = hm.split(":").map(Number);
  return new Date(2000, 0, 1, h || 0, m || 0).toLocaleTimeString(undefined, {
    hour: "numeric",
    minute: "2-digit",
  });
}

function ymdInZone(instant: Date, tzid: string): string {
  try {
    return new Intl.DateTimeFormat("en-CA", {
      timeZone: tzid,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).format(instant);
  } catch {
    return instant.toISOString().slice(0, 10);
  }
}

// YYYY-MM-DDTHH:MM wall clock of instant in tzid.
export function localInZone(instant: Date, tzid: string): string {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: tzid,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).formatToParts(instant);
  const get = (t: string) => parts.find((p) => p.type === t)?.value ?? "00";
  return `${get("year")}-${get("month")}-${get("day")}T${get("hour")}:${get("minute")}`;
}

function addDays(ymd: string, days: number): string {
  const [y, m, d] = ymd.split("-").map(Number);
  return new Date(Date.UTC(y, m - 1, d + days)).toISOString().slice(0, 10);
}

// Short zone name when the browser is not already in tzid. Empty when it is.
export function zoneSuffix(utcIso: string | null | undefined, tzid: string | null | undefined): string {
  if (!tzid || browserTimeZone() === tzid) return "";
  if (!utcIso) return ` ${tzid}`;
  try {
    const name = new Intl.DateTimeFormat(undefined, {
      timeZone: tzid,
      timeZoneName: "short",
    })
      .formatToParts(new Date(utcIso))
      .find((p) => p.type === "timeZoneName")?.value;
    return name ? ` ${name}` : ` ${tzid}`;
  } catch {
    return ` ${tzid}`;
  }
}

// "Today at 7:00 PM", in the event's zone, plus a zone label when the
// browser zone is different.
export function formatNextEvent(
  local: string | null | undefined,
  tzid: string | null | undefined,
  utcIso: string | null | undefined
): string {
  if (!local) return "No events scheduled yet";
  // The API always sends tzid with a next event (guarded by a Go test). If
  // it is ever missing, show the instant in the viewer's own zone rather
  // than labelling the event's wall clock as UTC, which shows the wrong day.
  if (!tzid) {
    if (!utcIso) return "No events scheduled yet";
    const zone = browserTimeZone();
    return formatNextEvent(localInZone(new Date(utcIso), zone), zone, utcIso);
  }
  const zone = tzid;
  const day = local.slice(0, 10);
  const today = ymdInZone(new Date(), zone);
  const time = formatWallClock(local);
  let when: string;
  if (day === today) when = `Today at ${time}`;
  else if (day === addDays(today, 1)) when = `Tomorrow at ${time}`;
  else {
    when =
      wallDate(local).toLocaleDateString(undefined, {
        weekday: "short",
        month: "short",
        day: "numeric",
      }) + ` at ${time}`;
  }
  return when + zoneSuffix(utcIso, zone);
}
