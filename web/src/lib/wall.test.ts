import { describe, expect, it } from "vitest";
import { formatNextEvent, localInZone } from "./wall";

describe("formatNextEvent", () => {
  it("never labels a missing zone as UTC", () => {
    // Saturday 7 PM Mountain = Sunday 01:00 UTC.
    const out = formatNextEvent("2035-10-20T01:00", "", "2035-10-20T01:00:00Z");
    expect(out).not.toMatch(/UTC/);
  });

  it("formats a Saturday 7 PM Mountain event as Saturday", () => {
    const out = formatNextEvent("2035-10-19T19:00", "America/Denver", "2035-10-20T01:00:00Z");
    expect(out).toMatch(/Oct 19/);
    expect(out).toMatch(/7:00/);
    expect(out).not.toMatch(/Oct 20/);
  });
});

describe("localInZone", () => {
  it("gives the wall clock in the zone", () => {
    expect(localInZone(new Date("2035-10-20T01:00:00Z"), "America/Denver")).toBe("2035-10-19T19:00");
    expect(localInZone(new Date("2035-10-20T00:30:00Z"), "UTC")).toBe("2035-10-20T00:30");
  });
});
