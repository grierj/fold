import { errorCode } from "../lib/errors";
import { Checkbox, Field, Input } from "./ui/Field";

// The one Seats section for every event form (DEC-95, consistency rule): a
// seat limit per date, how many guests one registration may bring, and
// whether a full date keeps a waitlist. The create forms and Edit series share
// it, so the limit is settable when the event is made.
export interface SeatsValue {
  // The limit as typed: "" is no limit.
  capacity: string;
  waitlist: boolean;
  guests: number;
}

export const emptySeats: SeatsValue = { capacity: "", waitlist: false, guests: 0 };

export function seatsFrom(capacity: number | null, waitlist: boolean, guests: number): SeatsValue {
  return { capacity: capacity === null ? "" : String(capacity), waitlist, guests };
}

// seatsProblem is the browser-side check before submit: a typed limit must be
// a whole number from 1 to 100,000. The server checks again.
export function seatsProblem(v: SeatsValue): string | null {
  const cap = v.capacity.trim();
  if (cap === "") return null;
  if (!/^\d+$/.test(cap) || Number(cap) < 1 || Number(cap) > 100000) {
    return "The seat limit needs to be a whole number from 1 to 100,000, or leave it empty for no limit.";
  }
  return null;
}

export const isSeatsError = (err: unknown) => errorCode(err) === "invalid_capacity";

export function focusSeatLimit(idPrefix: string) {
  setTimeout(() => document.getElementById(`seats-capacity-${idPrefix}`)?.focus(), 0);
}

// seatsBody is the wire form on create: the limit as a number (null is no
// limit), the waitlist switch and the guest allowance.
export function seatsBody(v: SeatsValue) {
  const cap = v.capacity.trim();
  return { capacity: cap === "" ? null : Number(cap), waitlistEnabled: v.waitlist, guestsAllowed: v.guests };
}

export function SeatsField({
  idPrefix,
  value,
  onChange,
  error,
}: {
  idPrefix: string;
  value: SeatsValue;
  onChange: (next: SeatsValue) => void;
  error?: string | null;
}) {
  return (
    <div className="space-y-4">
      <div className="grid sm:grid-cols-2 gap-4">
        <Field
          id={`seats-capacity-${idPrefix}`}
          label="Seat limit"
          optional
          hint="Seats per date. Leave it blank for no limit."
          error={error ?? undefined}
        >
          <Input
            type="number"
            min={1}
            max={100000}
            placeholder="No limit"
            value={value.capacity}
            onChange={(e) => onChange({ ...value, capacity: e.target.value })}
          />
        </Field>
        <Field id={`seats-guests-${idPrefix}`} label="Guests allowed" hint="Extra guests one registration may bring (0–5).">
          <Input
            type="number"
            min={0}
            max={5}
            value={value.guests}
            onChange={(e) => onChange({ ...value, guests: Math.max(0, Math.min(5, Number(e.target.value))) })}
          />
        </Field>
      </div>
      <Checkbox
        label="Waitlist when full"
        description="When the waitlist is off, a full date refuses new registrations. When it is on, extra people join the waitlist in order and move up automatically as seats open."
        checked={value.waitlist}
        onChange={(e) => onChange({ ...value, waitlist: e.target.checked })}
      />
    </div>
  );
}
