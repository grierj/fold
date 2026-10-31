import { Field, Select } from "./ui/Field";

// The one reminder time for every event form (M3.6b, DEC-133 B): the group
// and church-wide create forms and Edit series share it. It is a series
// setting; a single date's edit doesn't change it.
const REMINDER_CHOICES: { minutes: number; label: string }[] = [
  { minutes: 0, label: "Off" },
  { minutes: 120, label: "2 hours before" },
  { minutes: 1440, label: "1 day before" },
  { minutes: 2880, label: "2 days before" },
  { minutes: 10080, label: "1 week before" },
];

// A new event's reminder time (the server's default too).
export const DEFAULT_REMINDER_MINUTES = 1440;

// reminderSummary is the closed More options' words for it.
export function reminderSummary(minutes: number): string {
  if (minutes === 0) return "No reminder";
  const choice = REMINDER_CHOICES.find((c) => c.minutes === minutes);
  return choice ? `Reminder ${choice.label}` : "Reminder";
}

export function ReminderField({ idPrefix, value, onChange }: { idPrefix: string; value: number; onChange: (minutes: number) => void }) {
  return (
    <Field id={`reminder-${idPrefix}`} label="Reminder email" hint="We'll email everyone going. People on the waitlist hear they're still waiting.">
      <Select
        value={value}
        options={REMINDER_CHOICES.map((c) => ({ value: String(c.minutes), label: c.label }))}
        onChange={(e) => onChange(Number(e.target.value))}
      />
    </Field>
  );
}
