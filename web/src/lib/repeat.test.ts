import { describe, expect, it } from "vitest";
import { dateLine, repeatPattern, whenLine, type RepeatDate } from "./repeat";

// Wednesday Nov 4 2026, 7:00 PM in Denver (MST, UTC-7) = 02:00 UTC Thursday.
const denverWed = (date: string, utcDay: string): RepeatDate & { endsAt: string } => ({
  date,
  startsAt: `${utcDay}T02:00:00Z`,
  endsAt: `${utcDay}T03:30:00Z`,
});
const WEDS = [denverWed("2026-11-04", "2026-11-05"), denverWed("2026-11-11", "2026-11-12"), denverWed("2026-11-18", "2026-11-19")];
const NOW = new Date("2026-10-30T12:00:00Z");

// Dates at noon UTC keep the weekday the same in the UTC zone.
function noon(...days: string[]): RepeatDate[] {
  return days.map((d) => ({ date: d, startsAt: `${d}T12:00:00Z` }));
}

describe("repeatPattern", () => {
  it("says nothing for a single date", () => {
    expect(repeatPattern("", noon("2026-11-04"), "UTC")).toBeNull();
  });

  it("names a weekly day", () => {
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=WE", noon("2026-11-04", "2026-11-11"), "UTC")).toBe("Every Wednesday");
    expect(repeatPattern("RRULE:FREQ=WEEKLY;BYDAY=WE;COUNT=6", noon("2026-11-04"), "UTC")).toBe("Every Wednesday");
  });

  it("takes the day from the first date when a weekly rule has no BYDAY", () => {
    expect(repeatPattern("FREQ=WEEKLY", noon("2026-11-04"), "UTC")).toBe("Every Wednesday");
  });

  it("joins several weekly days in week order", () => {
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=TH,TU", noon("2026-11-03"), "UTC")).toBe("Every Tuesday and Thursday");
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=FR,MO,WE", noon("2026-11-02"), "UTC")).toBe("Every Monday, Wednesday and Friday");
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=SU,SA", noon("2026-11-07"), "UTC")).toBe("Every Saturday and Sunday");
  });

  it("says every other week for INTERVAL=2", () => {
    expect(repeatPattern("FREQ=WEEKLY;INTERVAL=2;BYDAY=WE", noon("2026-11-04"), "UTC")).toBe("Every other Wednesday");
    expect(repeatPattern("FREQ=WEEKLY;INTERVAL=2;BYDAY=TU,TH", noon("2026-11-03"), "UTC")).toBe("Every other Tuesday and Thursday");
  });

  it("says every day for DAILY", () => {
    expect(repeatPattern("FREQ=DAILY", noon("2026-11-04"), "UTC")).toBe("Every day");
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR,SA,SU", noon("2026-11-04"), "UTC")).toBe("Every day");
  });

  it("reads a position in the month", () => {
    expect(repeatPattern("FREQ=MONTHLY;BYDAY=1SA", noon("2026-11-07"), "UTC")).toBe("First Saturday of each month");
    expect(repeatPattern("FREQ=MONTHLY;BYDAY=2TU", noon("2026-11-10"), "UTC")).toBe("Second Tuesday of each month");
    expect(repeatPattern("FREQ=MONTHLY;BYDAY=-1FR", noon("2026-11-27"), "UTC")).toBe("Last Friday of each month");
    expect(repeatPattern("FREQ=MONTHLY;BYDAY=SA;BYSETPOS=1", noon("2026-11-07"), "UTC")).toBe("First Saturday of each month");
  });

  it("reads a day of the month", () => {
    expect(repeatPattern("FREQ=MONTHLY;BYMONTHDAY=15", noon("2026-11-15"), "UTC")).toBe("Monthly on the 15th");
    expect(repeatPattern("FREQ=MONTHLY;BYMONTHDAY=1", noon("2026-11-01"), "UTC")).toBe("Monthly on the 1st");
    expect(repeatPattern("FREQ=MONTHLY;BYMONTHDAY=22", noon("2026-11-22"), "UTC")).toBe("Monthly on the 22nd");
    expect(repeatPattern("FREQ=MONTHLY;BYMONTHDAY=23", noon("2026-11-23"), "UTC")).toBe("Monthly on the 23rd");
    expect(repeatPattern("FREQ=MONTHLY;BYMONTHDAY=12", noon("2026-11-12"), "UTC")).toBe("Monthly on the 12th");
    expect(repeatPattern("FREQ=MONTHLY;BYMONTHDAY=-1", noon("2026-11-30"), "UTC")).toBe("Monthly on the last day");
    expect(repeatPattern("FREQ=MONTHLY", noon("2026-11-09"), "UTC")).toBe("Monthly on the 9th");
  });

  it("falls back to Repeats for anything else", () => {
    for (const rule of [
      "FREQ=YEARLY",
      "FREQ=WEEKLY;INTERVAL=3;BYDAY=WE",
      "FREQ=MONTHLY;INTERVAL=2;BYDAY=1SA",
      "FREQ=MONTHLY;BYDAY=1SA,3SA",
      "FREQ=MONTHLY;BYMONTHDAY=1,15",
      "FREQ=MONTHLY;BYDAY=5SA",
      "FREQ=DAILY;BYHOUR=9,17",
      "FREQ=DAILY;INTERVAL=3",
      "FREQ=WEEKLY;BYDAY=XX",
      "garbage",
    ]) {
      expect(repeatPattern(rule, noon("2026-11-04"), "UTC"), rule).toBe("Repeats");
    }
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=WE", [], "UTC")).toBe("Repeats");
  });

  it("follows the viewer's zone when it moves the weekday", () => {
    // 7 PM Wednesday in Denver is Thursday in London and in Auckland.
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=WE", WEDS, "America/Denver")).toBe("Every Wednesday");
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=WE", WEDS, "Europe/London")).toBe("Every Thursday");
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=TU,WE", WEDS, "Europe/London")).toBe("Every Wednesday and Thursday");
    expect(repeatPattern("FREQ=WEEKLY", WEDS, "Pacific/Auckland")).toBe("Every Thursday");
  });

  it("keeps a monthly position only while the zone keeps its weekday", () => {
    // Saturday Nov 7, 7 PM in Denver is Sunday Nov 8 in London, where "first
    // Saturday" is no longer one stable weekday position.
    const sat = [{ date: "2026-11-07", startsAt: "2026-11-08T02:00:00Z" }];
    expect(repeatPattern("FREQ=MONTHLY;BYDAY=1SA", sat, "America/Denver")).toBe("First Saturday of each month");
    expect(repeatPattern("FREQ=MONTHLY;BYDAY=1SA", sat, "Europe/London")).toBe("Repeats");
    expect(repeatPattern("FREQ=MONTHLY;BYMONTHDAY=7", sat, "Europe/London")).toBe("Monthly on the 8th");
  });

  it("is not thrown off by one date edited onto another day", () => {
    const moved = [{ date: "2026-11-04", startsAt: "2026-11-06T12:00:00Z" }, ...noon("2026-11-11", "2026-11-18")];
    expect(repeatPattern("FREQ=WEEKLY;BYDAY=WE", moved, "UTC")).toBe("Every Wednesday");
  });
});

describe("whenLine", () => {
  it("reads a single date with its full weekday and one AM/PM", () => {
    const next = [{ date: "2026-11-05", startsAt: "2026-11-06T00:30:00Z", endsAt: "2026-11-06T03:00:00Z" }];
    expect(whenLine("", next, "America/Denver", NOW)).toBe("Thursday, Nov 5 · 5:30–8:00 PM");
  });

  it("adds the year only when it isn't this year", () => {
    const next = [{ date: "2027-01-07", startsAt: "2027-01-08T00:30:00Z", endsAt: "2027-01-08T03:00:00Z" }];
    expect(whenLine("", next, "America/Denver", NOW)).toBe("Thursday, Jan 7, 2027 · 5:30–8:00 PM");
  });

  it("keeps both meridiems when the time crosses noon", () => {
    expect(dateLine("2026-11-05T11:30:00Z", "2026-11-05T14:00:00Z", "UTC", NOW)).toBe("Thursday, Nov 5 · 11:30 AM–2:00 PM");
  });

  it("names the pattern, the time and the next date for a series", () => {
    expect(whenLine("FREQ=WEEKLY;BYDAY=WE", WEDS, "America/Denver", NOW)).toBe("Every Wednesday · 7:00–8:30 PM · next Nov 4");
  });

  it("shows the viewer's weekday, time and next date together", () => {
    expect(whenLine("FREQ=WEEKLY;BYDAY=WE", WEDS, "Europe/London", NOW)).toBe("Every Thursday · 2:00–3:30 AM · next Nov 5");
  });

  it("drops the time when the pattern is a fallback", () => {
    expect(whenLine("FREQ=YEARLY", WEDS, "America/Denver", NOW)).toBe("Repeats · next Nov 4");
  });

  it("has no line without a date", () => {
    expect(whenLine("FREQ=WEEKLY", [], "UTC", NOW)).toBeNull();
  });
});
