import type { AudienceRole, AudienceStatus, AudienceSummary, ComposeOptions, Message, MessageAudience } from "../types";
import { formatShortDay, formatWhenIn } from "./when";

// The composer's words and audience shapes (M3.4c, PLAN 7.2 and 7.3).

// The picker's state. target is "group:<id>", "event:<id>", "occurrence:<id>"
// (a date whose event isn't listed), "all", or "" before a choice; dates is
// "all" for every upcoming date or an occurrence id.
export interface AudienceChoice {
  target: string;
  dates: string;
  statuses: AudienceStatus[];
  roles: AudienceRole[];
}

export const STATUS_CHOICES: { id: AudienceStatus; label: string }[] = [
  { id: "going", label: "Going" },
  { id: "waitlisted", label: "Waitlisted" },
  { id: "maybe", label: "Maybe" },
];
export const ROLE_CHOICES: { id: AudienceRole; label: string }[] = [
  { id: "member", label: "Members" },
  { id: "leader", label: "Leaders" },
];
const DEFAULT_STATUSES: AudienceStatus[] = ["going", "waitlisted"];
const DEFAULT_ROLES: AudienceRole[] = ["member", "leader"];

export const emptyChoice = (): AudienceChoice => ({ target: "", dates: "all", statuses: DEFAULT_STATUSES, roles: DEFAULT_ROLES });

// toAudience is the API shape of a choice, or null until it names someone.
export function toAudience(c: AudienceChoice): MessageAudience | null {
  const [kind, raw] = c.target.split(":");
  const id = Number(raw);
  if (c.target === "all") return { type: "all_members" };
  if (!id) return null;
  if (kind === "group") return c.roles.length ? { type: "group", groupId: id, roles: c.roles } : null;
  if (!c.statuses.length) return null;
  if (kind === "occurrence") return { type: "occurrence", occurrenceId: id, statuses: c.statuses };
  if (kind !== "event") return null;
  return c.dates === "all"
    ? { type: "event_series", eventId: id, statuses: c.statuses }
    : { type: "occurrence", occurrenceId: Number(c.dates), statuses: c.statuses };
}

// fromAudience is the picker's state for a saved audience. A date is shown
// under its event when the event is listed.
export function fromAudience(a: MessageAudience, options: ComposeOptions): AudienceChoice {
  const c: AudienceChoice = { ...emptyChoice(), statuses: a.statuses?.length ? a.statuses : DEFAULT_STATUSES, roles: a.roles?.length ? a.roles : DEFAULT_ROLES };
  switch (a.type) {
    case "all_members": return { ...c, target: "all" };
    case "group": return { ...c, target: `group:${a.groupId}` };
    case "event_series": return { ...c, target: `event:${a.eventId}` };
    case "occurrence": {
      const ev = eventOfDate(options, a.occurrenceId ?? 0);
      return ev ? { ...c, target: `event:${ev.id}`, dates: String(a.occurrenceId) } : { ...c, target: `occurrence:${a.occurrenceId}` };
    }
    default: return c;
  }
}

// prefill reads /messages/new?group=, ?event= or ?occurrence=.
export function prefillChoice(params: URLSearchParams, options: ComposeOptions): AudienceChoice {
  const group = Number(params.get("group")), event = Number(params.get("event")), occurrence = Number(params.get("occurrence"));
  if (group) return fromAudience({ type: "group", groupId: group }, options);
  if (event) return fromAudience({ type: "event_series", eventId: event }, options);
  if (occurrence) return fromAudience({ type: "occurrence", occurrenceId: occurrence }, options);
  return emptyChoice();
}

function eventOfDate(options: ComposeOptions, occurrenceId: number) {
  return options.events.find((e) => e.dates.some((d) => d.occurrenceId === occurrenceId));
}

// One event's people: event merge fields work (the server's
// message_merge_event rule).
export const isOneEvent = (a: MessageAudience | null) => a?.type === "occurrence" || a?.type === "event_series";

// audienceKey identifies an audience for comparing and for ignoring stale
// counts.
export const audienceKey = (a: MessageAudience | null) => (a ? JSON.stringify(a) : "");

const statusWords = (s: AudienceStatus[] | undefined) =>
  STATUS_CHOICES.filter((x) => (s ?? DEFAULT_STATUSES).includes(x.id)).map((x) => x.label).join(", ");
const roleWords = (r: AudienceRole[] | undefined) =>
  ROLE_CHOICES.filter((x) => (r ?? DEFAULT_ROLES).includes(x.id)).map((x) => x.label).join(", ");

// audienceLabel says who a message goes to in a line: "Room 201 class
// (Members, Leaders)", "Book night, all upcoming dates (Going, Waitlisted)".
export function audienceLabel(a: MessageAudience, options: ComposeOptions | null): string {
  switch (a.type) {
    case "all_members": return "Everyone at church";
    case "group": {
      const g = options?.groups.find((x) => x.id === a.groupId);
      return `${g ? g.name : "A group that's no longer listed"} (${roleWords(a.roles)})`;
    }
    case "event_series": {
      const ev = options?.events.find((x) => x.id === a.eventId);
      return `${ev ? ev.title : "An event that's no longer listed"}, all upcoming dates (${statusWords(a.statuses)})`;
    }
    case "occurrence": {
      const ev = options ? eventOfDate(options, a.occurrenceId ?? 0) : undefined;
      const d = ev?.dates.find((x) => x.occurrenceId === a.occurrenceId);
      return `${ev && d ? `${ev.title}, ${formatShortDay(d.startsAt)}` : "One date of an event"} (${statusWords(a.statuses)})`;
    }
    case "events": return `${a.eventIds?.length ?? 0} events (${statusWords(a.statuses)})`;
    case "users": return `${a.userIds?.length ?? 0} chosen people`;
  }
}

const people = (n: number) => `${n} ${n === 1 ? "person" : "people"}`;

// Reasons that need a verb to read after a count ("3 have no email
// address"). Any other reason the API sends ("turned off messages from
// leaders", "marked our email as spam") reads as it comes.
const REASONS: Record<string, [one: string, many: string]> = {
  "no email address": ["has no email address", "have no email address"],
  "email not working": ["has an email address that isn't working", "have email addresses that aren't working"],
  "not active": ["isn't active", "aren't active"],
  "guest, hasn't asked for updates": ["is a guest who hasn't asked for updates", "are guests who haven't asked for updates"],
};

// summaryLine is the live count: "142 people; 9 won't get it: 6 turned off
// messages from leaders, 3 have no email address".
export function summaryLine(s: AudienceSummary): string {
  if (s.people === 0) return "No one is on this list yet.";
  const skipped = s.people - s.eligible;
  if (skipped === 0) return `${people(s.people)}; everyone on the list will get it.`;
  const reasons = Object.entries(s.skipped)
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([reason, n]) => `${n} ${REASONS[reason]?.[n === 1 ? 0 : 1] ?? reason}`);
  return `${people(s.people)}; ${skipped} won't get it: ${reasons.join(", ")}`;
}

// teenLine counts the teens who will get it (DEC-20), or "" for none.
export function teenLine(s: AudienceSummary): string {
  if (s.teens === 0) return "";
  return s.teens === 1 ? "1 of them is a teen." : `${s.teens} of them are teens.`;
}

// Times a message goes out read on the church's clock (the schedule is
// picked in it), with its zone named when the viewer's clock differs.

// confirmLine is the question before sending: "Send to 133 people now?"
export function confirmLine(eligible: number, at: string | null, zone: string): string {
  if (!at) return `Send to ${people(eligible)} now?`;
  const when = formatWhenIn(at, zone);
  return `Send to ${people(eligible)} ${/^(Today|Tomorrow) /.test(when) ? when[0].toLowerCase() + when.slice(1) : `on ${when}`}?`;
}

// statusLabel names a message's state in words, never by color alone. A
// scheduled time already reached is "Waiting to send" until the sender
// (M3.5) picks it up; a draft that was never scheduled and then cancelled
// was discarded.
export function statusLabel(m: Pick<Message, "status" | "scheduledAt" | "sentAt">, zone: string, now = new Date()): string {
  switch (m.status) {
    case "draft": return "Draft";
    case "scheduled":
      if (!m.scheduledAt || new Date(m.scheduledAt).getTime() <= now.getTime()) return "Waiting to send";
      return `Scheduled for ${formatWhenIn(m.scheduledAt, zone, now)}`;
    case "sending": return "Sending";
    case "sent": return m.sentAt ? `Sent ${formatWhenIn(m.sentAt, zone, now)}` : "Sent";
    case "cancelled": return m.scheduledAt ? "Cancelled" : "Discarded";
  }
}

// The merge fields the editor's Insert field menu offers (PLAN 7.3). Event
// fields need a message to one event's people.
export const MERGE_FIELDS: { field: string; label: string; event: boolean }[] = [
  { field: "first_name", label: "First name", event: false },
  { field: "event_title", label: "Event title", event: true },
  { field: "event_time", label: "Event date and time", event: true },
  { field: "event_link", label: "Event page link", event: true },
  { field: "manage_link", label: "Link to their sign-up", event: true },
];
