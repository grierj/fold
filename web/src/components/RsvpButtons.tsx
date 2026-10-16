import { useState } from "react";
import { Events } from "../api";
import type { EventOccurrence, RegistrationResponse, RegistrationStatus } from "../types";
import { errorText } from "../lib/errors";

const OPTIONS: { value: RegistrationStatus; label: string }[] = [
  { value: "going", label: "Going" },
  { value: "maybe", label: "Maybe" },
  { value: "declined", label: "Can't go" },
];

// Registration for one event date, as a Going / Maybe / Can't go
// segmented control. Knows the M1.5
// states: going, waitlisted with position, full with no waitlist, and
// removed. The guest count input appears when the event allows guests.
export function RsvpButtons({
  instance,
  onChange,
}: {
  instance: EventOccurrence;
  onChange: (reg: RegistrationResponse) => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [guests, setGuests] = useState(instance.myGuestCount ?? 0);

  const mine = instance.myRegistration;
  const removed = mine === "removed";
  const waitlisted = mine === "waitlisted";
  const fullNoWaitlist =
    instance.capacity !== null &&
    (instance.spotsLeft ?? 1) <= 0 &&
    !instance.waitlistEnabled &&
    mine !== "going" &&
    mine !== "waitlisted";

  async function choose(status: RegistrationStatus) {
    setBusy(status);
    setError(null);
    try {
      const guestCount = status === "going" ? guests : 0;
      const { registration } = await Events.register(instance.eventId, status, instance.date, guestCount);
      onChange(registration);
    } catch (e) {
      setError(errorText(e, "We couldn't save your answer."));
    } finally {
      setBusy(null);
    }
  }

  if (removed) {
    return (
      <div className="text-sm text-text-muted">
        A leader or host removed your registration for this date. Ask them to add you back.
      </div>
    );
  }

  return (
    <div>
      {waitlisted && instance.myWaitlistPosition != null && (
        <div className="text-sm font-semibold text-text-muted mb-2">
          You&apos;re on the waitlist — position {instance.myWaitlistPosition}. We&apos;ll email you if a spot
          opens.
        </div>
      )}
      {fullNoWaitlist && <div className="text-sm font-semibold text-text-muted mb-2">This event is full.</div>}
      <div className="flex gap-2 flex-wrap items-center">
        {/* Segmented control (DEC-63): one joined row, the chosen answer filled. */}
        <div role="group" aria-label="Your answer" className="flex w-full sm:max-w-sm">
        {OPTIONS.map((o) => {
          const active = mine === o.value;
          const disabled = busy !== null || (o.value === "going" && fullNoWaitlist);
          return (
            <button
              key={o.value}
              type="button"
              aria-pressed={active}
              disabled={disabled}
              onClick={() => void choose(o.value)}
              className="segment"
            >
              {busy === o.value ? "…" : o.label}
            </button>
          );
        })}
        </div>
        {instance.guestsAllowed > 0 && (
          <label className="text-sm text-text-muted flex items-center gap-1">
            Guests
            <input
              type="number"
              min={0}
              max={instance.guestsAllowed}
              value={guests}
              onChange={(e) => setGuests(Math.max(0, Math.min(instance.guestsAllowed, Number(e.target.value))))}
              className="input w-16 px-2 py-1 text-sm"
              aria-label={`Extra guests (up to ${instance.guestsAllowed})`}
            />
            <span className="text-text-subtle">(up to {instance.guestsAllowed})</span>
          </label>
        )}
      </div>
      {error && <div className="text-sm text-danger mt-1">{error}</div>}
    </div>
  );
}
