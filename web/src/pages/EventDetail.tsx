import { SeriesEditor } from "../components/SeriesEditor";
import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { ApiError, Events } from "../api";
import { useAuth } from "../auth";
import { DateBadge } from "../components/DateBadge";
import { EventIntro } from "../components/EventIntro";
import { Attendance } from "../components/Attendance";
import { RemoteBadge } from "../components/RemoteBadge";
import { focusRemoteCapacity, isRemoteError, remoteProblem } from "../components/RemoteField";
import { Menu } from "../components/ui/Menu";
import { Field, FormError, Input } from "../components/ui/Field";
import { Button, ButtonLink } from "../components/ui/Button";
import { ExternalLinkIcon } from "../components/icons";
import { FormActions } from "../components/ui/FormActions";
import { EventVisibilityControl } from "../components/EventVisibility";
import { RsvpButtons } from "../components/RsvpButtons";
import { GuestRegistrationForm } from "../components/GuestRegistrationForm";
import { formatTime, heldIn, zoneNote } from "../lib/when";
import type { EventDetailResponse, EventHostRow, EventOccurrence, RegistrationResponse } from "../types";
import { errorText } from "../lib/errors";
import { ErrorNotice } from "../components/ErrorNotice";
import { Missing } from "./NotFound";
import { EmptyState } from "../components/ui/EmptyState";
import { Skeleton } from "../components/ui/Skeleton";

export function EventDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user, loading: authLoading, isAdmin } = useAuth();
  const [page, setPage] = useState<EventDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);

  // quiet refreshes in place (after an RSVP) without the Loading screen.
  const load = useCallback(async (quiet = false) => {
    if (!id) return;
    if (!quiet) setLoading(true);
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

  // Answer right away, then refetch so names, spots and the waitlist match
  // the server.
  function handleRsvp(eventId: number, date: string, reg: RegistrationResponse) {
    void load(true);
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
          // Only leaders and admins get a "declined" count (DEC-64); don't
          // invent one for anyone else.
          if ((!before || before !== reg.status) && (reg.status !== "declined" || "declined" in registrations)) {
            registrations[reg.status] = (registrations[reg.status] ?? 0) + 1;
          }
          return {
            ...m,
            myRegistration: reg.status,
            myGuestCount: reg.guestCount,
            myWaitlistPosition: reg.waitlistPosition ?? undefined,
            myAttendanceMode: reg.attendanceMode,
            myChildcareCount: reg.childcareCount, myChildcareAgesNote: reg.childcareAgesNote,
            registrations,
          };
        }),
      };
    });
  }

  if (loading || authLoading) {
    return (
      <div className="max-w-3xl mx-auto px-4 py-8">
        <Skeleton kind="page" />
      </div>
    );
  }
  if (notFound) {
    return <Missing title="Event not found" body="It may have been cancelled or removed, or it may be private." />;
  }
  if (!page) {
    return (
      <div className="max-w-xl mx-auto px-4 py-12 sm:py-16">
        <ErrorNotice message={error ?? "We couldn't load this event."} onRetry={() => void load()} />
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
      {event.childcareAvailable && <div className="mt-3 text-text-muted">
        <p className="font-semibold">Childcare available{event.childcareAges && ` · ${event.childcareAges}`}</p>
        {event.childcareNotes && <p className="whitespace-pre-wrap">{event.childcareNotes}</p>}
      </div>}
      {event.description && <p className="text-text-muted whitespace-pre-wrap mt-3">{event.description}</p>}
      {error && <div className="text-danger mt-3">{error}</div>}


      {isAdmin && event.churchWide && (
        <HostEditor eventId={event.id} hosts={event.hosts ?? []} onChanged={() => void load()} />
      )}

      {canManage && (
        <div className="mt-4">
          <div className="flex flex-wrap items-center gap-4">
            <button type="button" className="inline-flex items-center min-h-11 text-sm text-primary font-medium" onClick={() => setEditing((v) => !v)}>
              {editing ? "Close editor" : event.recurrenceRule ? "Edit series" : "Edit event"}
            </button>
            <Menu tone="quiet" label="More event actions" items={[{ label: "Delete event", onSelect: () => void deleteEvent(), danger: true }]} />
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
      {occurrences.length === 0 && (
        <EmptyState title="No upcoming dates." body="This event has no more dates on the calendar." />
      )}
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
  // Manage sign-ups opens from the date's More menu (PR #66 review F1).
  const [managing, setManaging] = useState(false);
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
    // Badge, details and More on top; the answer and any open panel span the
    // card below, so the RSVP is full width on phones (PR #66 review N1).
    <div className="bg-surface rounded-lg border border-border p-4">
    <div className="flex gap-4">
      <DateBadge at={instance.startsAt} />
      <div className="grow min-w-0">
        <div className="font-bold">{instance.title || "Church event"}</div>
        <div className="text-sm text-text-muted">
          {time}
          {instance.location ? ` · ${instance.location}` : ""}
        </div>
        {held && <div className="text-sm text-text-muted">{held}</div>}
        {instance.videoUrl ? (
          <div>
            <ButtonLink to={instance.videoUrl} external variant="secondary">
              Join by video
              <span className="sr-only"> (opens in a new tab)</span>
              <ExternalLinkIcon className="h-4 w-4" />
            </ButtonLink>
          </div>
        ) : instance.videoHidden ? (
          <div className="text-sm text-text-muted">Tap Going below to get the video link.</div>
        ) : null}
        {instance.overridden && (
          <div className="text-sm text-text-muted">This date was edited on its own, so series changes leave it alone.</div>
        )}
        <Attendance instance={instance} />
      </div>
      {canManage && (
        <div className="shrink-0">
          <Menu
            tone="quiet"
            label={`More for ${instance.date}`}
            items={[
              { label: "Edit this date", onSelect: () => setEditingDate((v) => !v) },
              { label: "Manage sign-ups", onSelect: () => setManaging((v) => !v) },
              { label: cancelling ? "Cancelling…" : "Cancel this date", onSelect: () => void cancel(), danger: true, disabled: cancelling },
            ]}
          />
        </div>
      )}
    </div>
    <div className="mt-3">
        <div>
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
        {canManage && managing && <ManageRoster instance={instance} onChanged={onChanged} onClose={() => setManaging(false)} />}
    </div>
    </div>
  );
}

// ManageRoster gives leaders and hosts the M1.5 pruning controls for one
// date: remove anyone, promote a waitlisted person by hand, or add someone
// directly. Adding over capacity asks for confirmation first.
function ManageRoster({ instance, onChanged, onClose }: { instance: EventOccurrence; onChanged: () => void; onClose: () => void }) {
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
  return (
    <section aria-label="Manage sign-ups" className="mt-3 border-t border-border pt-3">
      <div className="mb-1 flex items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">Manage sign-ups</h3>
        <Button variant="link" onClick={onClose}>Close</Button>
      </div>
      {roster.length === 0 && <p className="text-sm text-text-muted mb-2">No one has signed up yet.</p>}
      {error && <FormError layout="mb-1">{error}</FormError>}
      <ul className="text-sm space-y-1 mb-2">
        {roster.map((a) => (
          <li key={a.userId} className="flex items-center justify-between gap-3">
            <span>
              {a.name} <span className="text-text-muted">· {a.status}</span>
              {a.attendanceMode === "remote" && <> <RemoteBadge /></>}
              {instance.childcareAvailable && (a.childcareCount ?? 0) > 0 && ` +${a.childcareCount} in childcare`}
              {instance.childcareAvailable && a.childcareAgesNote && ` · Ages: ${a.childcareAgesNote}`}
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
      <form onSubmit={add} className="space-y-2">
        <Field label="Add someone" hint="Their email address">
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
        </Field>
        <Button type="submit" variant="secondary" disabled={busy !== null}>
          {busy === "add" ? "Adding…" : "Add"}
        </Button>
      </form>
    </section>
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
  const [remoteCapacity, setRemoteCapacity] = useState(
    instance.remoteCapacity === null ? "" : String(instance.remoteCapacity)
  );
  const [remoteError, setRemoteError] = useState<string | null>(null);
  const remotePrefix = `date-${instance.id}`;
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
      {error && <FormError layout="mb-2">{error}</FormError>}
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
      <form onSubmit={add} className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1">
          <Field label="Host email" hint="They must already have an account.">
            <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </Field>
        </div>
        <Button variant="secondary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          Add host
        </Button>
      </form>
    </div>
  );
}

