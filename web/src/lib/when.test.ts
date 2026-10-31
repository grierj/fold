import { describe, expect, it } from "vitest";
import { badgeParts, formatCalendarDay, formatPeriod, formatTime, formatWhen, formatWhenRange, heldIn, historyDayBounds, zoneNote, formatNextMeeting, minuteOfDay, formatHour, wallClock, wallToInstant, formatWhenIn } from "./when";

// Saturday Oct 17 2026, 7:00 PM in Los Angeles (PDT, UTC-7) = 02:00 UTC Sunday.
const laSat7pm = "2026-10-18T02:00:00Z";

it("shortens current-year phone week titles but keeps other years explicit", () => {
  expect(formatPeriod("week", "2026-10-25", "2026-10-31", "2026-10-28")).toBe("Oct 25 – 31");
  expect(formatPeriod("week", "2027-10-24", "2027-10-30", "2026-10-28")).toBe("Oct 24 – 30, 2027");
  expect(formatPeriod("week", "2026-12-27", "2027-01-02", "2026-10-28")).toBe("Dec 27, 2026 – Jan 2, 2027");
});

describe("formatWhen", () => {
  it("converts to the viewer's zone", () => {
    const now = new Date("2026-10-01T12:00:00Z");
    expect(formatWhen(laSat7pm, "America/Los_Angeles", now)).toBe("Sat, Oct 17 at 7:00 PM");
    expect(formatWhen(laSat7pm, "America/Boise", now)).toBe("Sat, Oct 17 at 8:00 PM");
  });

  it("moves to the next date when the viewer's clock has passed midnight", () => {
    // 10:30 PM Saturday in LA is 1:30 AM Sunday in New York.
    const late = "2026-10-18T05:30:00Z";
    const now = new Date("2026-10-01T12:00:00Z");
    expect(formatWhen(late, "America/Los_Angeles", now)).toBe("Sat, Oct 17 at 10:30 PM");
    expect(formatWhen(late, "America/New_York", now)).toBe("Sun, Oct 18 at 1:30 AM");
    expect(badgeParts(late, "America/New_York")).toEqual({ month: "Oct", day: "18" });
    expect(badgeParts(late, "America/Los_Angeles")).toEqual({ month: "Oct", day: "17" });
  });

  it("says Today and Tomorrow by the viewer's calendar", () => {
    const now = new Date("2026-10-17T20:00:00Z"); // 2 PM Saturday in Boise
    expect(formatWhen(laSat7pm, "America/Boise", now)).toBe("Today at 8:00 PM");
    expect(formatWhen(laSat7pm, "Europe/London", now)).toBe("Tomorrow at 3:00 AM");
  });

  it("follows each zone across the DST weeks", () => {
    // The US falls back Nov 1 2026; Europe fell back Oct 25. 7 PM in New
    // York is midnight in London before Oct 25, 11 PM in the gap week, and
    // midnight again after Nov 1.
    expect(formatTime("2026-10-21T23:00:00Z", "Europe/London")).toBe("12:00 AM");
    expect(formatTime("2026-10-28T23:00:00Z", "Europe/London")).toBe("11:00 PM");
    expect(formatTime("2026-11-05T00:00:00Z", "Europe/London")).toBe("12:00 AM");
    expect(formatTime("2026-11-05T00:00:00Z", "America/New_York")).toBe("7:00 PM");
  });
});

describe("formatWhenRange", () => {
  const now = new Date("2026-10-17T16:00:00Z");

  it("shows the date once when both times are on the same viewer day", () => {
    expect(formatWhenRange("2026-10-17T19:00:00Z", "2026-10-17T20:30:00Z", "America/New_York", now)).toBe(
      "Today at 3:00 PM – 4:30 PM",
    );
  });

  it("shows both dates when the range crosses viewer midnight", () => {
    expect(formatWhenRange("2026-10-18T03:30:00Z", "2026-10-18T04:30:00Z", "America/New_York", now)).toBe(
      "Today at 11:30 PM – Tomorrow at 12:30 AM",
    );
  });

  it("uses different viewer dates even when the UTC date is the same", () => {
    expect(formatWhenRange("2026-10-18T09:30:00Z", "2026-10-18T10:30:00Z", "Pacific/Honolulu", new Date("2026-10-01T12:00:00Z"))).toBe(
      "Sat, Oct 17 at 11:30 PM – Sun, Oct 18 at 12:30 AM",
    );
  });
});

describe("formatCalendarDay", () => {
  it("formats stored days without shifting or exposing ISO text", () => {
    expect(formatCalendarDay("2026-11-07")).toBe("Nov 7");
  });
});

describe("heldIn", () => {
  it("names the event's zone and clock when it differs", () => {
    expect(heldIn(laSat7pm, "America/Los_Angeles", "America/Boise")).toBe("Held in Pacific time (7:00 PM PDT)");
  });

  it("is quiet when the clocks match, even under another zone id", () => {
    expect(heldIn(laSat7pm, "America/Los_Angeles", "America/Los_Angeles")).toBeNull();
    expect(heldIn(laSat7pm, "America/Denver", "America/Boise")).toBeNull();
  });

  it("adds the weekday when the event's date differs from the viewer's", () => {
    expect(heldIn("2026-10-18T05:30:00Z", "America/Los_Angeles", "America/New_York")).toBe(
      "Held in Pacific time (Sat, 10:30 PM PDT)",
    );
  });

  it("stays quiet for a zone the browser can't name", () => {
    expect(heldIn(laSat7pm, "Mars/Olympus_Mons", "America/Boise")).toBeNull();
  });

  it("uses the standard-time name after the change", () => {
    expect(heldIn("2026-11-08T03:00:00Z", "America/Los_Angeles", "America/Boise")).toBe("Held in Pacific time (7:00 PM PST)");
  });
});

describe("zoneNote", () => {
  it("names the zone the form edits in", () => {
    expect(zoneNote("America/Los_Angeles")).toBe("Times are in Pacific time.");
  });
});

describe("historyDayBounds", () => {
  it("uses an inclusive local From day and the next local day as the exclusive Until bound", () => {
    expect(historyDayBounds("2026-10-01", "2026-10-03", "UTC")).toEqual({
      from: "2026-10-01T00:00:00.000Z",
      until: "2026-10-04T00:00:00.000Z",
    });
    expect(historyDayBounds("2026-10-01", "2026-10-01", "UTC")).toEqual({
      from: "2026-10-01T00:00:00.000Z",
      until: "2026-10-02T00:00:00.000Z",
    });
  });

  it("follows short and long Denver days instead of adding 24 hours", () => {
    expect(historyDayBounds("2026-03-08", "2026-03-08", "America/Denver")).toEqual({
      from: "2026-03-08T07:00:00.000Z",
      until: "2026-03-09T06:00:00.000Z",
    });
    expect(historyDayBounds("2026-11-01", "2026-11-01", "America/Denver")).toEqual({
      from: "2026-11-01T06:00:00.000Z",
      until: "2026-11-02T07:00:00.000Z",
    });
  });

  it("handles non-hour offsets, midnight gaps, and year boundaries", () => {
    expect(historyDayBounds("2026-10-01", "", "Asia/Kathmandu")).toEqual({
      from: "2026-09-30T18:15:00.000Z",
      until: "",
    });
    expect(historyDayBounds("2026-09-06", "", "America/Santiago")).toEqual({
      from: "2026-09-06T04:00:00.000Z",
      until: "",
    });
    expect(historyDayBounds("2026-12-31", "2026-12-31", "UTC")).toEqual({
      from: "2026-12-31T00:00:00.000Z",
      until: "2027-01-01T00:00:00.000Z",
    });
  });

  it("keeps blank bounds blank and rejects invalid input", () => {
    expect(historyDayBounds("", "", "UTC")).toEqual({ from: "", until: "" });
    expect(historyDayBounds("2026-02-30", "", "UTC")).toBeNull();
    expect(historyDayBounds("2026-10-03", "2026-10-01", "UTC")).toBeNull();
    expect(historyDayBounds("2026-10-01", "", "Mars/Olympus_Mons")).toBeNull();
  });
});

describe("formatNextMeeting", () => {
  const now = new Date("2026-10-18T15:00:00Z"); // Sunday 9 AM in Boise
  it("says Today before 5 PM and Tonight from 5 PM, in the viewer's zone", () => {
    expect(formatNextMeeting("2026-10-18T22:01:00Z", "America/Boise", now)).toEqual({ soon: true, text: "Today · 4:01 PM" });
    expect(formatNextMeeting("2026-10-18T23:00:00Z", "America/Boise", now)).toEqual({ soon: true, text: "Tonight · 5:00 PM" });
    expect(formatNextMeeting("2026-10-19T01:00:00Z", "America/Boise", now)).toEqual({ soon: true, text: "Tonight · 7:00 PM" });
  });
  it("keeps the viewer-zone day and the ordinary wording for other days", () => {
    expect(formatNextMeeting("2026-10-19T01:00:00Z", "America/New_York", now)).toEqual({ soon: true, text: "Tonight · 9:00 PM" });
    expect(formatNextMeeting("2026-10-19T23:00:00Z", "America/Boise", now)).toEqual({ soon: false, text: "Tomorrow at 5:00 PM" });
  });
});

describe("minuteOfDay", () => {
  it("reads the instant on the zone's wall clock", () => {
    // 01:30 UTC on Oct 22 is 7:30 PM on Oct 21 in Denver (MDT, UTC-6).
    expect(minuteOfDay("2026-10-22T01:30:00Z", "2026-10-21", "America/Denver")).toBe(19 * 60 + 30);
  });
  it("clamps to the day", () => {
    expect(minuteOfDay("2026-10-21T01:00:00Z", "2026-10-21", "America/Denver")).toBe(0);
    expect(minuteOfDay("2026-10-22T07:00:00Z", "2026-10-21", "America/Denver")).toBe(1440);
  });
});

describe("formatHour", () => {
  it("names the hour", () => {
    expect(formatHour(7)).toMatch(/^7\sAM$/);
    expect(formatHour(12)).toMatch(/^12\sPM$/);
    expect(formatHour(22)).toMatch(/^10\sPM$/);
  });
});

describe("wallToInstant and wallClock", () => {
  it("reads a church clock as its UTC instant, either side of daylight saving", () => {
    expect(wallToInstant("2026-11-03", "09:00", "America/Boise")).toBe("2026-11-03T16:00:00.000Z");
    expect(wallToInstant("2026-07-03", "09:00", "America/Boise")).toBe("2026-07-03T15:00:00.000Z");
    expect(wallToInstant("2026-07-03", "09:00", "Asia/Kolkata")).toBe("2026-07-03T03:30:00.000Z");
    expect(wallClock("2026-11-03T16:00:00.000Z", "America/Boise")).toEqual({ day: "2026-11-03", time: "09:00" });
    expect(wallClock("2026-11-04T03:30:00.000Z", "America/Boise")).toEqual({ day: "2026-11-03", time: "20:30" });
  });

  it("answers null for a malformed day or time or an unknown zone", () => {
    expect(wallToInstant("2026-02-30", "09:00", "UTC")).toBeNull();
    expect(wallToInstant("2026-11-03", "9am", "UTC")).toBeNull();
    expect(wallToInstant("", "09:00", "UTC")).toBeNull();
    expect(wallToInstant("2026-11-03", "09:00", "Nowhere/Special")).toBeNull();
  });
});

describe("formatWhenIn", () => {
  const now = new Date("2026-11-02T18:00:00Z");
  it("reads the church's clock and names its zone only when the viewer's differs", () => {
    expect(formatWhenIn("2026-11-03T16:00:00Z", "America/Boise", now, "America/Denver")).toBe("Tomorrow at 9:00 AM");
    expect(formatWhenIn("2026-11-03T16:00:00Z", "America/Boise", now, "America/New_York")).toBe("Tomorrow at 9:00 AM MST");
  });
});
