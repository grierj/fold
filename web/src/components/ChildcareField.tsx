import { errorCode } from "../lib/errors";
import { Checkbox, Field, Input, Textarea } from "./ui/Field";

export interface ChildcareValue {
  available: boolean;
  capacity: string;
  ages: string;
  notes: string;
}

export const emptyChildcare: ChildcareValue = { available: false, capacity: "", ages: "", notes: "" };

export function childcareFrom(available = false, capacity?: number | null, ages = "", notes = ""): ChildcareValue {
  return { available, capacity: capacity == null ? "" : String(capacity), ages, notes };
}

export function childcareProblem(value: ChildcareValue): string | null {
  if (!value.available || value.capacity.trim() === "") return null;
  const cap = value.capacity.trim();
  if (!/^\d+$/.test(cap) || !Number.isSafeInteger(Number(cap)) || Number(cap) > 100000) {
    return "The childcare limit needs to be a whole number from 1 to 100,000, or leave it empty.";
  }
  return Number(cap) === 0 ? "Uncheck childcare instead of a 0 childcare limit." : null;
}

export function childcareBody(value: ChildcareValue) {
  return {
    childcareAvailable: value.available,
    childcareCapacity: !value.available || value.capacity.trim() === "" ? null : Number(value.capacity),
    childcareAges: value.ages.trim(),
    childcareNotes: value.notes.trim(),
  };
}

export function focusChildcareCapacity(prefix: string) {
  setTimeout(() => document.getElementById(`childcare-capacity-${prefix}`)?.focus(), 0);
}

export const isChildcareCapacityError = (err: unknown) => {
  const code = errorCode(err);
  return code === "invalid_childcare_capacity" || code === "childcare_capacity_zero";
};

export function ChildcareField({ idPrefix, value, onChange, error }: {
  idPrefix: string;
  value: ChildcareValue;
  onChange: (value: ChildcareValue) => void;
  error?: string | null;
}) {
  return (
    <div className="space-y-3">
      <Checkbox label="Childcare available" checked={value.available}
        description="Parents can ask for childcare when they register. Children are counted separately from seats."
        onChange={(e) => onChange({ ...value, available: e.target.checked })} />
      {value.available && <>
        <Field id={`childcare-capacity-${idPrefix}`} label="Childcare limit" optional
          hint="Children per date. Enter a whole number from 1 to 100,000. Leave it blank for no limit." error={error}>
          <Input type="text" inputMode="numeric" placeholder="No limit" value={value.capacity}
            onChange={(e) => onChange({ ...value, capacity: e.target.value })} />
        </Field>
        <Field id={`childcare-ages-${idPrefix}`} label="Ages you can care for" optional hint="For example: 2 to 10. Up to 100 characters.">
          <Input value={value.ages} maxLength={100} onChange={(e) => onChange({ ...value, ages: e.target.value })} />
        </Field>
        <Field id={`childcare-notes-${idPrefix}`} label="Notes for parents" optional hint="Up to 500 characters. These notes are public to anyone who can see the event.">
          <Textarea value={value.notes} maxLength={500} rows={3} onChange={(e) => onChange({ ...value, notes: e.target.value })} />
        </Field>
      </>}
    </div>
  );
}
