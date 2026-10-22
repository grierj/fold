import { Choices, Field, Input } from "./ui/Field";

export function childcareCountProblem(count: string): string | null {
  return count.trim() === "" || (/^\d+$/.test(count.trim()) && Number(count) <= 20)
    ? null : "Enter a whole number from 0 to 20 children.";
}

export function ChildcareRequestField({ count, ages, onCount, onAges, error, disabled, full, named = false }: {
  named?: boolean;
  count: string;
  ages: string;
  onCount: (count: string) => void;
  onAges: (ages: string) => void;
  error?: string | null;
  disabled?: boolean;
  full?: boolean;
}) {
  return (
    <Choices legend="Childcare for this date">
      <div className="space-y-3">
        <Field label={named ? "Other children needing childcare" : "Children needing childcare"} optional
          hint={full ? "Childcare is full for this date" : named ? "Enter 0 to 20. Do not include children selected by name above." : "Enter a whole number from 0 to 20."} error={error}>
          <Input type="text" inputMode="numeric" value={count} onChange={(e) => onCount(e.target.value)} disabled={disabled || full} />
        </Field>
        <Field label="Ages" optional hint="For example: 3 and 7. Only you and the event's leaders, hosts and admins can see this.">
          <Input value={ages} maxLength={200} onChange={(e) => onAges(e.target.value)} disabled={disabled} />
        </Field>
      </div>
    </Choices>
  );
}
