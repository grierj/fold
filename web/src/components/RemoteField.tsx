import { Checkbox, Field, Input } from "./ui/Field";

// The one Remote attendance section for every event form (M1.8 slice 2,
// consistency rule): a checkbox that offers remote seats, plus an
// optional cap on those seats. Remote seats are counted separately
// from the in-person capacity.
export interface RemoteValue {
  available: boolean;
  // The cap as typed: "" is no limit, "0" is no remote seats.
  capacity: string;
}

export const emptyRemote: RemoteValue = { available: false, capacity: "" };

// remoteFrom builds the field value from an event's stored remote
// fields (the series values, or a date's effective values).
export function remoteFrom(available?: boolean, capacity?: number | null): RemoteValue {
  return { available: available ?? false, capacity: capacity == null ? "" : String(capacity) };
}

// remoteProblem is the browser-side check before submit: a typed cap
// must be a whole number of 0 or more. The server checks again.
export function remoteProblem(v: RemoteValue): string | null {
  const cap = v.capacity.trim();
  if (cap === "") return null;
  if (!/^\d+$/.test(cap)) return "The remote seat limit needs to be a whole number of 0 or more, or leave it empty for no limit.";
  return null;
}

// remoteBody is the wire form: the checkbox, and the cap as a number
// (null clears it to no limit).
export function remoteBody(v: RemoteValue): { remoteAvailable: boolean; remoteCapacity: number | null } {
  const cap = v.capacity.trim();
  return { remoteAvailable: v.available, remoteCapacity: cap === "" ? null : Number(cap) };
}

export function RemoteField({
  idPrefix,
  value,
  onChange,
  error,
}: {
  idPrefix: string;
  value: RemoteValue;
  onChange: (next: RemoteValue) => void;
  error?: string | null;
}) {
  return (
    <div className="space-y-3">
      <Checkbox
        label="Remote attendance"
        description="People can register to join from home. Remote seats are counted on their own, not against the seats in the room."
        checked={value.available}
        onChange={(e) => onChange({ ...value, available: e.target.checked })}
      />
      {value.available && (
        <Field
          id={`remote-capacity-${idPrefix}`}
          label="Remote capacity"
          optional
          hint="Remote seats per date. Leave it blank for no limit, or 0 for no remote seats."
          error={error ?? undefined}
        >
          <Input
            type="number"
            min={0}
            placeholder="No limit"
            value={value.capacity}
            onChange={(e) => onChange({ ...value, capacity: e.target.value })}
          />
        </Field>
      )}
    </div>
  );
}
