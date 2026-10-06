import { useState } from "react";
import { Meetings } from "../api";
import type { MeetingInstance } from "../types";

const OPTIONS: { value: "going" | "maybe" | "declined"; label: string }[] = [
  { value: "going", label: "Going" },
  { value: "maybe", label: "Maybe" },
  { value: "declined", label: "Can't go" },
];

// Meetup-style RSVP buttons for one meeting instance.
export function RsvpButtons({
  instance,
  onChange,
}: {
  instance: MeetingInstance;
  onChange: (status: "going" | "maybe" | "declined") => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function choose(status: "going" | "maybe" | "declined") {
    setBusy(status);
    setError(null);
    try {
      await Meetings.rsvp(instance.meetingId, status, instance.date);
      onChange(status);
    } catch (e) {
      setError(e instanceof Error ? e.message : "RSVP failed");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div>
      <div className="flex gap-2">
        {OPTIONS.map((o) => {
          const active = instance.myRsvp === o.value;
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
