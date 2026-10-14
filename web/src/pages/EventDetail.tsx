import { ZoneSelect } from "../components/ZoneSelect";
import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, Events } from "../api";
import { useAuth } from "../auth";
import { DateBadge } from "../components/DateBadge";
import { AttendeeNames, EventIntro } from "../components/EventIntro";
import { EventVisibilityControl } from "../components/EventVisibility";
import { RsvpButtons } from "../components/RsvpButtons";
import { WEEKDAYS, weekdayCode } from "../lib/eventForm";
import { formatWallClock, zoneSuffix } from "../lib/wall";
import type { EventDetailResponse, EventHostRow, EventOccurrence, RegistrationStatus } from "../types";

function count(r: Record<string, number>, key: string): number {
  return r[key] ?? 0;
}

export function EventDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user, loading: authLoading, isAdmin } = useAuth();
  const [page, setPage] = useState<EventDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);

  const load = useCallback(async () => {
    if (!id) return;
    setLoading(true);
    setError(null);
    setNotFound(false);
    try {
      setPage(await Events.get(id));
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) setNotFound(true);
      else setError(e instanceof Error ? e.message : "Failed to load event");
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  function handleRsvp(eventId: number, date: string, status: RegistrationStatus) {
    setPage((p) => {
      if (!p) return p;
      return {
        ...p,
        occurrences: p.occurrences.map((m) => {
          if (m.eventId !== eventId || m.date !== date || m.myRegistration === status) return m;
          const registrations = { ...m.registrations };
          if (m.myRegistration) registrations[m.myRegistration] = Math.max(0, (registrations[m.myRegistration] ?? 0) - 1);
          registrations[status] = (registrations[status] ?? 0) + 1;
          return { ...m, myRegistration: status, registrations };
        }),
      };
    });
  }

  if (loading || authLoading) {
    return <div className="max-w-3xl mx-auto px-4 py-12 text-gray-500">Loading…</div>;
  }
  if (notFound || !page) {
    return (
      <div className="max-w-3xl mx-auto px-4 py-12 text-center">
        <h1 className="text-2xl font-bold mb-2">Event not found</h1>
        <p className="text-gray-600">
          {error ?? "This event doesn't exist or isn't visible to you."}{" "}
          <Link to="/" className="text-brand font-semibold">
            Back home
          </Link>
        </p>
      </div>
    );
  }

  const { event, occurrences, canManage, canSetVisibility } = page;
  const title = event.title || (event.hostedBy ? "Church event" : event.groupName || "Event");

  async function deleteEvent() {
    if (!confirm(`Delete “${title}”? This removes every date and its registrations.`)) return;
    try {
      await Events.delete(event.id);
      navigate("/", { replace: true });
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not delete the event");
    }
  }

  return (
    <div className="max-w-3xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-extrabold mb-2">{title}</h1>
      <EventIntro hostedBy={event.hostedBy} groupName={event.groupName} groupId={event.groupId} />
      {event.location && <p className="text-gray-600 mt-1">{event.location}</p>}
      {event.description && <p className="text-gray-700 whitespace-pre-wrap mt-3">{event.description}</p>}
      {error && <div className="text-red-600 mt-3">{error}</div>}


      {isAdmin && event.churchWide && (
        <HostEditor eventId={event.id} hosts={event.hosts ?? []} onChanged={() => void load()} />
      )}

      {canManage && (
        <div className="mt-4">
          <div className="flex flex-wrap items-center gap-4">
            <button type="button" className="text-sm text-brand font-medium" onClick={() => setEditing((v) => !v)}>
              {editing ? "Close editor" : event.recurrenceRule ? "Edit series" : "Edit event"}
            </button>
            <button type="button" className="text-sm text-red-600 hover:text-red-800 font-medium" onClick={() => void deleteEvent()}>
              Delete event
            </button>
          </div>
          {editing && <SeriesForm event={event} onSaved={() => { setEditing(false); void load(); }} />}
          {editing && canSetVisibility && (
            <EventVisibilityControl
              key={`${event.id}-${event.visibility}-${event.leaderOpened}`}
              eventId={event.id}
              churchWide={event.churchWide}
              groupName={event.groupName}
              groupVisibility={event.groupVisibility}
              visibility={event.visibility}
              leaderOpened={event.leaderOpened}
              onSaved={() => void load()}
            />
          )}
        </div>
      )}

      <h2 className="text-xl font-bold mt-8 mb-3">Upcoming dates</h2>
      {occurrences.length === 0 && <p className="text-gray-600">No upcoming dates.</p>}
      <div className="space-y-3">
        {occurrences.map((m) => (
          <DateRow
            key={`${m.eventId}-${m.date}`}
            instance={m}
            loggedIn={!!user}
            canManage={canManage}
            onRsvp={handleRsvp}
            onChanged={() => void load()}
            onNeedLogin={() => navigate("/login", { state: { from: `/events/${id}` } })}
          />
        ))}
      </div>
    </div>
  );
}

function DateRow({
  instance,
  loggedIn,
  canManage,
  onRsvp,
  onChanged,
  onNeedLogin,
}: {
  instance: EventOccurrence;
  loggedIn: boolean;
  canManage: boolean;
  onRsvp: (eventId: number, date: string, status: RegistrationStatus) => void;
  onChanged: () => void;
  onNeedLogin: () => void;
}) {
  const [editingDate, setEditingDate] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const time = formatWallClock(instance.startsLocal) + zoneSuffix(instance.startsAt, instance.tzid);

  async function cancel() {
    if (!confirm(`Cancel this date on ${instance.date}?`)) return;
    setCancelling(true);
    try {
      await Events.cancelOccurrence(instance.eventId, instance.date);
      onChanged();
    } catch (e) {
      alert(e instanceof Error ? e.message : "Cancel failed");
      setCancelling(false);
    }
  }

  return (
    <div className="bg-white rounded-xl border border-gray-200 p-4 flex gap-4">
      <DateBadge local={instance.startsLocal} />
      <div className="grow min-w-0">
        <div className="font-bold">{instance.title || "Church event"}</div>
        <div className="text-sm text-gray-600">
          {time}
          {instance.location ? ` · ${instance.location}` : ""}
        </div>
        {instance.overridden && (
          <div className="text-sm text-gray-500">This date was edited on its own, so series changes leave it alone.</div>
        )}
        <div className="text-sm text-gray-500 mt-1">
          {count(instance.registrations, "going")} going · {count(instance.registrations, "maybe")} maybe ·{" "}
          {count(instance.registrations, "declined")} can't go
        </div>
        <AttendeeNames instance={instance} />
        <div className="mt-2">
          {loggedIn ? (
            <RsvpButtons instance={instance} onChange={(status) => onRsvp(instance.eventId, instance.date, status)} />
          ) : (
            <button type="button" onClick={onNeedLogin} className="text-sm text-brand font-semibold">
              Log in to register
            </button>
          )}
        </div>
        {editingDate && (
          <DateEditor
            instance={instance}
            onSaved={() => {
              setEditingDate(false);
              onChanged();
            }}
            onClose={() => setEditingDate(false)}
          />
        )}
      </div>
      {canManage && (
        <div className="shrink-0 flex flex-col items-end gap-2">
          <button
            type="button"
            onClick={() => setEditingDate((v) => !v)}
            className="text-sm text-brand font-medium"
          >
            Edit this date
          </button>
          <button
            type="button"
            onClick={() => void cancel()}
            disabled={cancelling}
            className="text-sm text-red-600 hover:text-red-800 font-medium"
          >
            {cancelling ? "Cancelling…" : "Cancel"}
          </button>
        </div>
      )}
    </div>
  );
}

// DateEditor changes one occurrence only (time, duration, title,
// location). It mirrors the group page's editor: later series changes
// leave an edited date alone.
function DateEditor({
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
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    const body: { startsLocal?: string; durationMinutes?: number; location?: string; title?: string } = {};
    const startsLocal = `${date}T${time}`;
    if (startsLocal !== instance.startsLocal) body.startsLocal = startsLocal;
    if (duration !== instance.durationMinutes) body.durationMinutes = duration;
    if (title !== instance.title) body.title = title;
    if (location !== instance.location) body.location = location;
    if (Object.keys(body).length === 0) {
      onClose();
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await Events.updateOccurrence(instance.eventId, instance.date, body);
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update this date");
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-gray-100 pt-3">
      <p className="text-sm text-gray-600">
        This date only. Later series changes leave it alone. The series date stays {instance.date}.
      </p>
      {error && <div className="text-sm text-red-600">{error}</div>}
      <div className="grid sm:grid-cols-3 gap-3">
        <div>
          <label className="label" htmlFor={`cev-date-${instance.id}`}>
            Date
          </label>
          <input id={`cev-date-${instance.id}`} type="date" className="input" value={date} onChange={(e) => setDate(e.target.value)} required />
        </div>
        <div>
          <label className="label" htmlFor={`cev-time-${instance.id}`}>
            Start time
          </label>
          <input id={`cev-time-${instance.id}`} type="time" className="input" value={time} onChange={(e) => setTime(e.target.value)} required />
        </div>
        <div>
          <label className="label" htmlFor={`cev-dur-${instance.id}`}>
            Minutes
          </label>
          <input
            id={`cev-dur-${instance.id}`}
            type="number"
            min={1}
            max={10080}
            className="input"
            value={duration}
            onChange={(e) => setDuration(Number(e.target.value))}
          />
        </div>
      </div>
      <div className="grid sm:grid-cols-2 gap-3">
        <div>
          <label className="label" htmlFor={`cev-title-${instance.id}`}>
            Title
          </label>
          <input id={`cev-title-${instance.id}`} className="input" value={title} onChange={(e) => setTitle(e.target.value)} />
        </div>
        <div>
          <label className="label" htmlFor={`cev-loc-${instance.id}`}>
            Location
          </label>
          <input id={`cev-loc-${instance.id}`} className="input" value={location} onChange={(e) => setLocation(e.target.value)} />
        </div>
      </div>
      <div className="flex gap-2">
        <button type="submit" disabled={busy} className="btn-primary">
          {busy ? "Saving…" : "Save this date"}
        </button>
        <button type="button" onClick={onClose} className="btn-secondary">
          Close
        </button>
      </div>
    </form>
  );
}

function HostEditor({
  eventId,
  hosts,
  onChanged,
}: {
  eventId: number;
  hosts: EventHostRow[];
  onChanged: () => void;
}) {
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function add(e: React.FormEvent) {
    e.preventDefault();
    if (!email.trim()) return;
    setBusy(true);
    setError(null);
    try {
      await Events.addHost(eventId, email.trim());
      setEmail("");
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not add host");
    } finally {
      setBusy(false);
    }
  }

  async function remove(userId: number) {
    setBusy(true);
    setError(null);
    try {
      await Events.removeHost(eventId, userId);
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not remove host");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-4 bg-white rounded-xl border border-gray-200 p-4">
      <div className="font-semibold mb-2">Hosts</div>
      {error && <div className="text-sm text-red-600 mb-2">{error}</div>}
      <ul className="text-sm space-y-1 mb-3">
        {hosts.map((h) => (
          <li key={h.userId} className="flex items-center justify-between gap-3">
            <span>
              {h.name} <span className="text-gray-500">{h.email}</span>
            </span>
            {hosts.length > 1 && (
              <button type="button" className="text-red-600" disabled={busy} onClick={() => void remove(h.userId)}>
                Remove
              </button>
            )}
          </li>
        ))}
      </ul>
      <form onSubmit={add} className="flex gap-2">
        <input
          className="input"
          type="email"
          aria-label="Host email"
          placeholder="Add a host by email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <button type="submit" className="btn-secondary" disabled={busy}>
          Add
        </button>
      </form>
    </div>
  );
}

function SeriesForm({ event, onSaved }: { event: EventDetailResponse["event"]; onSaved: () => void }) {
  const [title, setTitle] = useState(event.title);
  const [description, setDescription] = useState(event.description);
  const [date, setDate] = useState(event.startsLocal.slice(0, 10));
  const [time, setTime] = useState(event.startsLocal.slice(11, 16));
  const [duration, setDuration] = useState(event.durationMinutes);
  const [tzid, setTzid] = useState(event.tzid);
  const [location, setLocation] = useState(event.location);
  const [recurring, setRecurring] = useState(event.recurrenceRule.startsWith("FREQ=WEEKLY"));
  const [days, setDays] = useState<string[]>(() => {
    const m = event.recurrenceRule.match(/BYDAY=([A-Z,]+)/);
    return m ? m[1].split(",").filter(Boolean) : [];
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await Events.update(event.id, {
        title: title.trim(),
        description: description.trim(),
        startsLocal: `${date}T${time}`,
        durationMinutes: duration,
        tzid: tzid.trim(),
        location: location.trim(),
        recurrenceRule: recurring ? `FREQ=WEEKLY;BYDAY=${days.join(",")}` : "",
      });
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not update the series");
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 bg-white rounded-xl border border-gray-200 p-4 space-y-3">
      {error && <div className="text-sm text-red-600">{error}</div>}
      <p className="text-sm text-gray-600">Future dates without their own edits move. Past dates stay.</p>
      <input className="input" aria-label="Title" value={title} onChange={(e) => setTitle(e.target.value)} />
      <textarea
        className="input"
        rows={4}
        maxLength={5000}
        aria-label="Description"
        value={description}
        onChange={(e) => setDescription(e.target.value)}
      />
      <div className="grid sm:grid-cols-2 gap-3">
        <input className="input" type="date" aria-label="Date" value={date} onChange={(e) => setDate(e.target.value)} required />
        <input className="input" type="time" aria-label="Start time" value={time} onChange={(e) => setTime(e.target.value)} required />
      </div>
      <div className="grid sm:grid-cols-2 gap-3">
        <input
          className="input"
          type="number"
          min={1}
          max={10080}
          step={1}
          aria-label="Duration in minutes"
          value={duration}
          onChange={(e) => setDuration(Number(e.target.value))}
        />
        <ZoneSelect id="series-tz" ariaLabel="Time zone" value={tzid} onChange={setTzid} />
      </div>
      <input className="input" aria-label="Location" value={location} onChange={(e) => setLocation(e.target.value)} />
      <label className="flex items-center gap-2 text-sm font-semibold">
        <input
          type="checkbox"
          checked={recurring}
          onChange={(e) => {
            setRecurring(e.target.checked);
            if (e.target.checked && days.length === 0 && date) setDays([weekdayCode(date)]);
          }}
        />
        Repeat weekly
      </label>
      {recurring && (
        <div className="flex flex-wrap gap-2">
          {WEEKDAYS.map((w) => (
            <button
              type="button"
              key={w.value}
              onClick={() => setDays((prev) => (prev.includes(w.value) ? prev.filter((d) => d !== w.value) : [...prev, w.value]))}
              className={
                days.includes(w.value)
                  ? "px-3 py-1.5 rounded-full text-sm font-semibold bg-brand text-white"
                  : "px-3 py-1.5 rounded-full text-sm font-semibold bg-gray-100 text-gray-600"
              }
            >
              {w.label}
            </button>
          ))}
        </div>
      )}
      <button type="submit" className="btn-primary" disabled={busy}>
        {busy ? "Saving…" : "Save series"}
      </button>
    </form>
  );
}
