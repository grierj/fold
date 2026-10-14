import { useState } from "react";
import { ApiError, Events } from "../api";
import type { Visibility, VisibilityImpact } from "../types";
import { GroupVisibilityReview } from "./GroupVisibilityReview";
import { openedEventWarning } from "./EventIntro";

const CHOICES: { value: Visibility; label: string }[] = [
  { value: "public", label: "Public — anyone" },
  { value: "private", label: "Private — signed-in people" },
  { value: "invite_only", label: "Invite only — group members" },
];

export function visibilityRank(v: string): number {
  if (v === "public") return 2;
  if (v === "private") return 1;
  return 0;
}

// EventVisibilityControl sets who can see one event. Every event keeps its
// own value, copied from the group when it was created (DEC-48). A group
// event set wider than its group is marked as opened by a leader. A
// church-wide event is public or private.
export function EventVisibilityControl({
  eventId,
  groupName,
  groupVisibility,
  visibility,
  leaderOpened,
  churchWide,
  onSaved,
}: {
  eventId: number;
  groupName?: string;
  groupVisibility?: Visibility;
  visibility: Visibility;
  leaderOpened?: boolean;
  churchWide?: boolean;
  onSaved: () => void;
}) {
  const [value, setValue] = useState<Visibility>(visibility === "invite_only" && churchWide ? "public" : visibility);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [review, setReview] = useState<VisibilityImpact | null>(null);

  // More private: preview first and confirm, the same as a group (DEC-48).
  async function save(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (visibilityRank(value) < visibilityRank(visibility)) {
      try {
        const impact = await Events.visibilityImpact(eventId, value);
        if (impact.tightened.length > 0) {
          setReview(impact);
          return;
        }
      } catch (err) {
        setError(err instanceof Error ? err.message : "Could not check this event");
        return;
      }
    }
    await commit(false);
  }

  async function commit(confirm: boolean) {
    setBusy(true);
    setError(null);
    try {
      await Events.setVisibility(eventId, { visibility: value, reason: reason.trim() || undefined, confirm });
      setReview(null);
      onSaved();
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setReview(await Events.visibilityImpact(eventId, value));
        setError("Registrations changed. Please review again.");
        return;
      }
      setError(err instanceof Error ? err.message : "Could not update visibility");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-gray-100 pt-3">
      {error && <div className="text-sm text-red-600">{error}</div>}
      <VisibilityField
        id={`event-visibility-${eventId}`}
        value={value}
        onChange={setValue}
        churchWide={churchWide}
        groupName={groupName}
        groupVisibility={groupVisibility}
        leaderOpened={leaderOpened}
        reason={reason}
        onReason={setReason}
      />
      {review && (
        <GroupVisibilityReview
          key={JSON.stringify(review)}
          impact={review}
          subject="event"
          busy={busy}
          onConfirm={() => void commit(true)}
          onCancel={() => setReview(null)}
        />
      )}
      <button type="submit" className="btn-secondary" disabled={busy}>
        {busy ? "Saving…" : "Save visibility"}
      </button>
    </form>
  );
}

// VisibilityField is the one "Who can see this event" control, used on every
// event create form and inside every event/series editor. A group event
// defaults to its group's value; choosing wider than the group asks for an
// optional reason and is saved as a leader-opened override (DEC-17/28).
// A church-wide event is public or private.
export function VisibilityField({
  id,
  value,
  onChange,
  churchWide,
  groupName,
  groupVisibility,
  leaderOpened,
  reason,
  onReason,
}: {
  id: string;
  value: Visibility;
  onChange: (v: Visibility) => void;
  churchWide?: boolean;
  groupName?: string;
  groupVisibility?: Visibility;
  leaderOpened?: boolean;
  reason: string;
  onReason: (v: string) => void;
}) {
  const opening = !churchWide && groupVisibility != null && visibilityRank(value) > visibilityRank(groupVisibility);
  const choices = churchWide ? CHOICES.filter((c) => c.value !== "invite_only") : CHOICES;
  return (
    <div className="space-y-3">
      <div>
        <label className="label" htmlFor={id}>
          Who can see this event
        </label>
        <select id={id} className="input" value={value} onChange={(e) => onChange(e.target.value as Visibility)}>
          {choices.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
              {!churchWide && c.value === groupVisibility ? " (same as the group)" : ""}
            </option>
          ))}
        </select>
      </div>
      {leaderOpened && !churchWide && (
        <p className="text-xs text-gray-500">
          A leader opened this event wider than the group. If the group becomes more private, you'll be asked whether to keep it open.
        </p>
      )}
      {opening && groupName && (
        <p className="text-sm text-gray-700 bg-amber-50 border border-amber-200 rounded-lg p-3">{openedEventWarning(groupName)}</p>
      )}
      {opening && (
        <div>
          <label className="label" htmlFor={`${id}-reason`}>
            Reason (optional)
          </label>
          <input id={`${id}-reason`} className="input" value={reason} onChange={(e) => onReason(e.target.value)} maxLength={500} />
        </div>
      )}
    </div>
  );
}
