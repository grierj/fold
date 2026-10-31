import type { DateReminders, Message, MessageReminder, ReminderKind } from "../types";
import { browserTimeZone, formatShortWeekdayDay, formatWhenIn, lowerLead } from "./when";

// The words for automatic event reminders (M3.6b, DEC-133 D): the lines
// on the date page its managers see, and how a reminder reads in Messages.

const people = (n: number) => `${n} ${n === 1 ? "person" : "people"}`;

const NOUN: Record<ReminderKind, string> = { going: "Reminder", waitlist: "Waitlist note" };

// One line on the date page; messageId links it to the reminder's page.
export interface ReminderLine {
  key: string;
  text: string;
  messageId?: number;
}

// reminderLines says, one line each: every reminder written for the date
// ("Reminder emailed to 12 people · yesterday at 7:00 PM · 1 didn't get it
// · 2 skipped", "Waitlist note emailed to 3 people · …"), then when the
// next one goes ("Reminder goes out Tue, Nov 3 at 7:00 PM") unless one
// already sent covers it, or that reminders are off. Times read on the
// church's clock, as other message times do.
export function reminderLines(r: DateReminders, zone: string, now = new Date(), viewer = browserTimeZone()): ReminderLine[] {
  const out: ReminderLine[] = [];
  for (const m of r.messages) {
    const noun = NOUN[m.kind];
    let text: string;
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
    out.push({ key: `m${m.messageId}`, text, messageId: m.messageId });
  }
  if (r.minutes === 0) {
    out.push({ key: "off", text: "Reminders are off for this event" });
  } else if (r.dueAt && !r.covered && new Date(r.dueAt).getTime() > now.getTime()) {
    out.push({ key: "due", text: `Reminder goes out ${lowerLead(formatWhenIn(r.dueAt, zone, now, viewer))}` });
  } else if (out.length === 0) {
    out.push({ key: "none", text: "No reminder went out for this date" });
  }
  return out;
}

// What a reminder is called: "Automatic reminder", "Automatic waitlist note".
export const reminderName = (r: Pick<MessageReminder, "kind"> | undefined) =>
  r?.kind === "waitlist" ? "Automatic waitlist note" : "Automatic reminder";

// reminderRowTitle is a reminder's line in Messages: "Automatic reminder ·
// Book night, Tue, Nov 3". The date reads in the event's zone, as its
// emails and its heading (displaySubject) do.
export function reminderRowTitle(m: Pick<Message, "reminder">, now = new Date()): string {
  const r = m.reminder;
  if (!r) return reminderName(r);
  return `${reminderName(r)} · ${r.eventTitle}, ${formatShortWeekdayDay(r.startsAt, r.tzid, now)}`;
}

// reminderDateText is the date it's for, in a sentence: "Book night, Tue,
// Nov 3 at 7:00 PM", in the event's zone, named when the viewer's differs.
export function reminderDateText(r: MessageReminder, now = new Date(), viewer = browserTimeZone()): string {
  return `${r.eventTitle}, ${lowerLead(formatWhenIn(r.startsAt, r.tzid, now, viewer, true))}`;
}

// Who a reminder goes to, for its To line.
export const reminderAudience = (r: Pick<MessageReminder, "kind"> | undefined) =>
  r?.kind === "waitlist" ? "Everyone on the waitlist" : "Everyone going";
