import { describe, expect, it } from "vitest";
import { composeOptions, recipients, summary } from "../test/compose";
import { audienceLabel, confirmLine, emptyChoice, failedLine, fromAudience, guardianLine, historyLine, historyTeenLine, isOneEvent, prefillChoice, pushBadge, pushHistoryLine, pushLine, retryQuestion, statusLabel, summaryLine, teenLine, toAudience } from "./messages";

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

  it("never says a push reminder was sent before anything went (M3.13d3)", () => {
    const z = "America/Boise", now = new Date("2026-11-02T18:00:00Z");
    const push = { status: "sent" as const, channel: "push" as const, scheduledAt: "2026-11-02T18:00:00Z" };
    // By its counts, not the clock: waiting while some rows are still to go.
    expect(statusLabel({ ...push, sentAt: null, pushWaiting: 3 }, z, now)).toBe("Waiting to go out");
    expect(statusLabel({ ...push, sentAt: "2026-11-02T18:00:00Z", pushWaiting: 1 }, z, now)).toMatch(/^Pushed Today at 11:00 AM( MST)?$/);
    // Nothing left waiting and nothing pushed (every row skipped), even
    // before its date.
    expect(statusLabel({ ...push, sentAt: null, pushWaiting: 0 }, z, now)).toBe("Nobody got it");
    expect(statusLabel({ ...push, sentAt: null }, z, now)).toBe("Nobody got it");
  });
});

describe("history", () => {
  const sent = { status: "sent" as const, sentAt: "2026-11-02T16:00:00Z" }, failed = { status: "failed" as const, sentAt: null };
  const sending = { status: "sending" as const, sentAt: null };
  const counts = (over: Partial<ReturnType<typeof recipients>["counts"]>) => ({ ...recipients().counts, ...over });

  it("words a sent message's counts", () => {
    const h = recipients({
      counts: counts({ sent: 120, delivered: 10, complained: 2, bounced: 3, skipped: 9 }),
      skippedBy: { "turned off messages from leaders": 6, "no email address": 3 },
    });
    expect(historyLine(h, sent)).toBe("Sent to 132 people · 3 bounced · 9 skipped (6 turned off messages from leaders, 3 have no email address)");
    expect(historyLine(recipients({ counts: counts({ sent: 1, skipped: 0 }) }), sent)).toBe("Sent to 1 person");
    expect(historyLine(recipients({ counts: counts({ sent: 128, failed: 4, skipped: 0 }) }), sent)).toBe("Sent to 128 people · 4 didn't get it");
    expect(historyLine(recipients({ counts: counts({ sent: 0, failed: 4, skipped: 0 }) }), failed)).toBe("No emails went out · 4 didn't get it");
    // Everyone skipped for an email reason; their pushes may still have gone (DEC-122).
    expect(historyLine(recipients({ counts: counts({ sent: 0, skipped: 2 }), skippedBy: { "turned off messages from leaders": 2 } }), sent)).toBe("No emails went out · 2 skipped (2 turned off messages from leaders)");
    expect(historyLine(recipients({ counts: counts({ sent: 0, skipped: 0 }) }), sent)).toBe("No one was on the list.");
  });

  it("counts the way while it sends", () => {
    expect(historyLine(recipients({ counts: counts({ sent: 38, failed: 2, queued: 92, skipped: 9 }) }), sending)).toBe("Sending… 40 of 132 so far");
    expect(historyLine(recipients({ counts: counts({ sent: 128, queued: 4 }) }), { status: "sending", sentAt: "2026-11-02T16:00:00Z" })).toBe("Sending again… 128 of 132 so far");
  });

  it("asks before trying again", () => {
    expect(retryQuestion(4)).toBe("Send again to the 4 people it didn't reach?");
    expect(retryQuestion(1)).toBe("Send again to the 1 person it didn't reach?");
    expect(retryQuestion(0)).toBe("Try sending this message again?");
    expect(retryQuestion(1, 1)).toBe("Send again to the 1 person and 1 guardian copy it didn't reach?");
    expect(retryQuestion(0, 2)).toBe("Send the 2 guardian copies that didn't go out again?");
    // The banner says the same.
    expect(failedLine(1, 1)).toBe("1 person didn't get this");
    expect(failedLine(3, 0)).toBe("3 people didn't get this");
    expect(failedLine(0, 1)).toBe("1 guardian copy didn't go out");
    expect(historyTeenLine(0)).toBe("");
    expect(historyTeenLine(2)).toBe("2 of the people it was for are teens.");
  });

  it("counts guardian copies without naming anyone (DEC-132)", () => {
    expect(guardianLine({ guardianCopies: 0, guardianCopiesMissed: 0 })).toBe("");
    expect(guardianLine({ guardianCopies: 2, guardianCopiesMissed: 0 })).toBe("2 guardian copies sent");
    expect(guardianLine({ guardianCopies: 3, guardianCopiesMissed: 1 })).toBe("2 guardian copies sent · 1 didn't go out");
    expect(guardianLine({ guardianCopies: 1, guardianCopiesMissed: 1 })).toBe("1 guardian copy didn't go out");
  });

  it("names a failed and a partly sent message", () => {
    const z = "America/Boise", now = new Date("2026-11-02T18:00:00Z");
    expect(statusLabel({ status: "failed", scheduledAt: null, sentAt: null }, z, now)).toBe("Failed");
    expect(statusLabel({ status: "sent", scheduledAt: null, sentAt: "2026-11-02T16:00:00Z", failedCount: 3 }, z, now)).toMatch(/^Partly sent Today at 9:00 AM( MST)?$/);
    expect(statusLabel({ status: "sent", scheduledAt: null, sentAt: null, failedCount: 0 }, z, now)).toBe("Sent");
  });
});

// "Push notification too" (M3.13d2): who also gets one, and a history's
// pushes counted apart from its email.
describe("push words", () => {
  it("counts who also gets a push, and says nothing for none", () => {
    expect(pushLine(summary({ push: 38 }))).toBe("38 also get a push notification.");
    expect(pushLine(summary({ push: 1 }))).toBe("1 also gets a push notification.");
    expect(pushLine(summary({ push: 0 }))).toBe("");
  });

  it("words a history's pushes: sent, waiting by when, and missed by reason", () => {
    expect(pushHistoryLine({ sent: 38, waiting: [{ until: "8:00 AM", n: 2 }], missed: { "device gone": 1 } }))
      .toBe("Push notifications: 38 pushed · 2 waiting until 8:00 AM · 1 device gone");
    expect(pushHistoryLine({ sent: 0, waiting: [{ until: "", n: 3 }], missed: { "hourly limit": 2, "device gone": 2 } }))
      .toBe("Push notifications: 3 waiting · 2 devices gone · 2 over the hourly limit");
    expect(pushHistoryLine({ sent: 0, waiting: [], missed: {} })).toBe("");
    expect(pushHistoryLine(undefined)).toBe("");
  });

  it("words one person's push on their history row", () => {
    expect(pushBadge({ status: "sent" })).toBe("Push sent");
    expect(pushBadge({ status: "waiting", until: "8:00 AM" })).toBe("Push waiting until 8:00 AM");
    expect(pushBadge({ status: "waiting" })).toBe("Push waiting");
    expect(pushBadge({ status: "missed" })).toBe("Push didn't go out");
    expect(pushBadge({ status: "missed", reason: "push turned off by the church" })).toBe("Push didn't go out: notifications turned off by the church");
  });
});
