import { weekdayIndex } from "./days";

// The weekly rules the event forms write and read: FREQ=WEEKLY with an
// optional BYDAY list. Fold refuses sub-daily rules (DEC-89); anything more
// than a weekly list is left as the raw rule text. lib/repeat.ts words the
// wider rules for display.

// Mon-first, as the weekday picker lists them.
export const WEEKDAYS = [
  { label: "Mon", value: "MO" },
  { label: "Tue", value: "TU" },
  { label: "Wed", value: "WE" },
  { label: "Thu", value: "TH" },
  { label: "Fri", value: "FR" },
  { label: "Sat", value: "SA" },
  { label: "Sun", value: "SU" },
];

// Indexed like Date.getDay(): 0 = Sunday.
export const WEEKDAY_CODES = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"];

// The RRULE code of a YYYY-MM-DD calendar date's weekday (no zone shift).
export function weekdayCode(date: string): string {
  return WEEKDAY_CODES[weekdayIndex(date)];
}

// "FREQ=WEEKLY;BYDAY=MO,WE", or "" for a single date.
export function weeklyRule(days: string[]): string {
  return `FREQ=WEEKLY;BYDAY=${days.join(",")}`;
}

export function isSimpleWeekly(rule: string): boolean {
  return rule === "" || /^FREQ=WEEKLY(;BYDAY=[A-Z,]+)?$/.test(rule);
}

export function isWeekly(rule: string): boolean {
  return rule.startsWith("FREQ=WEEKLY");
}

export function bydays(rule: string): string[] {
  const m = rule.match(/BYDAY=([A-Z,]+)/);
  return m ? m[1].split(",").filter(Boolean) : [];
}

// A rule's KEY=value parts, keys upper-cased, any "RRULE:" prefix dropped.
export function parseRule(rule: string): Map<string, string> {
  const parts = new Map<string, string>();
  for (const part of rule.replace(/^RRULE:/i, "").split(";")) {
    const [key, value] = part.split("=");
    if (key && value !== undefined) parts.set(key.toUpperCase(), value);
  }
  return parts;
}
