// Time zone names and the zone picker's options: how a zone reads to a person
// ("Pacific Time", "Los Angeles (Pacific)") and how typing filters them. Stable
// display helpers; the names themselves come from lib/when.

import { shortZoneName, zoneGenericName } from "./when";

function allZones(): string[] {
  try {
    const f = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
    return f ? f("timeZone") : [];
  } catch {
    return [];
  }
}

// A short, readable label for an IANA zone: "Los Angeles (Pacific)",
// "Knox, Indiana (Central)". Display only; the option value stays the IANA
// id. Zones with no place in the id (UTC, Etc/GMT+5) show the id, since a
// bare "GMT-05:00" would hide that Etc/GMT+5 is that zone.
/** @public tested directly: the label table. */
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
/** @public tested directly: the picker's lead zones. */
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
  search: string; // what typing matches: label, region name, offset (not the id)
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
    const search = [label, zoneGenericName(z, locale), offset].join(" ").toLowerCase();
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

// Typing filters the picker (PR7-REVIEW F1): every word must appear in the
// label or the region name ("pacific", "mountain"), or the query is a UTC
// offset ("-7", "UTC+5:30"). The IANA id ("Pacific/Auckland") only counts
// when the query has a "/", so "pac" means Pacific time, not every
// Pacific/ island. Matches rank Yours and Church first, then the US zones,
// then labels that start with the query, then the rest, each in list order.
export function filterZones(options: ZonePick[], query: string): ZonePick[] {
  const q = query.trim().toLowerCase();
  if (!q) return options;
  const off = offsetQuery(q);
  if (off) return options.filter((o) => o.offset.toLowerCase() === off);
  const words = q.split(/\s+/);
  const byId = q.includes("/");
  const hits = options.filter((o) => {
    const hay = byId ? `${o.search} ${o.value.replace(/_/g, " ").toLowerCase()}` : o.search;
    return words.every((w) => hay.includes(w));
  });
  const rank = (o: ZonePick) => (o.tag ? 0 : US_ZONES.includes(o.value) ? 1 : o.label.toLowerCase().startsWith(q) ? 2 : 3);
  return hits.map((o, i) => ({ o, i, r: rank(o) })).sort((a, b) => a.r - b.r || a.i - b.i).map((x) => x.o);
}
