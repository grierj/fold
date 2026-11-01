import type { AudienceSummary, ComposeDate, ComposeEvent, ComposeGroup, ComposeOptions, Message, MessageRecipients } from "../types";

// Composer fixtures (M3.4c): a leader's group and one event with two dates.
export const bookClub: ComposeGroup = { id: 7, name: "Book club" };
export const dates: ComposeDate[] = [
  { occurrenceId: 31, date: "2026-11-05", startsAt: "2026-11-06T02:00:00Z" },
  { occurrenceId: 32, date: "2026-11-12", startsAt: "2026-11-13T02:00:00Z" },
];
export const bookNight: ComposeEvent = { id: 3, title: "Book night", groupName: "Book club", churchWide: false, dates };

export function composeOptions(over: Partial<ComposeOptions> = {}): ComposeOptions {
  return { canCompose: true, canMessageEveryone: false, smsEnabled: false, pushEnabled: true, groups: [bookClub], events: [bookNight], ...over };
}

export function summary(over: Partial<AudienceSummary> = {}): AudienceSummary {
  return {
    people: 3, eligible: 2, teens: 1, push: 0, skipped: { "turned off messages from leaders": 1 },
    recipients: [{ name: "Ann Adult", teen: false }, { name: "Opal Opted", teen: false, skipped: "won't get this" }, { name: "Tia Teen", teen: true }],
    more: false, ...over,
  };
}

export function message(over: Partial<Message> = {}): Message {
  return {
    id: 5, channel: "email", status: "draft", subject: "Bring a friend", bodyHtml: "<p>Hi {{first_name}}</p>",
    audience: { type: "group", groupId: 7, roles: ["member", "leader"] }, scheduledAt: null, sentAt: null,
    authorName: "Lee Leader", editable: true, updatedAt: "2026-10-31T15:00:00Z", error: "", failedCount: 0, category: "announcement", displaySubject: "", pushAlert: true, ...over,
  };
}

// A sent message's history (M3.5b) as a leader gets it: three reached, a
// teen among them, one skipped, and a guardian's copy counted, not named
// (DEC-132). adminRecipients is the same list as an admin gets it.
export function recipients(over: Partial<MessageRecipients> = {}): MessageRecipients {
  return {
    counts: { sent: 2, delivered: 0, bounced: 0, complained: 0, failed: 0, skipped: 1, queued: 0, teens: 1, guardianCopies: 1, guardianCopiesMissed: 0, guardianCopiesFailed: 0, push: { sent: 0, waiting: [], missed: {} } },
    skippedBy: { "turned off messages from leaders": 1 },
    recipients: [
      { name: "Ann Adult", teen: false, status: "sent", channel: "email" },
      { name: "Opal Opted", teen: false, status: "missed", channel: "email" },
      { name: "Tia Teen", teen: true, status: "sent", channel: "email" },
    ],
    more: false, canRetry: false, ...over,
  };
}

export function adminRecipients(over: Partial<MessageRecipients> = {}): MessageRecipients {
  return recipients({
    recipients: [
      { name: "Ann Adult", teen: false, status: "sent", channel: "email" },
      { name: "Opal Opted", teen: false, status: "missed", reason: "turned off messages from leaders", channel: "email" },
      { name: "Pat Parent", teen: false, status: "sent", copyOf: "Tia Teen", channel: "email" },
      { name: "Tia Teen", teen: true, status: "sent", channel: "email" },
    ],
    ...over,
  });
}

// An automatic reminder for Book night's first date (M3.6b): no author,
// never editable, its subject and body holding merge fields as written.
export function reminderMessage(over: Partial<Message> = {}): Message {
  return message({
    id: 9, status: "sent", subject: "Reminder: {{event_title}} on {{event_time}}", displaySubject: "Reminder: Book night on Thu, Nov 5 · 7:00 PM MST", bodyHtml: "<p>Hi {{first_name}},</p>",
    audience: { type: "occurrence", occurrenceId: 31, statuses: ["going"] }, sentAt: "2026-11-05T02:00:00Z",
    authorName: "", editable: false, category: "reminder",
    reminder: { kind: "going", occurrenceId: 31, eventId: 3, eventTitle: "Book night", date: "2026-11-05", startsAt: "2026-11-06T02:00:00Z", tzid: "America/Boise" },
    ...over,
  });
}
