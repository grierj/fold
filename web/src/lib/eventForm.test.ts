import { describe, expect, it } from "vitest";
import { zoneOptions } from "./eventForm";

describe("zoneOptions", () => {
  it("puts the browser zone first, then the church zone, with no repeats", () => {
    const o = zoneOptions("America/Boise", "America/New_York");
    expect(o[0]).toEqual({ value: "America/New_York", label: "Your time zone" });
    expect(o[1]).toEqual({ value: "America/Boise", label: "Church time zone" });
    const values = o.map((z) => z.value);
    expect(new Set(values).size).toBe(values.length);
  });

  it("shows one entry when the browser is in the church zone", () => {
    const o = zoneOptions("America/Boise", "America/Boise");
    expect(o[0]).toEqual({ value: "America/Boise", label: "Your time zone (church time zone)" });
    expect(o.filter((z) => z.value === "America/Boise")).toHaveLength(1);
  });

  it("works before the church zone loads", () => {
    expect(zoneOptions(null, "UTC")[0]).toEqual({ value: "UTC", label: "Your time zone" });
  });
});
