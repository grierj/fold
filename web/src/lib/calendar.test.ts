import { describe, expect, it } from "vitest";
import { addDays, addMonths, groupByDay, monthGrid, rangeFor, weekRange } from "./calendar";

describe("calendar dates", () => {
  it("starts weeks on Sunday", () => {
    expect(weekRange("2026-10-21")).toEqual(["2026-10-18", "2026-10-19", "2026-10-20", "2026-10-21", "2026-10-22", "2026-10-23", "2026-10-24"]);
    expect(weekRange("2026-10-18")[0]).toBe("2026-10-18");
  });

  it("steps over a daylight-saving week without losing a day", () => {
    // US clocks fall back on Sunday, Nov 1, 2026.
    expect(weekRange("2026-11-03")).toEqual(["2026-11-01", "2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05", "2026-11-06", "2026-11-07"]);
    expect(addDays("2026-10-31", 1)).toBe("2026-11-01");
  });

  it("builds a 6-week month grid that holds the whole month", () => {
    const g = monthGrid("2026-10-19");
    expect(g).toHaveLength(6);
    expect(g.every((w) => w.length === 7)).toBe(true);
    expect(g[0][0]).toBe("2026-09-27");
    expect(g[0][4]).toBe("2026-10-01");
    expect(g.flat()).toContain("2026-10-31");
  });

  it("moves by months from the 1st", () => {
    expect(addMonths("2026-01-31", 1)).toBe("2026-02-01");
    expect(addMonths("2026-01-15", -1)).toBe("2025-12-01");
  });

  it("gives each view its range", () => {
    expect(rangeFor("week", "2026-10-21")).toEqual({ from: "2026-10-18", to: "2026-10-24" });
    expect(rangeFor("month", "2026-10-21")).toEqual({ from: "2026-09-27", to: "2026-11-07" });
    expect(rangeFor("agenda", "2026-10-21")).toEqual({ from: "2026-10-21", to: "2026-11-19" });
    expect(rangeFor("agenda", "2026-10-21", 60).to).toBe("2026-12-19");
  });

  it("buckets by the viewer's day, so 11:30 PM Pacific is the next day in New York", () => {
    const occ = { startsAt: "2026-10-22T06:30:00Z" };
    expect([...groupByDay([occ], "America/Los_Angeles").keys()]).toEqual(["2026-10-21"]);
    expect([...groupByDay([occ], "America/New_York").keys()]).toEqual(["2026-10-22"]);
  });
});
