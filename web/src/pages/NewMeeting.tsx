import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Groups } from "../api";
import { useAuth } from "../auth";

const WEEKDAYS = [
  { label: "Mon", value: "MO" },
  { label: "Tue", value: "TU" },
  { label: "Wed", value: "WE" },
  { label: "Thu", value: "TH" },
  { label: "Fri", value: "FR" },
  { label: "Sat", value: "SA" },
  { label: "Sun", value: "SU" },
];

// Build an RFC 3339 datetime with the user's local UTC offset, e.g.
// 2026-10-14T19:00:00-06:00. The server treats per-instance times as local
// times carried on the date, so preserving the local wall time matters.
function localRfc3339(date: string, time: string): string {
  const d = new Date(`${date}T${time}:00`);
  const offMin = -d.getTimezoneOffset();
  const sign = offMin >= 0 ? "+" : "-";
  const abs = Math.abs(offMin);
  const hh = String(Math.floor(abs / 60)).padStart(2, "0");
  const mm = String(abs % 60).padStart(2, "0");
  return `${date}T${time}:00${sign}${hh}:${mm}`;
}

export function NewMeeting() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user, loading: authLoading } = useAuth();
  const [groupName, setGroupName] = useState("");
  const [canManage, setCanManage] = useState(false);
  const [loading, setLoading] = useState(true);

  const [date, setDate] = useState("");
  const [startTime, setStartTime] = useState("19:00");
  const [durationMin, setDurationMin] = useState(90);
  const [location, setLocation] = useState("");
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

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!date) {
      setError("Pick a date.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const startsAt = localRfc3339(date, startTime);
      const start = new Date(`${date}T${startTime}:00`);
      const end = new Date(start.getTime() + durationMin * 60_000);
      const offMin = -start.getTimezoneOffset();
      const sign = offMin >= 0 ? "+" : "-";
      const abs = Math.abs(offMin);
      const off = `${sign}${String(Math.floor(abs / 60)).padStart(2, "0")}:${String(abs % 60).padStart(2, "0")}`;
      const pad = (n: number) => String(n).padStart(2, "0");
      const endsAt = `${end.getFullYear()}-${pad(end.getMonth() + 1)}-${pad(end.getDate())}T${pad(end.getHours())}:${pad(end.getMinutes())}:00${off}`;
      // The server expands recurrences on the UTC timeline, so BYDAY must use
      // the UTC weekday. Map each selected local weekday by the same day-shift
      // the UTC conversion applies to the start time.
      const WD = ["SU", "MO", "TU", "WE", "TH", "FR", "SA"];
      const shift = (start.getUTCDay() - start.getDay() + 7) % 7;
      const utcDays = days.map((d) => WD[(WD.indexOf(d) + shift) % 7]);
      const recurrenceRule =
        recurring && utcDays.length > 0 ? `FREQ=WEEKLY;BYDAY=${utcDays.join(",")}` : undefined;
      await Groups.createMeeting(id!, { startsAt, endsAt, location, recurrenceRule });
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
        <Link to="/login" state={{ from: `/groups/${id}/meetings/new` }} className="text-brand font-semibold">
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
        <div className="grid sm:grid-cols-2 gap-4">
          <div>
            <label className="label">Date</label>
            <input
              type="date"
              className="input"
              value={date}
              onChange={(e) => setDate(e.target.value)}
              required
            />
          </div>
          <div>
            <label className="label">Start time</label>
            <input
              type="time"
              className="input"
              value={startTime}
              onChange={(e) => setStartTime(e.target.value)}
              required
            />
          </div>
        </div>
        <div>
          <label className="label">Duration (minutes)</label>
          <input
            type="number"
            min={15}
            step={15}
            className="input"
            value={durationMin}
            onChange={(e) => setDurationMin(Number(e.target.value))}
          />
        </div>
        <div>
          <label className="label">Location (blank = group's location)</label>
          <input
            className="input"
            value={location}
            onChange={(e) => setLocation(e.target.value)}
            placeholder="e.g. Room 201, Main Campus"
          />
        </div>
        <div className="border-t border-gray-100 pt-4">
          <label className="flex items-center gap-2 font-semibold cursor-pointer">
            <input
              type="checkbox"
              checked={recurring}
              onChange={(e) => setRecurring(e.target.checked)}
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
