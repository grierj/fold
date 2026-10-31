import type { AudienceSummary, ComposeDate, ComposeEvent, ComposeGroup, ComposeOptions, Message, MessageRecipients } from "../types";

// Composer fixtures (M3.4c): a leader's group and one event with two dates.
export const bookClub: ComposeGroup = { id: 7, name: "Book club" };
export const dates: ComposeDate[] = [
  { occurrenceId: 31, date: "2026-11-05", startsAt: "2026-11-06T02:00:00Z" },
  { occurrenceId: 32, date: "2026-11-12", startsAt: "2026-11-13T02:00:00Z" },
];
export const bookNight: ComposeEvent = { id: 3, title: "Book night", groupName: "Book club", churchWide: false, dates };

export function composeOptions(over: Partial<ComposeOptions> = {}): ComposeOptions {
  return { canCompose: true, canMessageEveryone: false, smsEnabled: false, groups: [bookClub], events: [bookNight], ...over };
}

export function summary(over: Partial<AudienceSummary> = {}): AudienceSummary {
  return {
    people: 3, eligible: 2, teens: 1, skipped: { "turned off messages from leaders": 1 },
    recipients: [{ name: "Ann Adult", teen: false }, { name: "Opal Opted", teen: false, skipped: "won't get this" }, { name: "Tia Teen", teen: true }],
    more: false, ...over,
  };
}

export function message(over: Partial<Message> = {}): Message {
  return {
    id: 5, channel: "email", status: "draft", subject: "Bring a friend", bodyHtml: "<p>Hi {{first_name}}</p>",
    audience: { type: "group", groupId: 7, roles: ["member", "leader"] }, scheduledAt: null, sentAt: null,
    authorName: "Lee Leader", editable: true, updatedAt: "2026-10-31T15:00:00Z", error: "", failedCount: 0, ...over,
  };
}

// A sent message's history (M3.5b): two reached, a teen among them, a
// guardian's copy, one skipped.
export function recipients(over: Partial<MessageRecipients> = {}): MessageRecipients {
  return {
    counts: { sent: 3, delivered: 0, bounced: 0, complained: 0, failed: 0, skipped: 1, queued: 0, teens: 1 },
    skippedBy: { "turned off messages from leaders": 1 },
    recipients: [
      { name: "Ann Adult", teen: false, status: "sent" },
      { name: "Opal Opted", teen: false, status: "missed" },
      { name: "Pat Parent", teen: false, status: "sent", copyOf: "Tia Teen" },
      { name: "Tia Teen", teen: true, status: "sent" },
    ],
    more: false, canRetry: false, ...over,
  };
}
