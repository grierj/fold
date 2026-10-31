import { describe, expect, it } from "vitest";
import type { DateReminder, DateReminders } from "../types";
import { reminderAudience, reminderDateText, reminderLines, reminderName, reminderRowTitle } from "./reminders";

// Wednesday Nov 4 2026, 11:00 AM in Boise (MST, UTC-7).
const now = new Date("2026-11-04T18:00:00Z");
const zone = "America/Boise";
const sent = (over: Partial<DateReminder> = {}): DateReminder => ({
  messageId: 41, kind: "going", status: "sent", sentAt: "2026-11-04T02:00:00Z", reached: 12, failed: 0, ...over,
});
const lines = (r: Partial<DateReminders>, viewer = zone) =>
  reminderLines({ minutes: 1440, dueAt: null, messages: [], ...r }, zone, now, viewer).map((l) => l.text);

describe("reminderLines", () => {
  it("says who each sent reminder reached and when, on the church's clock, linking to it", () => {
    const out = reminderLines({ minutes: 1440, dueAt: "2026-11-04T02:00:00Z", messages: [sent(), sent({ messageId: 42, kind: "waitlist", reached: 3 })] }, zone, now, zone);
    expect(out.map((l) => l.text)).toEqual([
      "Reminder emailed to 12 people · yesterday at 7:00 PM",
      "Waitlist note emailed to 3 people · yesterday at 7:00 PM",
    ]);
    expect(out.map((l) => l.messageId)).toEqual([41, 42]);
    expect(lines({ messages: [sent({ reached: 1, failed: 2, sentAt: "2026-11-01T01:00:00Z" })] })).toEqual(["Reminder emailed to 1 person · Sat, Oct 31 at 7:00 PM · 2 didn't get it"]);
    // Another clock than the viewer's is named.
    expect(lines({ messages: [sent()] }, "America/Los_Angeles")).toEqual(["Reminder emailed to 12 people · yesterday at 7:00 PM MST"]);
  });

  it("says when the next one goes before it does", () => {
    expect(lines({ dueAt: "2026-11-11T02:00:00Z" })).toEqual(["Reminder goes out Tue, Nov 10 at 7:00 PM"]);
    expect(lines({ dueAt: "2026-11-05T02:00:00Z" })).toEqual(["Reminder goes out today at 7:00 PM"]);
    expect(lines({ dueAt: "2026-11-06T02:00:00Z" })).toEqual(["Reminder goes out tomorrow at 7:00 PM"]);
  });

  it("says reminders are off, with any that went before", () => {
    expect(lines({ minutes: 0 })).toEqual(["Reminders are off for this event"]);
    expect(lines({ minutes: 0, messages: [sent()] })).toEqual(["Reminder emailed to 12 people · yesterday at 7:00 PM", "Reminders are off for this event"]);
  });

  it("puts a failed or sending reminder in words, and says when none went out", () => {
    expect(lines({ messages: [sent({ status: "failed", sentAt: null, reached: 0, failed: 12 })] })).toEqual(["Reminder didn't go out"]);
    expect(lines({ messages: [sent({ status: "sending", sentAt: null })] })).toEqual(["Reminder is going out now"]);
    expect(lines({ dueAt: "2026-11-03T02:00:00Z" })).toEqual(["No reminder went out for this date"]);
  });
});

describe("a reminder in Messages", () => {
  const r = { kind: "going" as const, occurrenceId: 31, eventId: 3, eventTitle: "Book night", date: "2026-11-03", startsAt: "2026-11-04T02:00:00Z" };
  it("is named by its date, not its subject", () => {
    expect(reminderRowTitle({ reminder: r }, zone, now)).toBe("Automatic reminder · Book night, Tue, Nov 3");
    expect(reminderRowTitle({ reminder: { ...r, kind: "waitlist" } }, zone, now)).toBe("Automatic waitlist note · Book night, Tue, Nov 3");
    expect(reminderRowTitle({ reminder: undefined }, zone, now)).toBe("Automatic reminder");
    expect(reminderName(r)).toBe("Automatic reminder");
    expect(reminderDateText({ ...r, startsAt: "2026-11-11T02:00:00Z" }, zone, now)).toBe("Book night, Tue, Nov 10 at 7:00 PM");
    expect(reminderDateText(r, zone, now)).toBe("Book night, yesterday at 7:00 PM");
    expect(reminderAudience(r)).toBe("Everyone going");
    expect(reminderAudience({ kind: "waitlist" })).toBe("Everyone on the waitlist");
  });
});
