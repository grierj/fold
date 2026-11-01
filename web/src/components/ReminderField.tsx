import { Checkbox, Field, Select } from "./ui/Field";

// The one reminder time for every event form (M3.6b, DEC-133 B): the group
// and church-wide create forms and Edit series share it. It is a series
// setting; a single date's edit doesn't change it. Beside it, the 2-hour
// push reminder switch (M3.13d3, PLAN 7.7), on by default, shown only
// while the church has notifications on devices on.
const REMINDER_CHOICES: { minutes: number; label: string }[] = [
  { minutes: 0, label: "Off" },
  { minutes: 120, label: "2 hours before" },
  { minutes: 1440, label: "1 day before" },
  { minutes: 2880, label: "2 days before" },
  { minutes: 10080, label: "1 week before" },
];

// A new event's reminder time (the server's default too).
export const DEFAULT_REMINDER_MINUTES = 1440;

// reminderSummary is the closed More options' words for it. push is the
// push reminder switch, or undefined when it isn't offered.
export function reminderSummary(minutes: number, push?: boolean): string {
  const choice = REMINDER_CHOICES.find((c) => c.minutes === minutes);
  const email = minutes === 0 ? (push ? "No reminder email" : "No reminder") : choice ? `Reminder ${choice.label}` : "Reminder";
  return push ? `${email} · Push reminder` : email;
}

// The push reminder switch, when the form offers it.
export interface ReminderPushChoice {
  value: boolean;
  onChange: (on: boolean) => void;
}

export function ReminderField({ idPrefix, value, onChange, push }: {
  idPrefix: string;
  value: number;
  onChange: (minutes: number) => void;
  push?: ReminderPushChoice;
}) {
  return (
    <>
      <Field id={`reminder-${idPrefix}`} label="Reminder email" hint="We'll email everyone going. People on the waitlist hear they're still waiting.">
        <Select
          value={value}
          options={REMINDER_CHOICES.map((c) => ({ value: String(c.minutes), label: c.label }))}
          onChange={(e) => onChange(Number(e.target.value))}
        />
      </Field>
      {push && (
        <Checkbox label="Push reminder 2 hours before"
          description="People who turned on notifications get a short reminder on their phone."
          checked={push.value} onChange={(e) => push.onChange(e.target.checked)} />
      )}
    </>
  );
}
