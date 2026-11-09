import { describe, expect, it } from "vitest";
import { bydays, isSimpleWeekly, isWeekly, parseRule, weekdayCode, weeklyRule } from "./rrule";

describe("weekly rules", () => {
  it("writes and reads back the same days", () => {
    const rule = weeklyRule(["MO", "WE"]);
    expect(rule).toBe("FREQ=WEEKLY;BYDAY=MO,WE");
    expect(isSimpleWeekly(rule)).toBe(true);
    expect(isWeekly(rule)).toBe(true);
    expect(bydays(rule)).toEqual(["MO", "WE"]);
  });

  it("treats a single date and a bare weekly rule as simple, anything wider as not", () => {
    expect(isSimpleWeekly("")).toBe(true);
    expect(isSimpleWeekly("FREQ=WEEKLY")).toBe(true);
    expect(isSimpleWeekly("FREQ=WEEKLY;INTERVAL=2;BYDAY=MO")).toBe(false);
    expect(isSimpleWeekly("FREQ=MONTHLY;BYDAY=1SA")).toBe(false);
    expect(bydays("FREQ=WEEKLY")).toEqual([]);
  });

  it("names the weekday of a calendar date across the clock changes", () => {
    expect(weekdayCode("2026-03-08")).toBe("SU");
    expect(weekdayCode("2026-03-09")).toBe("MO");
    expect(weekdayCode("2026-11-01")).toBe("SU");
    expect(weekdayCode("2026-11-04")).toBe("WE");
  });

  it("splits a rule into upper-cased parts and drops the RRULE prefix", () => {
    expect([...parseRule("RRULE:freq=WEEKLY;byday=MO,TU;UNTIL=").entries()]).toEqual([["FREQ", "WEEKLY"], ["BYDAY", "MO,TU"], ["UNTIL", ""]]);
  });
});
