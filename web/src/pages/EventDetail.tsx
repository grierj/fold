import { SeriesEditor } from "../components/SeriesEditor";
import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, Events } from "../api";
import { useAuth } from "../auth";
import { DateBadge } from "../components/DateBadge";
import { AttendeeNames, EventIntro } from "../components/EventIntro";
import { EventVisibilityControl } from "../components/EventVisibility";
import { RsvpButtons } from "../components/RsvpButtons";
import { GuestRegistrationForm } from "../components/GuestRegistrationForm";
import { formatTime, heldIn, zoneNote } from "../lib/when";
import type { EventDetailResponse, EventHostRow, EventOccurrence, RegistrationResponse } from "../types";
import { errorText } from "../lib/errors";

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
      else setError(errorText(e, "We couldn't load this event."));
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  function handleRsvp(eventId: number, date: string, reg: RegistrationResponse) {
    setPage((p) => {
      if (!p) return p;
      return {
        ...p,
        occurrences: p.occurrences.map((m) => {
          if (m.eventId !== eventId || m.date !== date) return m;
          const registrations = { ...m.registrations };
          const before = m.myRegistration;
          if (before && before !== reg.status) {
            registrations[before] = Math.max(0, (registrations[before] ?? 0) - 1);
          }
          if (!before || before !== reg.status) {
            registrations[reg.status] = (registrations[reg.status] ?? 0) + 1;
          }
          return {
            ...m,
            myRegistration: reg.status,
            myGuestCount: reg.guestCount,
            myWaitlistPosition: reg.waitlistPosition ?? undefined,
            registrations,
          };
        }),
      };
    });
  }

  if (loading || authLoading) {
    return <div className="max-w-3xl mx-auto px-4 py-12 text-text-muted">Loading…</div>;
  }
  if (notFound || !page) {
    return (
      <div className="max-w-3xl mx-auto px-4 py-12 text-center">
        <h1 className="text-2xl font-bold mb-2">Event not found</h1>
        <p className="text-text-muted">
          {error ?? "This event doesn't exist or isn't visible to you."}{" "}
          <Link to="/" className="text-primary font-semibold">
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
      setError(errorText(e, "We couldn't delete the event."));
    }
  }

  return (
    <div className="max-w-3xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-bold mb-2">{title}</h1>
      <EventIntro hostedBy={event.hostedBy} groupName={event.groupName} groupId={event.groupId} />
      {event.location && <p className="text-text-muted mt-1">{event.location}</p>}
      {event.description && <p className="text-text-muted whitespace-pre-wrap mt-3">{event.description}</p>}
      {error && <div className="text-danger mt-3">{error}</div>}


      {isAdmin && event.churchWide && (
        <HostEditor eventId={event.id} hosts={event.hosts ?? []} onChanged={() => void load()} />
      )}

      {canManage && (
        <div className="mt-4">
          <div className="flex flex-wrap items-center gap-4">
            <button type="button" className="text-sm text-primary font-medium" onClick={() => setEditing((v) => !v)}>
              {editing ? "Close editor" : event.recurrenceRule ? "Edit series" : "Edit event"}
            </button>
            <button type="button" className="text-sm text-danger hover:text-danger-hover font-medium" onClick={() => void deleteEvent()}>
              Delete event
            </button>
          </div>
          {editing && (
            <SeriesEditor
              series={{ ...event, eventId: event.id }}
              idPrefix={`ev${event.id}`}
              onSaved={() => { setEditing(false); void load(); }}
              onClose={() => setEditing(false)}
            />
          )}
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
      {occurrences.length === 0 && <p className="text-text-muted">No upcoming dates.</p>}
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
  onRsvp: (eventId: number, date: string, reg: RegistrationResponse) => void;
  onChanged: () => void;
  onNeedLogin: () => void;
}) {
  const [editingDate, setEditingDate] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const time = formatTime(instance.startsAt);
  const held = heldIn(instance.startsAt, instance.tzid);

  async function cancel() {
    if (!confirm(`Cancel this date on ${instance.date}?`)) return;
    setCancelling(true);
    try {
      await Events.cancelOccurrence(instance.eventId, instance.date);
      onChanged();
    } catch (e) {
      alert(errorText(e, "We couldn't cancel that date."));
      setCancelling(false);
    }
  }

  return (
    <div className="bg-surface rounded-lg border border-border p-4 flex gap-4">
      <DateBadge at={instance.startsAt} />
      <div className="grow min-w-0">
        <div className="font-bold">{instance.title || "Church event"}</div>
        <div className="text-sm text-text-muted">
          {time}
          {instance.location ? ` · ${instance.location}` : ""}
        </div>
        {held && <div className="text-sm text-text-muted">{held}</div>}
        {instance.overridden && (
          <div className="text-sm text-text-muted">This date was edited on its own, so series changes leave it alone.</div>
        )}
        <div className="text-sm text-text-muted mt-1">
          {count(instance.registrations, "going")} going · {count(instance.registrations, "maybe")} maybe ·{" "}
          {count(instance.registrations, "declined")} can&apos;t go
          {instance.capacity !== null && (
            <>
              {" "}· {instance.spotsLeft} of {instance.capacity} spots left
            </>
          )}
        </div>
        <AttendeeNames instance={instance} />
        <div className="mt-2">
          {loggedIn ? (
            <RsvpButtons instance={instance} onChange={(status) => onRsvp(instance.eventId, instance.date, status)} />
          ) : instance.visibility === "public" ? (
            <div className="space-y-2">
              <GuestRegistrationForm instance={instance} />
              <p className="text-sm text-text-muted">
                Already have an account?{" "}
                <button type="button" onClick={onNeedLogin} className="text-primary font-semibold">
                  Log in to register
                </button>
              </p>
            </div>
          ) : (
            <button type="button" onClick={onNeedLogin} className="text-sm text-primary font-semibold">
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
        {canManage && <ManageRoster instance={instance} onChanged={onChanged} />}
      </div>
      {canManage && (
        <div className="shrink-0 flex flex-col items-end gap-2">
          <button
            type="button"
            onClick={() => setEditingDate((v) => !v)}
            className="text-sm text-primary font-medium"
          >
            Edit this date
          </button>
          <button
            type="button"
            onClick={() => void cancel()}
            disabled={cancelling}
            className="text-sm text-danger hover:text-danger-hover font-medium"
          >
            {cancelling ? "Cancelling…" : "Cancel"}
          </button>
        </div>
      )}
    </div>
  );
}

// ManageRoster gives leaders and hosts the M1.5 pruning controls for one
// date: remove anyone, promote a waitlisted person by hand, or add someone
// directly. Adding over capacity asks for confirmation first.
function ManageRoster({ instance, onChanged }: { instance: EventOccurrence; onChanged: () => void }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [email, setEmail] = useState("");
  const [error, setError] = useState<string | null>(null);

  async function remove(userId: number, name: string) {
    if (!confirm(`Remove ${name}'s registration for this date? They won't be able to re-register on their own.`))
      return;
    setBusy(`remove-${userId}`);
    setError(null);
    try {
      await Events.removeRegistration(instance.eventId, instance.date, userId);
      onChanged();
    } catch (e) {
      setError(errorText(e, "We couldn't remove that registration."));
    } finally {
      setBusy(null);
    }
  }

  async function promote(userId: number) {
    setBusy(`promote-${userId}`);
    setError(null);
    try {
      await Events.promoteRegistration(instance.eventId, instance.date, userId);
      onChanged();
    } catch (e) {
      setError(errorText(e, "We couldn't move them off the waitlist."));
    } finally {
      setBusy(null);
    }
  }

  async function add(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = email.trim();
    if (!trimmed) return;
    const full = instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
    if (full && !confirm("This date is full. Add them over capacity?")) return;
    setBusy("add");
    setError(null);
    try {
      await Events.addRegistration(instance.eventId, instance.date, trimmed, full || undefined);
      setEmail("");
      onChanged();
    } catch (err) {
      setError(errorText(err, "We couldn't add that registration."));
    } finally {
      setBusy(null);
    }
  }

  const roster = (instance.attendees ?? []).filter((a) =>
    ["going", "maybe", "waitlisted"].includes(a.status)
  );
  if (!instance.attendeesVisible || roster.length === 0) return null;

  return (
    <div className="mt-3 border-t border-border pt-3">
      <div className="text-sm font-semibold text-text-muted mb-1">Manage registrations</div>
      {error && <div className="text-sm text-danger mb-1">{error}</div>}
      <ul className="text-sm space-y-1 mb-2">
        {roster.map((a) => (
          <li key={a.userId} className="flex items-center justify-between gap-3">
            <span>
              {a.name} <span className="text-text-muted">· {a.status}</span>
            </span>
            <span className="flex gap-2">
              {a.status === "waitlisted" && (
                <button
                  type="button"
                  disabled={busy !== null}
                  onClick={() => void promote(a.userId)}
                  className="text-primary font-medium"
                >
                  {busy === `promote-${a.userId}` ? "…" : "Promote"}
                </button>
              )}
              <button
                type="button"
                disabled={busy !== null}
                onClick={() => void remove(a.userId, a.name)}
                className="text-danger hover:text-danger-hover"
              >
                {busy === `remove-${a.userId}` ? "…" : "Remove"}
              </button>
            </span>
          </li>
        ))}
      </ul>
      <form onSubmit={add} className="flex gap-2">
        <input
          className="input text-sm"
          type="email"
          aria-label="Add by email"
          placeholder="Add someone by email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <button type="submit" className="btn-secondary text-sm" disabled={busy !== null}>
          {busy === "add" ? "…" : "Add"}
        </button>
      </form>
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
  const [capacity, setCapacity] = useState(instance.capacity === null ? "" : String(instance.capacity));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    const body: {
      startsLocal?: string;
      durationMinutes?: number;
      location?: string;
      title?: string;
      capacityOverride?: number | null;
    } = {};
    const startsLocal = `${date}T${time}`;
    if (startsLocal !== instance.startsLocal) body.startsLocal = startsLocal;
    if (duration !== instance.durationMinutes) body.durationMinutes = duration;
    if (title !== instance.title) body.title = title;
    if (location !== instance.location) body.location = location;
    const capTrimmed = capacity.trim();
    const capCurrent = instance.capacity === null ? "" : String(instance.capacity);
    if (capTrimmed !== capCurrent) body.capacityOverride = capTrimmed === "" ? null : Number(capTrimmed);
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
      setError(errorText(err, "We couldn't update this date."));
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-border pt-3">
      <p className="text-sm text-text-muted">
        This date only. Later series changes leave it alone. The series date stays {instance.date}. {zoneNote(instance.tzid)}
      </p>
      {error && <div className="text-sm text-danger">{error}</div>}
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
      <div>
        <label className="label" htmlFor={`cev-cap-${instance.id}`}>
          Capacity for this date
        </label>
        <input
          id={`cev-cap-${instance.id}`}
          type="number"
          min={1}
          className="input"
          placeholder="Same as the event"
          value={capacity}
          onChange={(e) => setCapacity(e.target.value)}
        />
        <p className="text-xs text-text-muted mt-1">Empty means this date uses the event&apos;s capacity.</p>
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
      setError(errorText(err, "We couldn't add that host."));
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
      setError(errorText(err, "We couldn't remove that host."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-4 bg-surface rounded-lg border border-border p-4">
      <div className="font-semibold mb-2">Hosts</div>
      {error && <div className="text-sm text-danger mb-2">{error}</div>}
      <ul className="text-sm space-y-1 mb-3">
        {hosts.map((h) => (
          <li key={h.userId} className="flex items-center justify-between gap-3">
            <span>
              {h.name} <span className="text-text-muted">{h.email}</span>
            </span>
            {hosts.length > 1 && (
              <button type="button" className="text-danger" disabled={busy} onClick={() => void remove(h.userId)}>
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

