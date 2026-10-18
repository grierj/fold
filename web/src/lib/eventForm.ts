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

// The six US zones, listed right after "Yours" and "Church" in the time
// zone picker (DEC-65).
export const US_ZONES = [
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "America/Anchorage",
  "Pacific/Honolulu",
];

export interface ZonePick {
  value: string;
  label: string; // "Los Angeles (Pacific)"
  tag: "" | "Yours" | "Church" | "Yours & church";
  offset: string; // "UTC-7"
  search: string; // what typing matches: label, id, region name, offset
}

// Options for the time zone picker (DEC-65): the browser zone and the
// church zone first (tagged), then the six US zones, then every other IANA
// zone alphabetically by label. The current value is always present (an
// existing series may use any zone). Pass church = null for the church-zone
// setting itself, which has no separate "Church" entry.
export function zonePickerOptions(
  church: string | null | undefined,
  browser: string | null | undefined,
  current: string | null | undefined,
  offsetOf: (tz: string) => string,
  locale?: string,
): ZonePick[] {
  const tag = (z: string): ZonePick["tag"] =>
    z === browser && z === church ? "Yours & church" : z === browser ? "Yours" : z === church ? "Church" : "";
  const seen = new Set<string>();
  const make = (z: string): ZonePick => {
    const label = zoneLabel(z, locale);
    const offset = offsetOf(z);
    const search = [label, z.replace(/_/g, " "), zoneGenericName(z, locale), offset].join(" ").toLowerCase();
    return { value: z, label, tag: tag(z), offset, search };
  };
  const lead: ZonePick[] = [];
  for (const z of [browser, church, ...US_ZONES]) {
    if (z && !seen.has(z)) {
      seen.add(z);
      lead.push(make(z));
    }
  }
  const rest: ZonePick[] = [];
  for (const z of [...allZones(), current ?? ""]) {
    if (z && !seen.has(z)) {
      seen.add(z);
      rest.push(make(z));
    }
  }
  rest.sort((a, b) => a.label.localeCompare(b.label, locale));
  return [...lead, ...rest];
}

// "-7", "utc-7", "GMT -07:00", "+5:30" become "utc-7" or "utc+5:30" (the
// form zoneOffset uses); anything else is not an offset query.
function offsetQuery(q: string): string | null {
  const m = q.replace(/\u2212/g, "-").match(/^(?:utc|gmt)?\s*([+-])\s*(\d{1,2})(?::?(\d{2}))?$/);
  if (!m) return null;
  const h = Number(m[2]);
  const min = m[3] && m[3] !== "00" ? `:${m[3]}` : "";
  return h === 0 && !min ? "utc" : `utc${m[1]}${h}${min}`;
}

// Typing filters the picker: every word must appear in the label, the IANA
// id or the region name ("pacific", "mountain"), or the query is a UTC
// offset ("-7", "UTC+5:30"). The list keeps its order.
export function filterZones(options: ZonePick[], query: string): ZonePick[] {
  const q = query.trim().toLowerCase();
  if (!q) return options;
  const off = offsetQuery(q);
  if (off) return options.filter((o) => o.offset.toLowerCase() === off);
  const words = q.split(/\s+/);
  return options.filter((o) => words.every((w) => o.search.includes(w)));
}
