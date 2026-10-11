import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Events } from "../api";
import { useAuth } from "../auth";
import { COMMON_ZONES, WEEKDAYS, weekdayCode } from "../lib/eventForm";
import { browserTimeZone } from "../lib/wall";

export function NewChurchEvent() {
  const navigate = useNavigate();
  const { user, loading, isAdmin } = useAuth();
  const [title, setTitle] = useState("");
  const [date, setDate] = useState("");
  const [startTime, setStartTime] = useState("19:00");
  const [durationMin, setDurationMin] = useState(90);
  const [tzid, setTzid] = useState(browserTimeZone);
  const [location, setLocation] = useState("");
  const [visibility, setVisibility] = useState<"public" | "private">("public");
  const [hostEmail, setHostEmail] = useState("");
  const [recurring, setRecurring] = useState(false);
  const [days, setDays] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function toggleDay(v: string) {
    setDays((prev) => (prev.includes(v) ? prev.filter((d) => d !== v) : [...prev, v]));
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!title.trim()) {
      setError("Give the event a title.");
      return;
    }
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
      const { event } = await Events.createChurch({
        title: title.trim(),
        startsLocal: `${date}T${startTime}`,
        durationMinutes: durationMin,
        tzid: tzid.trim(),
        location: location.trim(),
        visibility,
        hosts: hostEmail.trim() ? [hostEmail.trim()] : [],
        recurrenceRule: recurring ? `FREQ=WEEKLY;BYDAY=${days.join(",")}` : "",
      });
      navigate(`/events/${event.id}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to schedule event");
    } finally {
      setBusy(false);
    }
  }

  if (loading) {
    return <div className="max-w-2xl mx-auto px-4 py-12 text-gray-500">Loading…</div>;
  }
  if (!user) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <Link to="/login" state={{ from: "/events/new" }} className="text-brand font-semibold">
          Log in
        </Link>{" "}
        to schedule a church-wide event.
      </div>
    );
  }
  if (!isAdmin) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center text-gray-600">
        Only an admin can schedule a church-wide event.
      </div>
    );
  }

  return (
    <div className="max-w-2xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-extrabold mb-2">Schedule a church-wide event</h1>
      <p className="text-gray-600 mb-6">You are a host. Add another host if someone else should run it.</p>
      <form onSubmit={submit} className="bg-white rounded-xl border border-gray-200 p-6 space-y-4">
        {error && <div className="text-red-600 text-sm">{error}</div>}
        <div>
          <label className="label" htmlFor="church-event-title">
            Title
          </label>
          <input id="church-event-title" className="input" value={title} onChange={(e) => setTitle(e.target.value)} required />
        </div>
        <div className="grid sm:grid-cols-2 gap-4">
          <div>
            <label className="label" htmlFor="church-event-date">
              Date
            </label>
            <input
              id="church-event-date"
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
            <label className="label" htmlFor="church-event-time">
              Start time
            </label>
            <input
              id="church-event-time"
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
            <label className="label" htmlFor="church-event-duration">
              Duration (minutes)
            </label>
            <input
              id="church-event-duration"
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
            <label className="label" htmlFor="church-event-tz">
              Time zone
            </label>
            <input
              id="church-event-tz"
              className="input"
              list="church-event-zones"
              value={tzid}
              onChange={(e) => setTzid(e.target.value)}
              required
            />
            <datalist id="church-event-zones">
              {COMMON_ZONES.map((z) => (
                <option key={z} value={z} />
              ))}
            </datalist>
          </div>
        </div>
        <div>
          <label className="label" htmlFor="church-event-location">
            Location
          </label>
          <input id="church-event-location" className="input" value={location} onChange={(e) => setLocation(e.target.value)} />
        </div>
        <div>
          <label className="label" htmlFor="church-event-visibility">
            Who can see it
          </label>
          <select
            id="church-event-visibility"
            className="input"
            value={visibility}
            onChange={(e) => setVisibility(e.target.value as "public" | "private")}
          >
            <option value="public">Public — anyone</option>
            <option value="private">Private — signed-in people</option>
          </select>
        </div>
        <div>
          <label className="label" htmlFor="church-event-host">
            Another host's email (optional)
          </label>
          <input
            id="church-event-host"
            type="email"
            className="input"
            value={hostEmail}
            onChange={(e) => setHostEmail(e.target.value)}
            placeholder="They must already have an account"
          />
        </div>
        <div className="border-t border-gray-100 pt-4">
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
          <Link to="/" className="btn-secondary">
            Cancel
          </Link>
        </div>
      </form>
    </div>
  );
}
