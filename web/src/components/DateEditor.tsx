import { useState } from "react";
import { Events } from "../api";
import type { EventOccurrence, Visibility } from "../types";
import { errorText } from "../lib/errors";
import { zoneNote } from "../lib/when";
import { focusRemoteCapacity, isRemoteError, remoteProblem } from "./RemoteField";
import { Field, FormError, Input } from "./ui/Field";
import { FormActions } from "./ui/FormActions";
import { Button } from "./ui/Button";
import { VisibilityField } from "./EventVisibility";
// DateEditor changes one occurrence only (time, duration, title,
// location). It mirrors the group page's editor: later series changes
// leave an edited date alone.
export function DateEditor({
  instance,
  onSaved,
  onClose,
}: {
  instance: EventOccurrence;
  onSaved: () => void;
  onClose: () => void;
}) {
  const [date, setDate] = useState(instance.startsLocal.slice(0, 10));
  const [time, setTime] = useState(instance.startsLocal.slice(11, 16));
  const [duration, setDuration] = useState(instance.durationMinutes);
  const [title, setTitle] = useState(instance.title);
  const [location, setLocation] = useState(instance.location);
  const [capacity, setCapacity] = useState(instance.capacity === null ? "" : String(instance.capacity));
  const [remoteCapacity, setRemoteCapacity] = useState(
    instance.remoteCapacity === null ? "" : String(instance.remoteCapacity)
  );
  const [remoteError, setRemoteError] = useState<string | null>(null);
  const remotePrefix = `date-${instance.id}`;
  const [visibility, setVisibility] = useState<Visibility | "series">(instance.visibilityOverride ?? "series");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    const remoteMsg = remoteProblem({ available: instance.remoteAvailable, capacity: remoteCapacity }, true);
    setRemoteError(remoteMsg);
    if (remoteMsg) {
      focusRemoteCapacity(remotePrefix);
      return;
    }
    const body: {
      startsLocal?: string;
      durationMinutes?: number;
      location?: string;
      title?: string;
      capacityOverride?: number | null;
      remoteCapacityOverride?: number | null;
      visibility?: Visibility | "series";
      reason?: string;
      confirm?: boolean;
    } = {};
    const startsLocal = `${date}T${time}`;
    if (startsLocal !== instance.startsLocal) body.startsLocal = startsLocal;
    if (duration !== instance.durationMinutes) body.durationMinutes = duration;
    if (title !== instance.title) body.title = title;
    if (location !== instance.location) body.location = location;
    const capTrimmed = capacity.trim();
    const capCurrent = instance.capacity === null ? "" : String(instance.capacity);
    if (capTrimmed !== capCurrent) body.capacityOverride = capTrimmed === "" ? null : Number(capTrimmed);
    if (instance.remoteAvailable) {
      const rcTrimmed = remoteCapacity.trim();
      const rcCurrent = instance.remoteCapacity === null ? "" : String(instance.remoteCapacity);
      if (rcTrimmed !== rcCurrent) body.remoteCapacityOverride = rcTrimmed === "" ? null : Number(rcTrimmed);
    }
    if (visibility !== (instance.visibilityOverride ?? "series")) { body.visibility = visibility; body.reason = reason.trim() || undefined; }
    if (Object.keys(body).length === 0) {
      onClose();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      if (body.visibility !== undefined) {
        const impact = await Events.dateVisibilityImpact(instance.eventId, instance.date, body.visibility);
        const count = impact.tightened.reduce((n, row) => n + row.registrants, 0);
        if (count > 0 && !window.confirm(`This cancels and notifies ${count} registered people who can no longer see this date. Save this date?`)) { setBusy(false); return; }
        body.confirm = count > 0;
      }
      await Events.updateOccurrence(instance.eventId, instance.date, body);
      onSaved();
    } catch (err) {
      const msg = errorText(err, "We couldn't update this date.");
      setError(msg);
      if (isRemoteError(msg)) {
        setRemoteError(msg);
        focusRemoteCapacity(remotePrefix);
      }
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-border pt-3">
      <p className="text-sm text-text-muted">
        This date only. Later series changes leave it alone. The series date stays {instance.date}. {zoneNote(instance.tzid)}
      </p>
      {error && <FormError>{error}</FormError>}
      <div className="grid sm:grid-cols-3 gap-3">
        <Field id={`cev-date-${instance.id}`} label="Date">
          <Input type="date" value={date} onChange={(e) => setDate(e.target.value)} required />
        </Field>
        <Field id={`cev-time-${instance.id}`} label="Start time">
          <Input type="time" value={time} onChange={(e) => setTime(e.target.value)} required />
        </Field>
        <Field id={`cev-dur-${instance.id}`} label="Minutes">
          <Input
            type="number"
            min={1}
            max={10080}
            value={duration}
            onChange={(e) => setDuration(Number(e.target.value))}
          />
        </Field>
      </div>
      <div className="grid sm:grid-cols-2 gap-3">
        <Field id={`cev-title-${instance.id}`} label="Title">
          <Input value={title} onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <Field id={`cev-loc-${instance.id}`} label="Location" optional>
          <Input value={location} onChange={(e) => setLocation(e.target.value)} />
        </Field>
      </div>
      <Field id={`cev-cap-${instance.id}`} label="Capacity for this date" optional hint="Leave it blank to use the event’s capacity.">
        <Input
          type="number"
          min={1}
          placeholder="Same as the event"
          value={capacity}
          onChange={(e) => setCapacity(e.target.value)}
        />
      </Field>
      {instance.remoteAvailable && (
        <Field
          id={`remote-capacity-${remotePrefix}`}
          label="Remote seat limit for this date"
          optional
          hint="Enter a whole number from 0 to 100,000. Leave it blank to use the event’s remote seat limit, or 0 to close remote seats for this date."
          error={remoteError}
        >
          <Input
            type="text"
            inputMode="numeric"
            placeholder="Same as the event"
            value={remoteCapacity}
            onChange={(e) => setRemoteCapacity(e.target.value)}
          />
        </Field>
      )}
      <VisibilityField id={`date-visibility-${instance.id}`} value={visibility === "series" ? (instance.seriesVisibility ?? instance.visibility ?? "public") : visibility} onChange={setVisibility}
        inheritSelected={visibility === "series"} onInherit={() => setVisibility("series")}
        seriesVisibility={instance.seriesVisibility ?? instance.visibility ?? "public"}
        churchWide={instance.churchWide} groupName={instance.groupName} groupVisibility={instance.groupVisibility}
        reason={reason} onReason={setReason} />
      <FormActions>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          {busy ? "Saving…" : "Save this date"}
        </Button>
        <Button variant="secondary" layout="w-full sm:w-auto" onClick={onClose}>
          Close
        </Button>
      </FormActions>
    </form>
  );
}
