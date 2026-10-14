import { describe, expect, it } from "vitest";
import { shortZoneName, zoneLabel, zoneOptions, zoneSelectOptions } from "./eventForm";

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

describe("zoneSelectOptions", () => {
  it.each([
    ["America/Boise", "Yours · Boise (Mountain)"],
    ["America/Indiana/Knox", "Yours · Knox, Indiana (Central)"],
  ])("lists browser zone %s first even when the value is the church zone", (browser, label) => {
    const o = zoneSelectOptions("America/Los_Angeles", browser, "America/Los_Angeles", "en-US");
    expect(o[0]).toEqual({ value: browser, label });
    expect(o[1]).toEqual({ value: "America/Los_Angeles", label: "Church · Los Angeles (Pacific)" });
    expect(o.length).toBeGreaterThan(2);
    const values = o.map((z) => z.value);
    expect(new Set(values).size).toBe(values.length);
  });

  it("tags one entry when the browser is in the church zone", () => {
    const o = zoneSelectOptions("America/Boise", "America/Boise", "America/Boise", "en-US");
    expect(o[0]).toEqual({ value: "America/Boise", label: "Yours & church · Boise (Mountain)" });
    expect(o.filter((z) => z.value === "America/Boise")).toHaveLength(1);
  });

  it("keeps IANA ids as values and the same set of options as before", () => {
    const o = zoneSelectOptions("America/Los_Angeles", "America/Boise", "Etc/GMT+7", "en-US");
    const plain = zoneOptions("America/Los_Angeles", "America/Boise").map((z) => z.value);
    expect(o.slice(0, plain.length).map((z) => z.value)).toEqual(plain);
    expect(o.some((z) => z.value === "Etc/GMT+7")).toBe(true);
    expect(o.every((z) => /^[A-Za-z_+\-0-9/]+$/.test(z.value))).toBe(true);
  });

  it("keeps every label short enough for a phone-width field", () => {
    const o = zoneSelectOptions("America/Los_Angeles", "America/Indiana/Knox", null, "en-US");
    for (const z of o.slice(0, 12)) expect(z.label.length).toBeLessThanOrEqual(33);
    expect(zoneSelectOptions("America/Boise", "America/Boise", null, "en-US")[0].label.length).toBeLessThanOrEqual(33);
  });
});
