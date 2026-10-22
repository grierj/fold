import { Field, Input } from "./ui/Field";

export function childcareCountProblem(count: string): string | null {
  return count.trim() === "" || (/^\d+$/.test(count.trim()) && Number(count) <= 20)
    ? null : "Enter a whole number from 0 to 20 children.";
}

export function ChildcareRequestField({ count, ages, onCount, onAges, error, disabled }: {
  count: string;
  ages: string;
  onCount: (count: string) => void;
  onAges: (ages: string) => void;
  error?: string | null;
  disabled?: boolean;
}) {
  return (
    <div className="space-y-3">
      <Field label="Children needing childcare" optional hint="Enter a whole number from 0 to 20. Leave it blank for none." error={error}>
        <Input type="text" inputMode="numeric" value={count} onChange={(e) => onCount(e.target.value)} disabled={disabled} />
      </Field>
      <Field label="Ages" optional hint="The children's ages. Up to 200 characters. Only you and the event's leaders, hosts and admins can see this.">
        <Input value={ages} maxLength={200} onChange={(e) => onAges(e.target.value)} disabled={disabled} />
      </Field>
    </div>
  );
}
