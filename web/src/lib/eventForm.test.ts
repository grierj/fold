import { describe, expect, it } from "vitest";
import { filterZones, shortZoneName, US_ZONES, zoneLabel, zonePickerOptions } from "./eventForm";
import { zoneOffset } from "./when";

describe("zoneLabel", () => {
  it.each([
    ["America/Los_Angeles", "Los Angeles (Pacific)"],
    ["America/Boise", "Boise (Mountain)"],
    ["America/Indiana/Knox", "Knox, Indiana (Central)"],
    ["America/Argentina/San_Juan", "San Juan, Argentina (Argentina)"],
    ["America/Phoenix", "Phoenix (Mountain Standard)"],
    ["Pacific/Honolulu", "Honolulu (Hawaii-Aleutian)"],
    ["Asia/Kathmandu", "Kathmandu (Nepal)"],
    ["Australia/Lord_Howe", "Lord Howe"],
    ["UTC", "UTC"],
    ["Etc/UTC", "UTC"],
    ["Etc/GMT+7", "Etc/GMT+7"],
  ])("labels %s as %s", (tz, label) => {
    expect(zoneLabel(tz, "en-US")).toBe(label);
  });

  it("shows the place alone for an id the browser can't name", () => {
    expect(zoneLabel("Mars/Olympus_Mons", "en-US")).toBe("Olympus Mons");
  });
});

describe("shortZoneName", () => {
  it.each([
    ["Pacific Time", "Pacific"],
    ["Hawaii-Aleutian Standard Time", "Hawaii-Aleutian"],
    ["Mountain Standard Time", "Mountain Standard"],
    ["India Standard Time", "India"],
    ["United Kingdom Time", "United Kingdom"],
    ["Coordinated Universal", "Coordinated Universal"],
  ])("shortens %s to %s", (full, short) => expect(shortZoneName(full)).toBe(short));
});

describe("zonePickerOptions", () => {
  const off = (z: string) => zoneOffset(z, new Date("2026-01-15T12:00:00Z"));
  it.each([
    ["America/Boise", "Boise (Mountain)"],
    ["America/Indiana/Knox", "Knox, Indiana (Central)"],
  ])("lists browser zone %s first, then the church zone, then the six US zones", (browser, label) => {
    const o = zonePickerOptions("America/Los_Angeles", browser, "America/Los_Angeles", off, "en-US");
    expect(o[0]).toMatchObject({ value: browser, label, tag: "Yours" });
    expect(o[1]).toMatchObject({ value: "America/Los_Angeles", label: "Los Angeles (Pacific)", tag: "Church", offset: "UTC-8" });
    const lead = o.slice(2, 7).map((z) => z.value);
    expect(lead).toEqual(US_ZONES.filter((z) => z !== "America/Los_Angeles").slice(0, 5));
    const values = o.map((z) => z.value);
    expect(new Set(values).size).toBe(values.length);
  });

  it("tags one entry when the browser is in the church zone", () => {
    const o = zonePickerOptions("America/Boise", "America/Boise", "America/Boise", off, "en-US");
    expect(o[0]).toMatchObject({ value: "America/Boise", tag: "Yours & church" });
    expect(o.filter((z) => z.value === "America/Boise")).toHaveLength(1);
  });

  it("sorts the rest by label and keeps an unusual current value", () => {
    const o = zonePickerOptions(null, "America/Boise", "Etc/GMT+7", off, "en-US");
    expect(o.some((z) => z.value === "Etc/GMT+7")).toBe(true);
    expect(o.some((z) => z.tag === "Church")).toBe(false);
    const rest = o.slice(1 + US_ZONES.length).map((z) => z.label);
    expect(rest).toEqual([...rest].sort((a, b) => a.localeCompare(b, "en-US")));
  });
});

describe("filterZones", () => {
  const off = (z: string) => zoneOffset(z, new Date("2026-01-15T12:00:00Z"));
  const o = zonePickerOptions("America/Boise", "America/Indiana/Knox", null, off, "en-US");
  const values = (q: string) => filterZones(o, q).map((z) => z.value);
  it("matches the region name, keeping the list order", () => {
    expect(values("pac")[0]).toBe("America/Los_Angeles");
    expect(values("mountain").slice(0, 2)).toEqual(["America/Boise", "America/Denver"]);
    expect(values("mountain")).toContain("America/Phoenix");
  });
  it("matches the city, and the IANA id only when the query has a slash", () => {
    expect(values("los ang")).toEqual(["America/Los_Angeles"]);
    expect(values("indiana")).toContain("America/Indiana/Knox");
    expect(values("pac")).not.toContain("Pacific/Auckland");
    expect(values("pac")).not.toContain("Pacific/Apia");
    expect(values("pacific/")).toContain("Pacific/Auckland");
  });
  it("ranks Yours and Church, then US zones, then label prefixes, then the rest", () => {
    // Yours is Knox (Central), Church is Boise (Mountain).
    expect(values("central")[0]).toBe("America/Indiana/Knox");
    expect(values("central")[1]).toBe("America/Chicago");
    expect(values("mountain")[0]).toBe("America/Boise");
    const b = values("bo");
    expect(b[0]).toBe("America/Boise");
    const prefix = b.slice(1).findIndex((z) => !zoneLabel(z, "en-US").toLowerCase().startsWith("bo"));
    const afterPrefix = b.slice(1 + prefix);
    expect(prefix).toBeGreaterThan(0);
    expect(afterPrefix.every((z) => !zoneLabel(z, "en-US").toLowerCase().startsWith("bo"))).toBe(true);
  });
  it("matches a UTC offset", () => {
    expect(values("-7")).toEqual(expect.arrayContaining(["America/Boise", "America/Denver", "America/Phoenix"]));
    expect(values("utc-7")).not.toContain("America/Los_Angeles");
    expect(values("+5:30")).toContain("Asia/Colombo");
    expect(values("UTC+5:30")).toContain("Asia/Colombo");
  });
  it("shows everything for an empty query and nothing for nonsense", () => {
    expect(filterZones(o, "  ")).toHaveLength(o.length);
    expect(values("zzzz")).toEqual([]);
  });
});
