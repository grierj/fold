import { errorCode } from "../lib/errors";
import { Checkbox, Field, Input } from "./ui/Field";

// The one Remote attendance section for every event form (M1.8 slice 2,
// consistency rule): a checkbox that offers remote seats, plus an
// optional cap on those seats. Remote seats are counted separately
// from the in-person capacity.
export interface RemoteValue {
  available: boolean;
  // The cap as typed: "" is no limit.
  capacity: string;
}

export const emptyRemote: RemoteValue = { available: false, capacity: "" };

// remoteFrom builds the field value from an event's stored remote
// fields (the series values, or a date's effective values).
export function remoteFrom(available?: boolean, capacity?: number | null): RemoteValue {
  return { available: available ?? false, capacity: capacity == null ? "" : String(capacity) };
}

// remoteProblem is the browser-side check before submit: a typed cap
// must be a positive whole number. Only a per-date override allows 0.
export function remoteProblem(v: RemoteValue, allowZero = false): string | null {
  if (!v.available) return null;
  const cap = v.capacity.trim();
  if (cap === "") return null;
  if (!/^\d+$/.test(cap) || !Number.isSafeInteger(Number(cap)) || Number(cap) > 100000) {
    return `The remote seat limit needs to be a whole number from ${allowZero ? 0 : 1} to 100,000, or leave it empty.`;
  }
  if (!allowZero && Number(cap) === 0) return "Uncheck remote attendance instead of a 0 remote seat limit.";
  return null;
}

export function focusRemoteCapacity(idPrefix: string) {
  setTimeout(() => document.getElementById(`remote-capacity-${idPrefix}`)?.focus(), 0);
}

export const isRemoteError = (err: unknown) => {
  const code = errorCode(err);
  return code === "invalid_remote_capacity" || code === "invalid_remote_capacity_override" || code === "remote_capacity_zero";
};

// remoteBody is the wire form: the checkbox, and the cap as a number
// (null clears it to no limit).
export function remoteBody(v: RemoteValue): { remoteAvailable: boolean; remoteCapacity: number | null } {
  const cap = v.capacity.trim();
  return { remoteAvailable: v.available, remoteCapacity: !v.available || cap === "" ? null : Number(cap) };
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
        description={<span id={`remote-hint-${idPrefix}`}>Add a video link above so remote people can join.</span>}
        aria-describedby={`remote-hint-${idPrefix}`}
        checked={value.available}
        onChange={(e) => onChange({ ...value, available: e.target.checked })}
      />
      {value.available && (
        <Field
          id={`remote-capacity-${idPrefix}`}
          label="Remote seat limit"
          optional
          hint="Remote seats per date, counted separately from in-person seats. Enter a whole number from 1 to 100,000. Leave it blank for no limit."
          error={error ?? undefined}
        >
          <Input
            type="text"
            inputMode="numeric"
            placeholder="No limit"
            value={value.capacity}
            onChange={(e) => onChange({ ...value, capacity: e.target.value })}
          />
        </Field>
      )}
    </div>
  );
}
