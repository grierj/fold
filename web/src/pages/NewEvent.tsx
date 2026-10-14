import { VisibilityField } from "../components/EventVisibility";
import type { Visibility } from "../types";
import { ZoneSelect } from "../components/ZoneSelect";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Groups } from "../api";
import { useAuth } from "../auth";
import { WEEKDAYS, weekdayCode } from "../lib/eventForm";
import { useEventZone } from "../lib/useEventZone";

export function NewEvent() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user, loading: authLoading } = useAuth();
  const [groupName, setGroupName] = useState("");
  const [canManage, setCanManage] = useState(false);
  const [loading, setLoading] = useState(true);

  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [date, setDate] = useState("");
  const [startTime, setStartTime] = useState("19:00");
  const [durationMin, setDurationMin] = useState(90);
  const { tzid, setTzid } = useEventZone();
  const [location, setLocation] = useState("");
  // Asked at creation, defaulting to the group's value (DEC-51).
  const [groupVisibility, setGroupVisibility] = useState<Visibility>("public");
  const [visibility, setVisibility] = useState<Visibility>("public");
  const [visReason, setVisReason] = useState("");
  const [recurring, setRecurring] = useState(false);
  const [days, setDays] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!id) return;
    let alive = true;
    Groups.get(id)
      .then((d) => {
        if (!alive) return;
        setGroupName(d.group.name);
        setCanManage(d.canManage);
        setLocation(d.group.location);
        setGroupVisibility(d.group.visibility);
        setVisibility(d.group.visibility);
      })
      .catch(() => {
        if (alive) setCanManage(false);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [id]);

  function toggleDay(v: string) {
    setDays((prev) => (prev.includes(v) ? prev.filter((d) => d !== v) : [...prev, v]));
  }

  function onRecurring(checked: boolean) {
    setRecurring(checked);
    if (checked && days.length === 0 && date) setDays([weekdayCode(date)]);
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!date) {
      setError("Pick a date.");
      return;
    }
    if (!tzid.trim()) {
      setError("Pick a time zone.");
      return;
    }
    if (recurring && days.length === 0) {
      setError("Pick at least one day to repeat weekly.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await Groups.createEvent(id!, {
        title: title.trim(),
        description: description.trim(),
        startsLocal: `${date}T${startTime}`,
        durationMinutes: durationMin,
        tzid: tzid.trim(),
        location: location.trim(),
        recurrenceRule: recurring ? `FREQ=WEEKLY;BYDAY=${days.join(",")}` : "",
        visibility,
        ...(visReason.trim() ? { visibilityReason: visReason.trim() } : {}),
      });
      navigate(`/groups/${id}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to schedule event");
    } finally {
      setBusy(false);
    }
  }

  if (loading || authLoading) {
    return <div className="max-w-2xl mx-auto px-4 py-12 text-gray-500">Loading…</div>;
  }

  if (!user) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <Link to="/login" state={{ from: `/groups/${id}/events/new` }} className="text-brand font-semibold">
          Log in
        </Link>{" "}
        to schedule an event.
      </div>
    );
  }

  if (!canManage) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <p className="text-gray-600">
          Only the group leader or an admin can schedule events for{" "}
          <span className="font-semibold">{groupName || "this group"}</span>.
        </p>
        <Link to={`/groups/${id}`} className="text-brand font-semibold">
          Back to group
        </Link>
      </div>
    );
  }

  return (
    <div className="max-w-2xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-extrabold mb-2">Schedule an event</h1>
      <p className="text-gray-600 mb-6">
        for <span className="font-semibold">{groupName}</span>
      </p>
      <form onSubmit={submit} className="bg-white rounded-xl border border-gray-200 p-6 space-y-4">
        {error && <div className="text-red-600 text-sm">{error}</div>}
        <div>
          <label className="label" htmlFor="event-title">
            Title (optional)
          </label>
          <input
            id="event-title"
            className="input"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={groupName || "Event name"}
          />
        </div>
        <div>
          <label className="label" htmlFor="event-description">
            Description
          </label>
          <textarea
            id="event-description"
            className="input"
            rows={4}
            maxLength={5000}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="What should people know before they come?"
          />
        </div>
        <div className="grid sm:grid-cols-2 gap-4">
          <div>
            <label className="label" htmlFor="event-date">
              Date
            </label>
            <input
              id="event-date"
              type="date"
              className="input"
              value={date}
              onChange={(e) => {
                const v = e.target.value;
                setDate(v);
                if (recurring && days.length === 0 && v) setDays([weekdayCode(v)]);
              }}
              required
            />
          </div>
          <div>
            <label className="label" htmlFor="event-time">
              Start time
            </label>
            <input
              id="event-time"
              type="time"
              className="input"
              value={startTime}
              onChange={(e) => setStartTime(e.target.value)}
              required
            />
          </div>
        </div>
        <div className="grid sm:grid-cols-2 gap-4">
          <div>
            <label className="label" htmlFor="event-duration">
              Duration (minutes)
            </label>
            <input
              id="event-duration"
              type="number"
              min={1}
              max={10080}
              step={1}
              className="input"
              value={durationMin}
              onChange={(e) => setDurationMin(Number(e.target.value))}
            />
          </div>
          <div>
            <label className="label" htmlFor="event-tz">
              Time zone
            </label>
            <ZoneSelect id="event-tz" value={tzid} onChange={setTzid} describedBy="tz-hint" />
            <p id="tz-hint" className="text-sm text-gray-500 mt-1">
              The church's time zone. Change it if the event is held somewhere else. The clock stays put across daylight saving.
            </p>
          </div>
        </div>
        <div>
          <label className="label" htmlFor="event-location">
            Location (blank = group's location)
          </label>
          <input
            id="event-location"
            className="input"
            value={location}
            onChange={(e) => setLocation(e.target.value)}
            placeholder="e.g. Room 201, Main Campus"
          />
        </div>
        <VisibilityField
          id="event-visibility"
          value={visibility}
          onChange={setVisibility}
          groupName={groupName}
          groupVisibility={groupVisibility}
          reason={visReason}
          onReason={setVisReason}
        />
        <div className="border-t border-gray-100 pt-4">
          <label className="flex items-center gap-2 font-semibold cursor-pointer">
            <input
              type="checkbox"
              checked={recurring}
              onChange={(e) => onRecurring(e.target.checked)}
              className="w-4 h-4 accent-[#f64060]"
            />
            Repeat weekly
          </label>
          {recurring && (
            <div className="mt-3 flex flex-wrap gap-2">
              {WEEKDAYS.map((w) => (
                <button
                  type="button"
                  key={w.value}
                  onClick={() => toggleDay(w.value)}
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
          )}
        </div>
        <div className="flex gap-3">
          <button type="submit" disabled={busy} className="btn-primary">
            {busy ? "Scheduling…" : "Schedule event"}
          </button>
          <Link to={`/groups/${id}`} className="btn-secondary">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}
