export const COMMON_ZONES = [
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Boise",
  "America/Phoenix",
  "America/Los_Angeles",
  "America/Anchorage",
  "Pacific/Honolulu",
  "Europe/London",
  "UTC",
];

export const WEEKDAYS = [
  { label: "Mon", value: "MO" },
  { label: "Tue", value: "TU" },
  { label: "Wed", value: "WE" },
  { label: "Thu", value: "TH" },
  { label: "Fri", value: "FR" },
  { label: "Sat", value: "SA" },
  { label: "Sun", value: "SU" },
];

const CODES = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"];

// Weekday of a YYYY-MM-DD calendar date, in that date's own zone (no UTC shift).
export function weekdayCode(date: string): string {
  const [y, m, d] = date.split("-").map(Number);
  return CODES[new Date(y, (m || 1) - 1, d || 1).getDay()];
}

export function isSimpleWeekly(rule: string): boolean {
  return rule === "" || /^FREQ=WEEKLY(;BYDAY=[A-Z,]+)?$/.test(rule);
}

export function bydays(rule: string): string[] {
  const m = rule.match(/BYDAY=([A-Z,]+)/);
  return m ? m[1].split(",").filter(Boolean) : [];
}

export interface ZoneOption {
  value: string;
  label: string;
}

// Zone suggestions for event forms (DEC-50): the browser's zone first,
// then the church's, then the common list. No zone appears twice.
export function zoneOptions(church: string | null | undefined, browser: string | null | undefined): ZoneOption[] {
  const out: ZoneOption[] = [];
  const seen = new Set<string>();
  const add = (value: string | null | undefined, label: string) => {
    if (!value || seen.has(value)) return;
    seen.add(value);
    out.push({ value, label });
  };
  if (browser && browser === church) add(browser, "Your time zone (church time zone)");
  add(browser, "Your time zone");
  add(church, "Church time zone");
  for (const z of COMMON_ZONES) add(z, z);
  return out;
}

function allZones(): string[] {
  try {
    const f = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
    return f ? f("timeZone") : [];
  } catch {
    return [];
  }
}

// The zone's generic name in the reader's language ("Pacific Time"), the
// same in summer and winter, so it matches "the clock stays put". Falls back
// to the long name, then to nothing, if the browser can't name it.
export function zoneGenericName(tz: string, locale?: string): string {
  for (const style of ["longGeneric", "long"] as const) {
    try {
      const part = new Intl.DateTimeFormat(locale, { timeZone: tz, timeZoneName: style })
        .formatToParts(new Date())
        .find((p) => p.type === "timeZoneName");
      if (part?.value) return part.value;
    } catch {
      // unsupported style or unknown zone: try the next one
    }
  }
  return "";
}

// The generic name without its trailing "Time": "Pacific Time" -> "Pacific",
// "Hawaii-Aleutian Standard Time" -> "Hawaii-Aleutian". A zone that skips
// daylight saving inside a US region keeps "Standard" ("Mountain Standard"
// for Phoenix), so it doesn't read as the same clock as Denver.
const DST_REGIONS = new Set(["Pacific", "Mountain", "Central", "Eastern", "Alaska", "Atlantic"]);
export function shortZoneName(name: string): string {
  const std = name.match(/^(.+) Standard Time$/);
  if (std) return DST_REGIONS.has(std[1]) ? `${std[1]} Standard` : std[1];
  const t = name.match(/^(.+) Time$/);
  return t ? t[1] : name;
}

// A short, readable label for an IANA zone: "Los Angeles (Pacific)",
// "Knox, Indiana (Central)". Display only; the option value stays the IANA
// id. Zones with no place in the id (UTC, Etc/GMT+5) show the id, since a
// bare "GMT-05:00" would hide that Etc/GMT+5 is that zone.
export function zoneLabel(tz: string, locale?: string): string {
  if (tz === "UTC" || tz === "Etc/UTC") return "UTC";
  const parts = tz.split("/");
  if (parts.length < 2 || parts[0] === "Etc") return tz;
  const nice = (s: string) => s.replace(/_/g, " ");
  const place = parts.length > 2 ? `${nice(parts[parts.length - 1])}, ${nice(parts[parts.length - 2])}` : nice(parts[1]);
  const full = zoneGenericName(tz, locale);
  if (!full || /^GMT/.test(full)) return place;
  const name = shortZoneName(full);
  return name === place ? place : `${place} (${name})`;
}

// Options for <ZoneSelect>: the browser zone first, then the church zone,
// the common list, and every other IANA zone; the current value is always
// present (an existing series may use any zone). Labels are short enough
// to fit a phone-width field, and the special entries lead with what they
// are: "Church · Los Angeles (Pacific)", "Yours & church · Boise (Mountain)".
export function zoneSelectOptions(
  church: string | null | undefined,
  browser: string | null | undefined,
  current: string | null | undefined,
  locale?: string,
): ZoneOption[] {
  const tag = (z: string) =>
    z === browser && z === church ? "Yours & church · " : z === browser ? "Yours · " : z === church ? "Church · " : "";
  const out: ZoneOption[] = [];
  const seen = new Set<string>();
  for (const z of [...zoneOptions(church, browser).map((o) => o.value), ...allZones(), current ?? ""]) {
    if (z && !seen.has(z)) {
      seen.add(z);
      out.push({ value: z, label: tag(z) + zoneLabel(z, locale) });
    }
  }
  return out;
}
