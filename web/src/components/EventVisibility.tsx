import { useState } from "react";
import { Events } from "../api";
import type { Visibility } from "../types";
import { openedEventWarning } from "./EventIntro";

const CHOICES: { value: Visibility; label: string }[] = [
  { value: "public", label: "Public — anyone" },
  { value: "private", label: "Private — signed-in people" },
  { value: "invite_only", label: "Invite only — group members" },
];

function rank(v: string): number {
  if (v === "public") return 2;
  if (v === "private") return 1;
  return 0;
}

// EventVisibilityControl sets who can see one event. A group event can follow
// the group or keep its own value. A church-wide event is public or private.
export function EventVisibilityControl({
  eventId,
  groupName,
  groupVisibility,
  visibility,
  source,
  churchWide,
  onSaved,
}: {
  eventId: number;
  groupName?: string;
  groupVisibility?: Visibility;
  visibility: Visibility;
  source: "group" | "custom";
  churchWide?: boolean;
  onSaved: () => void;
}) {
  const [mode, setMode] = useState<"group" | "custom">(churchWide ? "custom" : source);
  const [value, setValue] = useState<Visibility>(visibility === "invite_only" && churchWide ? "public" : visibility);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const chosen: Visibility = churchWide ? value : mode === "group" ? (groupVisibility ?? "public") : value;
  const opening = !churchWide && groupVisibility != null && rank(chosen) > rank(groupVisibility);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await Events.setVisibility(eventId, {
        visibility: chosen,
        source: churchWide ? "custom" : mode,
        reason: reason.trim() || undefined,
      });
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update visibility");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-gray-100 pt-3">
      <div className="text-sm font-semibold">Who can see this event</div>
      {error && <div className="text-sm text-red-600">{error}</div>}
      {churchWide ? (
        <select
          className="input"
          aria-label="Who can see this event"
          value={value === "invite_only" ? "public" : value}
          onChange={(e) => setValue(e.target.value as Visibility)}
        >
          <option value="public">Public — anyone</option>
          <option value="private">Private — signed-in people</option>
        </select>
      ) : (
        <>
          <label className="flex items-center gap-2 text-sm">
            <input type="radio" name={`vis-${eventId}`} checked={mode === "group"} onChange={() => setMode("group")} />
            Same as the group
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input type="radio" name={`vis-${eventId}`} checked={mode === "custom"} onChange={() => setMode("custom")} />
            Choose for this event
          </label>
          {mode === "custom" && (
            <select className="input" aria-label="Event visibility" value={value} onChange={(e) => setValue(e.target.value as Visibility)}>
              {CHOICES.map((c) => (
                <option key={c.value} value={c.value}>
                  {c.label}
                </option>
              ))}
            </select>
          )}
        </>
      )}
      {opening && groupName && (
        <p className="text-sm text-gray-700 bg-amber-50 border border-amber-200 rounded-lg p-3">{openedEventWarning(groupName)}</p>
      )}
      {opening && (
        <div>
          <label className="label" htmlFor={`reason-${eventId}`}>
            Reason (optional)
          </label>
          <input
            id={`reason-${eventId}`}
            className="input"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            maxLength={500}
          />
        </div>
      )}
      <button type="submit" className="btn-secondary" disabled={busy}>
        {busy ? "Saving…" : "Save visibility"}
      </button>
    </form>
  );
}
