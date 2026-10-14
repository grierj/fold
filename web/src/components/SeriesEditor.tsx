import { useState } from "react";
import { Events } from "../api";
import { WEEKDAYS, bydays, isSimpleWeekly, weekdayCode } from "../lib/eventForm";
import { ZoneSelect } from "./ZoneSelect";

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
              ? "px-3 py-1.5 rounded-full text-sm font-semibold bg-brand text-white"
              : "px-3 py-1.5 rounded-full text-sm font-semibold bg-gray-100 text-gray-600 hover:bg-gray-200"
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
    try {
      await Events.update(instance.eventId, {
        title: title.trim(),
        description: description.trim(),
        startsLocal: `${date}T${time}`,
        durationMinutes: duration,
        tzid: tzid.trim(),
        location: location.trim(),
        recurrenceRule,
        rruleUntil: until,
      });
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update the series");
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-gray-100 pt-3">
      <p className="text-sm text-gray-600">
        This changes every future date that has not been edited on its own. Customized dates stay as they are. Dates
        the new schedule drops are cancelled, and people who were going or maybe are told.
      </p>
      {error && <div className="text-sm text-red-600">{error}</div>}
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
      <div className="grid sm:grid-cols-2 gap-3">
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
              className="w-4 h-4 accent-[#f64060]"
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
