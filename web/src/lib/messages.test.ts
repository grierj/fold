import { describe, expect, it } from "vitest";
import { composeOptions, summary } from "../test/compose";
import { audienceLabel, confirmLine, emptyChoice, fromAudience, isOneEvent, prefillChoice, statusLabel, summaryLine, teenLine, toAudience } from "./messages";

describe("audiences", () => {
  it("turns the picker's choice into the API's audience and back", () => {
    const o = composeOptions();
    expect(toAudience(emptyChoice())).toBeNull();
    expect(toAudience({ ...emptyChoice(), target: "group:7" })).toEqual({ type: "group", groupId: 7, roles: ["member", "leader"] });
    expect(toAudience({ ...emptyChoice(), target: "group:7", roles: [] })).toBeNull();
    expect(toAudience({ ...emptyChoice(), target: "event:3" })).toEqual({ type: "event_series", eventId: 3, statuses: ["going", "waitlisted"] });
    expect(toAudience({ ...emptyChoice(), target: "event:3", dates: "32", statuses: ["maybe"] })).toEqual({ type: "occurrence", occurrenceId: 32, statuses: ["maybe"] });
    expect(toAudience({ ...emptyChoice(), target: "all" })).toEqual({ type: "all_members" });
    expect(fromAudience({ type: "occurrence", occurrenceId: 32, statuses: ["going"] }, o)).toMatchObject({ target: "event:3", dates: "32", statuses: ["going"] });
    expect(fromAudience({ type: "occurrence", occurrenceId: 99 }, o)).toMatchObject({ target: "occurrence:99" });
    expect(toAudience(fromAudience({ type: "occurrence", occurrenceId: 99 }, o))).toEqual({ type: "occurrence", occurrenceId: 99, statuses: ["going", "waitlisted"] });
  });

  it("prefills from the address", () => {
    const o = composeOptions();
    expect(prefillChoice(new URLSearchParams("group=7"), o).target).toBe("group:7");
    expect(prefillChoice(new URLSearchParams("event=3"), o)).toMatchObject({ target: "event:3", dates: "all" });
    expect(prefillChoice(new URLSearchParams("occurrence=31"), o)).toMatchObject({ target: "event:3", dates: "31" });
    expect(prefillChoice(new URLSearchParams(""), o).target).toBe("");
  });

  it("knows which audiences take event fields", () => {
    expect(isOneEvent({ type: "occurrence", occurrenceId: 1 })).toBe(true);
    expect(isOneEvent({ type: "event_series", eventId: 1 })).toBe(true);
    expect(isOneEvent({ type: "group", groupId: 1 })).toBe(false);
    expect(isOneEvent(null)).toBe(false);
  });

  it("says who a message goes to", () => {
    const o = composeOptions();
    expect(audienceLabel({ type: "group", groupId: 7, roles: ["member", "leader"] }, o)).toBe("Book club (Members, Leaders)");
    expect(audienceLabel({ type: "event_series", eventId: 3, statuses: ["going", "waitlisted"] }, o)).toBe("Book night, all upcoming dates (Going, Waitlisted)");
    expect(audienceLabel({ type: "group", groupId: 99 }, o)).toBe("A group that's no longer listed (Members, Leaders)");
    expect(audienceLabel({ type: "all_members" }, null)).toBe("Everyone at church");
  });
});

describe("counts", () => {
  it("reads the live count with each reason as the API names it", () => {
    expect(summaryLine(summary({ people: 142, eligible: 133, skipped: { "turned off messages from leaders": 6, "no email address": 3 } })))
      .toBe("142 people; 9 won't get it: 6 turned off messages from leaders, 3 have no email address");
    expect(summaryLine(summary({ people: 2, eligible: 1, skipped: { "no email address": 1 } }))).toBe("2 people; 1 won't get it: 1 has no email address");
    expect(summaryLine(summary({ people: 1, eligible: 1, skipped: {} }))).toBe("1 person; everyone on the list will get it.");
    expect(summaryLine(summary({ people: 0, eligible: 0, skipped: {} }))).toBe("No one is on this list yet.");
    expect(teenLine(summary({ teens: 0 }))).toBe("");
    expect(teenLine(summary({ teens: 3 }))).toBe("3 of them are teens.");
  });

  it("asks before sending with the count", () => {
    expect(confirmLine(133, null, "America/Boise")).toBe("Send to 133 people now?");
    expect(confirmLine(1, "2026-11-05T16:00:00Z", "America/Boise")).toMatch(/^Send to 1 person on Thu, Nov 5 at 9:00 AM( MST)?\?$/);
    expect(confirmLine(1, "2026-11-05T16:00:00Z", "Asia/Kolkata")).toBe("Send to 1 person on Thu, Nov 5 at 9:30 PM GMT+5:30?");
  });

  it("names every status in words", () => {
    const z = "America/Boise", now = new Date("2026-11-02T18:00:00Z");
    expect(statusLabel({ status: "draft", scheduledAt: null, sentAt: null }, z, now)).toBe("Draft");
    expect(statusLabel({ status: "scheduled", scheduledAt: "2026-11-03T16:00:00Z", sentAt: null }, z, now)).toMatch(/^Scheduled for Tomorrow at 9:00 AM( MST)?$/);
    expect(statusLabel({ status: "scheduled", scheduledAt: "2026-11-03T16:00:00Z", sentAt: null }, "Asia/Kolkata", now)).toBe("Scheduled for Tomorrow at 9:30 PM GMT+5:30");
    // Send now, or a time already reached: the sender hasn't picked it up yet.
    expect(statusLabel({ status: "scheduled", scheduledAt: "2026-11-02T18:00:00Z", sentAt: null }, z, now)).toBe("Waiting to send");
    expect(statusLabel({ status: "sending", scheduledAt: null, sentAt: null }, z, now)).toBe("Sending");
    expect(statusLabel({ status: "sent", scheduledAt: null, sentAt: "2026-11-02T16:00:00Z" }, z, now)).toMatch(/^Sent Today at 9:00 AM( MST)?$/);
    expect(statusLabel({ status: "cancelled", scheduledAt: "2026-11-03T16:00:00Z", sentAt: null }, z, now)).toBe("Cancelled");
    expect(statusLabel({ status: "cancelled", scheduledAt: null, sentAt: null }, z, now)).toBe("Discarded");
  });
});
