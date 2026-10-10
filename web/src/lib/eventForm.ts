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
