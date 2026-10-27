import { describe, expect, it } from "vitest";
import { badgeParts, formatCalendarDay, formatTime, formatWhen, heldIn, zoneNote } from "./when";

// Saturday Oct 17 2026, 7:00 PM in Los Angeles (PDT, UTC-7) = 02:00 UTC Sunday.
const laSat7pm = "2026-10-18T02:00:00Z";

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
