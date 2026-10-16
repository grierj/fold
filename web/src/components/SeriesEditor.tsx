import { useState } from "react";
import { Events } from "../api";
import { WEEKDAYS, bydays, isSimpleWeekly, weekdayCode } from "../lib/eventForm";
import { ZoneSelect } from "./ZoneSelect";
import { errorText } from "../lib/errors";

function WeekdayPicker({ days, onToggle }: { days: string[]; onToggle: (value: string) => void }) {
  return (
    <div className="flex flex-wrap gap-2">
      {WEEKDAYS.map((w) => (
        <button
          type="button"
          key={w.value}
          onClick={() => onToggle(w.value)}
          className={
            days.includes(w.value)
              ? "px-3 py-1.5 rounded-full text-sm font-semibold bg-primary text-primary-foreground"
              : "px-3 py-1.5 rounded-full text-sm font-semibold bg-sunken text-text-muted hover:bg-border"
          }
        >
          {w.label}
        </button>
      ))}
    </div>
  );
}

export interface SeriesFields {
  eventId: number;
  startsLocal: string;
  durationMinutes: number;
  tzid: string;
  title: string;
  description: string;
  location: string;
  recurrenceRule: string;
  rruleUntil?: string;
  capacity: number | null;
  waitlistEnabled: boolean;
  guestsAllowed: number;
}

// SeriesEditor is the one "Edit series" / "Edit event" form, used on the
// group page and the event page (consistency rule, AGENTS.md).
export function SeriesEditor({
  series: instance,
  idPrefix,
  locationLabel = "Location",
  onSaved,
  onClose,
}: {
  series: SeriesFields;
  idPrefix: string;
  locationLabel?: string;
  onSaved: () => void;
  onClose?: () => void;
}) {
  const simple = isSimpleWeekly(instance.recurrenceRule);
  const [date, setDate] = useState(instance.startsLocal.slice(0, 10));
  const [time, setTime] = useState(instance.startsLocal.slice(11, 16) || "19:00");
  const [duration, setDuration] = useState(instance.durationMinutes);
  const [tzid, setTzid] = useState(instance.tzid);
  const [title, setTitle] = useState(instance.title);
  const [description, setDescription] = useState(instance.description);
  const [location, setLocation] = useState(instance.location);
  const [recurring, setRecurring] = useState(instance.recurrenceRule !== "");
  const [days, setDays] = useState<string[]>(() => {
    const picked = bydays(instance.recurrenceRule);
    if (picked.length > 0) return picked;
    if (instance.recurrenceRule.startsWith("FREQ=WEEKLY")) return [weekdayCode(instance.startsLocal.slice(0, 10))];
    return [];
  });
  const [rule, setRule] = useState(instance.recurrenceRule);
  const [until, setUntil] = useState(instance.rruleUntil ?? "");
  const [capacity, setCapacity] = useState(instance.capacity === null ? "" : String(instance.capacity));
  const [waitlistEnabled, setWaitlistEnabled] = useState(instance.waitlistEnabled);
  const [guestsAllowed, setGuestsAllowed] = useState(instance.guestsAllowed);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function toggleDay(v: string) {
    setDays((prev) => (prev.includes(v) ? prev.filter((d) => d !== v) : [...prev, v]));
  }

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (simple && recurring && days.length === 0) {
      setError("Pick at least one day to repeat weekly.");
      return;
    }
    setBusy(true);
    setError(null);
    const recurrenceRule = simple ? (recurring ? `FREQ=WEEKLY;BYDAY=${days.join(",")}` : "") : rule.trim();
    const capTrimmed = capacity.trim();
    const capInitial = instance.capacity === null ? "" : String(instance.capacity);
    // Only send capacity fields that changed: on the group page the
    // initial capacity is the date's effective value, and an untouched
    // field must not clobber a per-date override.
    const body: {
      title: string;
      description: string;
      startsLocal: string;
      durationMinutes: number;
      tzid: string;
      location: string;
      recurrenceRule: string;
      rruleUntil: string;
      capacity?: number | null;
      waitlistEnabled?: boolean;
      guestsAllowed?: number;
    } = {
      title: title.trim(),
      description: description.trim(),
      startsLocal: `${date}T${time}`,
      durationMinutes: duration,
      tzid: tzid.trim(),
      location: location.trim(),
      recurrenceRule,
      rruleUntil: until,
    };
    if (capTrimmed !== capInitial) body.capacity = capTrimmed === "" ? null : Number(capTrimmed);
    if (waitlistEnabled !== instance.waitlistEnabled) body.waitlistEnabled = waitlistEnabled;
    if (guestsAllowed !== instance.guestsAllowed) body.guestsAllowed = guestsAllowed;
    try {
      await Events.update(instance.eventId, body);
      onSaved();
    } catch (err) {
      setError(errorText(err, "We couldn't update the series."));
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-border pt-3">
      <p className="text-sm text-text-muted">
        This changes every future date that has not been edited on its own. Customized dates stay as they are. Dates
        the new schedule drops are cancelled, and people who were going or maybe are told.
      </p>
      {error && <div className="text-sm text-danger">{error}</div>}
      <div className="grid sm:grid-cols-2 gap-3">
        <div>
          <label className="label" htmlFor={`s-date-${idPrefix}`}>
            First date
          </label>
          <input id={`s-date-${idPrefix}`} type="date" className="input" value={date} onChange={(e) => setDate(e.target.value)} required />
        </div>
        <div>
          <label className="label" htmlFor={`s-time-${idPrefix}`}>
            Start time
          </label>
          <input id={`s-time-${idPrefix}`} type="time" className="input" value={time} onChange={(e) => setTime(e.target.value)} required />
        </div>
      </div>
      <div className="grid sm:grid-cols-[11rem_minmax(0,1fr)] gap-3">
        <div>
          <label className="label" htmlFor={`s-dur-${idPrefix}`}>
            Duration (minutes)
          </label>
          <input
            id={`s-dur-${idPrefix}`}
            type="number"
            min={1}
            max={10080}
            className="input"
            value={duration}
            onChange={(e) => setDuration(Number(e.target.value))}
          />
        </div>
        <div>
          <label className="label" htmlFor={`s-tz-${idPrefix}`}>
            Time zone
          </label>
          <ZoneSelect id={`s-tz-${idPrefix}`} value={tzid} onChange={setTzid} />
        </div>
      </div>
      <div className="grid sm:grid-cols-2 gap-3">
        <div>
          <label className="label" htmlFor={`s-title-${idPrefix}`}>
            Title
          </label>
          <input id={`s-title-${idPrefix}`} className="input" value={title} onChange={(e) => setTitle(e.target.value)} />
        </div>
        <div>
          <label className="label" htmlFor={`s-loc-${idPrefix}`}>
            {locationLabel}
          </label>
          <input id={`s-loc-${idPrefix}`} className="input" value={location} onChange={(e) => setLocation(e.target.value)} />
        </div>
      </div>
      <div>
        <label className="label" htmlFor={`s-desc-${idPrefix}`}>
          Description
        </label>
        <textarea
          id={`s-desc-${idPrefix}`}
          className="input"
          rows={3}
          maxLength={5000}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </div>
      {simple ? (
        <div>
          <label className="flex items-center gap-2 font-semibold cursor-pointer">
            <input
              type="checkbox"
              checked={recurring}
              onChange={(e) => {
                setRecurring(e.target.checked);
                if (e.target.checked && days.length === 0 && date) setDays([weekdayCode(date)]);
              }}
              className="w-4 h-4 accent-primary"
            />
            Repeat weekly
          </label>
          {recurring && (
            <div className="mt-3">
              <WeekdayPicker days={days} onToggle={toggleDay} />
            </div>
          )}
        </div>
      ) : (
        <div>
          <label className="label" htmlFor={`s-rule-${idPrefix}`}>
            Recurrence rule
          </label>
          <input id={`s-rule-${idPrefix}`} className="input" value={rule} onChange={(e) => setRule(e.target.value)} />
        </div>
      )}
      <div>
        <label className="label" htmlFor={`s-until-${idPrefix}`}>
          Repeat until (optional)
        </label>
        <input id={`s-until-${idPrefix}`} type="date" className="input" value={until} onChange={(e) => setUntil(e.target.value)} />
      </div>
      <div className="grid sm:grid-cols-3 gap-3">
        <div>
          <label className="label" htmlFor={`s-cap-${idPrefix}`}>
            Capacity
          </label>
          <input
            id={`s-cap-${idPrefix}`}
            type="number"
            min={1}
            className="input"
            placeholder="No limit"
            value={capacity}
            onChange={(e) => setCapacity(e.target.value)}
          />
          <p className="text-xs text-text-muted mt-1">Seats per date. Empty means no limit.</p>
        </div>
        <div>
          <label className="label" htmlFor={`s-guests-${idPrefix}`}>
            Guests allowed
          </label>
          <input
            id={`s-guests-${idPrefix}`}
            type="number"
            min={0}
            max={5}
            className="input"
            value={guestsAllowed}
            onChange={(e) => setGuestsAllowed(Math.max(0, Math.min(5, Number(e.target.value))))}
          />
          <p className="text-xs text-text-muted mt-1">Extra guests one registration may bring (0–5).</p>
        </div>
        <div className="flex items-end pb-1">
          <label className="flex items-center gap-2 text-sm cursor-pointer">
            <input
              type="checkbox"
              checked={waitlistEnabled}
              onChange={(e) => setWaitlistEnabled(e.target.checked)}
              className="w-4 h-4 accent-primary"
            />
            Waitlist when full
          </label>
        </div>
      </div>
      <p className="text-xs text-text-muted">
        When the waitlist is off, a full date refuses new registrations. When it is on, extra people join the
        waitlist in order and move up automatically as spots open.
      </p>
      <div className="flex gap-2">
        <button type="submit" disabled={busy} className="btn-primary">
          {busy ? "Saving…" : "Save series"}
        </button>
        {onClose && (
          <button type="button" onClick={onClose} className="btn-secondary">
            Close
          </button>
        )}
      </div>
    </form>
  );
}
