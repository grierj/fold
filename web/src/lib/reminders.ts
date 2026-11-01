import type { DateReminders, Message, MessageReminder, ReminderKind } from "../types";
import { browserTimeZone, formatShortWeekdayDay, formatWhenIn, lowerLead } from "./when";

// The words for automatic event reminders (M3.6b, DEC-133 D): the lines
// on the date page its managers see, and how a reminder reads in Messages.

const people = (n: number) => `${n} ${n === 1 ? "person" : "people"}`;

const NOUN: Record<ReminderKind, string> = { going: "Reminder", waitlist: "Waitlist note", push: "Push reminder" };

// One line on the date page; messageId links it to the reminder's page.
export interface ReminderLine {
  key: string;
  text: string;
  messageId?: number;
}

// pushText is a push reminder's line (M3.13d3), its push rows counted:
// "Reminder pushed to 5 people · today at 5:00 PM · 1 skipped", or while
// none has gone yet, "Push reminder waiting to go to 5 people".
function pushText(m: DateReminders["messages"][number], zone: string, now: Date, viewer: string): string {
  const waiting = m.waiting ?? 0;
  const parts: string[] = [];
  if (m.reached > 0) {
    parts.push(`Reminder pushed to ${people(m.reached)}`);
    if (m.sentAt) parts.push(lowerLead(formatWhenIn(m.sentAt, zone, now, viewer, true)));
    if (waiting > 0) parts.push(`${waiting} waiting`);
  } else {
    parts.push(waiting > 0 ? `Push reminder waiting to go to ${people(waiting)}` : "Push reminder reached no one");
  }
  if (m.failed > 0) parts.push(`${m.failed} didn't get it`);
  if (m.skipped > 0) parts.push(`${m.skipped} skipped`);
  return parts.join(" · ");
}

// reminderLines says, one line each: every reminder written for the date
// ("Reminder emailed to 12 people · yesterday at 7:00 PM · 1 didn't get it
// · 2 skipped", "Waitlist note emailed to 3 people · …", "Reminder pushed
// to 5 people · …"), then when the next one goes ("Reminder goes out Tue,
// Nov 3 at 7:00 PM") unless one already sent covers it, or that reminders
// are off, and the same for the push reminder when the church has push on
// (r.push isn't null). Times read on the church's clock, as other message
// times do.
export function reminderLines(r: DateReminders, zone: string, now = new Date(), viewer = browserTimeZone()): ReminderLine[] {
  const out: ReminderLine[] = [];
  let emails = 0;
  for (const m of r.messages) {
    const noun = NOUN[m.kind];
    let text: string;
    if (m.kind === "push") {
      out.push({ key: `m${m.messageId}`, text: pushText(m, zone, now, viewer), messageId: m.messageId });
      continue;
    }
    switch (m.status) {
      case "sent": {
        const parts = [m.reached > 0 ? `${noun} emailed to ${people(m.reached)}` : `${noun} reached no one`];
        if (m.sentAt) parts.push(lowerLead(formatWhenIn(m.sentAt, zone, now, viewer, true)));
        if (m.failed > 0) parts.push(`${m.failed} didn't get it`);
        if (m.skipped > 0) parts.push(`${m.skipped} skipped`);
        text = parts.join(" · ");
        break;
      }
      case "failed": text = `${noun} didn't go out`; break;
      case "cancelled": continue;
      default: text = `${noun} is going out now`;
    }
    emails++;
    out.push({ key: `m${m.messageId}`, text, messageId: m.messageId });
  }
  const push = r.push ?? null;
  if (r.minutes === 0) {
    out.push({ key: "off", text: push ? "Reminder emails are off for this event" : "Reminders are off for this event" });
  } else if (r.dueAt && !r.covered && new Date(r.dueAt).getTime() > now.getTime()) {
    out.push({ key: "due", text: `Reminder goes out ${lowerLead(formatWhenIn(r.dueAt, zone, now, viewer))}` });
  } else if (emails === 0) {
    out.push({ key: "none", text: "No reminder went out for this date" });
  }
  if (push === false && r.minutes !== 0) {
    out.push({ key: "push-off", text: "Push reminders are off for this event" });
  } else if (push && r.pushDueAt && new Date(r.pushDueAt).getTime() > now.getTime()) {
    out.push({ key: "push-due", text: `Push reminder goes out ${lowerLead(formatWhenIn(r.pushDueAt, zone, now, viewer))}` });
  }
  return out;
}

// What a reminder is called: "Automatic reminder", "Automatic waitlist
// note", "Automatic push reminder".
export const reminderName = (r: Pick<MessageReminder, "kind"> | undefined) =>
  r?.kind === "waitlist" ? "Automatic waitlist note" : r?.kind === "push" ? "Automatic push reminder" : "Automatic reminder";

// reminderRowTitle is a reminder's line in Messages: "Automatic reminder ·
// Book night, Tue, Nov 3". The date reads in the event's zone, as its
// emails and its heading (displaySubject) do.
export function reminderRowTitle(m: Pick<Message, "reminder">, now = new Date()): string {
  const r = m.reminder;
  if (!r) return reminderName(r);
  let day: string;
  try {
    day = formatShortWeekdayDay(r.startsAt, r.tzid || browserTimeZone(), now);
  } catch {
    // A zone the browser doesn't know: its own clock, as formatWhenIn falls back.
    day = formatShortWeekdayDay(r.startsAt, browserTimeZone(), now);
  }
  return `${reminderName(r)} · ${r.eventTitle}, ${day}`;
}

// reminderDateText is the date it's for, in a sentence: "Book night, Tue,
// Nov 3 at 7:00 PM", in the event's zone, named when the viewer's differs.
export function reminderDateText(r: MessageReminder, now = new Date(), viewer = browserTimeZone()): string {
  return `${r.eventTitle}, ${lowerLead(formatWhenIn(r.startsAt, r.tzid, now, viewer, true))}`;
}

// Who a reminder goes to, for its To line.
export const reminderAudience = (r: Pick<MessageReminder, "kind"> | undefined) =>
  r?.kind === "waitlist" ? "Everyone on the waitlist" : r?.kind === "push" ? "Everyone going who turned on notifications" : "Everyone going";
