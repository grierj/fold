import { Link } from "react-router-dom";
import type { DateReminders } from "../types";
import { reminderLines } from "../lib/reminders";
import { useChurchZoneLoad } from "../lib/useEventZone";
import { browserTimeZone } from "../lib/when";

// The date page's reminder lines for its event's managers (M3.6b, DEC-133
// D): each reminder sent, with how many it reached, linking to its history;
// when the next goes; or that they're off. The API sends the field to
// managers only. The push reminder (M3.13d3) has its own lines.
export function DateReminderLines({ reminders }: { reminders: DateReminders }) {
  const church = useChurchZoneLoad();
  if (!church.done) return null;
  const lines = reminderLines(reminders, church.zone ?? browserTimeZone());
  return (
    <div className="space-y-1 border-t border-border pt-3">
      <h2 className="text-base font-semibold">Reminders</h2>
      <ul className="text-sm text-text-muted">
        {lines.map((l) => (
          <li key={l.key}>
            {l.messageId ? (
              <Link to={`/messages/${l.messageId}`} className="inline-flex min-h-11 items-center font-medium text-primary underline">
                {l.text}
              </Link>
            ) : (
              <p className="py-1">{l.text}</p>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
