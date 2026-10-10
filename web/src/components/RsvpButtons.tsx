import { useState } from "react";
import { Events } from "../api";
import type { EventOccurrence, RegistrationStatus } from "../types";

const OPTIONS: { value: RegistrationStatus; label: string }[] = [
  { value: "going", label: "Going" },
  { value: "maybe", label: "Maybe" },
  { value: "declined", label: "Can't go" },
];

// Meetup-style registration buttons for one event date.
export function RsvpButtons({
  instance,
  onChange,
}: {
  instance: EventOccurrence;
  onChange: (status: RegistrationStatus) => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function choose(status: RegistrationStatus) {
    setBusy(status);
    setError(null);
    try {
      await Events.register(instance.eventId, status, instance.date);
      onChange(status);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not save your registration");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div>
      <div className="flex gap-2">
        {OPTIONS.map((o) => {
          const active = instance.myRegistration === o.value;
          return (
            <button
              key={o.value}
              disabled={busy !== null}
              onClick={() => choose(o.value)}
              className={
                active
                  ? "px-4 py-1.5 rounded-full text-sm font-semibold bg-brand text-white"
                  : "px-4 py-1.5 rounded-full text-sm font-semibold border border-gray-300 hover:border-brand hover:text-brand"
              }
            >
              {busy === o.value ? "…" : o.label}
            </button>
          );
        })}
      </div>
      {error && <div className="text-sm text-red-600 mt-1">{error}</div>}
    </div>
  );
}
