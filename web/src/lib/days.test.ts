import { describe, expect, it } from "vitest";
import { addDays, addMonths, daysBetween, weekdayIndex } from "./days";

describe("day math", () => {
  it("steps across both daylight-saving changes without losing or repeating a day", () => {
    // US clocks spring forward Sunday, Mar 8, and fall back Sunday, Nov 1, 2026.
    expect(addDays("2026-03-07", 1)).toBe("2026-03-08");
    expect(addDays("2026-03-08", 1)).toBe("2026-03-09");
    expect(addDays("2026-10-31", 1)).toBe("2026-11-01");
    expect(addDays("2026-11-01", 1)).toBe("2026-11-02");
    expect(addDays("2026-11-02", -1)).toBe("2026-11-01");
    expect(addDays("2026-12-31", 1)).toBe("2027-01-01");
  });

  it("counts calendar days across a daylight-saving change", () => {
    expect(daysBetween("2026-10-31", "2026-11-02")).toBe(2);
    expect(daysBetween("2026-03-07", "2026-03-09")).toBe(2);
    expect(daysBetween("2026-11-02", "2026-11-01")).toBe(-1);
    expect(daysBetween("2026-05-04", "2026-05-04")).toBe(0);
  });

  it("moves by months from the 1st", () => {
    expect(addMonths("2026-01-31", 1)).toBe("2026-02-01");
    expect(addMonths("2026-01-15", -1)).toBe("2025-12-01");
  });

  it("names the weekday of the calendar day itself, in any zone", () => {
    expect(weekdayIndex("2026-10-18")).toBe(0);
    expect(weekdayIndex("2026-11-01")).toBe(0);
    expect(weekdayIndex("2026-03-08")).toBe(0);
    expect(weekdayIndex("2026-11-02")).toBe(1);
  });

  it("agrees with the local calendar for every day of a year that has both clock changes", () => {
    for (let day = "2026-01-01"; day < "2027-01-01"; day = addDays(day, 1)) {
      const [y, m, d] = day.split("-").map(Number);
      expect(weekdayIndex(day), day).toBe(new Date(y, m - 1, d).getDay());
    }
  });
});
