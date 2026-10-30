import { Form } from "../components/ui/Form";
import { useHousehold } from "../lib/useHousehold";
import { SeriesEditor } from "../components/SeriesEditor";
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { ApiError, Events } from "../api";
import { useAuth } from "../auth";
import { OccurrenceCard } from "../components/OccurrenceCard";
import { EventIntro } from "../components/EventIntro";
import { Attendance } from "../components/Attendance";
import { DateEditor } from "../components/DateEditor";
import { Menu } from "../components/ui/Menu";
import { Field, FormError, Input } from "../components/ui/Field";
import { Button, ButtonLink } from "../components/ui/Button";
import { ExternalLinkIcon } from "../components/icons";
import { EventVisibilityControl } from "../components/EventVisibility";
import { RsvpButtons } from "../components/RsvpButtons";
import { GuestRegistrationForm } from "../components/GuestRegistrationForm";
import { DateActions } from "../components/DateActions";
import { formatTime, heldIn } from "../lib/when";
import { placeLine } from "../lib/place";
import { dateLine, whenLine } from "../lib/repeat";
import type { EventDetailResponse, EventHostRow, EventOccurrence, HouseholdPerson } from "../types";
import { errorText } from "../lib/errors";
import { ErrorNotice } from "../components/ErrorNotice";
import { Missing } from "./NotFound";
import { EmptyState } from "../components/ui/EmptyState";
import { Skeleton } from "../components/ui/Skeleton";
import { applyActorRsvpResults, type ActorRsvpResult, type SeriesFeedbackSignal } from "../lib/rsvpFeedback";
import { useRsvpMutationGate, type RsvpMutationGate } from "../lib/useRsvpMutationGate";

const ManageRoster = lazy(() => import("../components/ManageRoster").then((module) => ({ default: module.ManageRoster })));

export function EventDetail() {
  const { id } = useParams<{ id: string }>();
  // Another event must never inherit the editor, feedback or pending loads.
  return <EventDetailContent key={id} />;
}

function EventDetailContent() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  // The group page's "Manage sign-ups" links here with ?manage=<original date> (DEC-99).
  const manageDate = useSearchParams()[0].get("manage");
  const { user, loading: authLoading, isAdmin } = useAuth();
  const { people, error: householdError, retry: retryHousehold } = useHousehold(user);
  const [page, setPage] = useState<EventDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [seriesFeedback, setSeriesFeedback] = useState<SeriesFeedbackSignal | null>(null);
  const seriesFeedbackRevision = useRef(0);
  const loadRevision = useRef(0);
  const rsvpMutationGate = useRsvpMutationGate();
  const editorRef = useRef<HTMLDivElement>(null);

  // Opening the editor brings it into view and puts the cursor in its first
  // field, so a screen reader and a keyboard follow the button they pressed.
  useEffect(() => {
    if (!editing) return;
    const editor = editorRef.current;
    editor?.scrollIntoView?.({ block: "start" });
    editor?.querySelector<HTMLElement>("input, textarea, select")?.focus({ preventScroll: true });
  }, [editing]);

  // Quiet refreshes keep ordinary RSVP drafts mounted. A terminal refresh
  // invalidates protected data immediately, and only the newest request may
  // commit page, error, not-found or loading state.
  const load = useCallback(async (quiet = false, failClosed = false) => {
    if (!id) return;
    const revision = ++loadRevision.current;
    if (failClosed) {
      setPage(null);
      setLoading(true);
    } else if (!quiet) {
      setLoading(true);
    }
    setError(null);
    setNotFound(false);
    try {
      const next = await Events.get(id);
      if (loadRevision.current === revision) setPage(next);
    } catch (e) {
      if (loadRevision.current !== revision) return;
      if (e instanceof ApiError && e.status === 404) setNotFound(true);
      else setError(errorText(e, "We couldn't load this event."));
    } finally {
      if (loadRevision.current === revision) setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  // Refetch the whole date after a batch; counts and mixed outcomes come from the server.
  function handleRsvp() { void load(true); }
  function reportSeriesFeedback(eventId: number, date: string, actorResults: ActorRsvpResult[]) {
    const signal = { eventId, date, actorResults, revision: ++seriesFeedbackRevision.current };
    setPage((current) => current ? {
      ...current,
      occurrences: current.occurrences.map((instance) => applyActorRsvpResults(instance, eventId, actorResults)),
    } : current);
    setSeriesFeedback(signal);
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
        <h1 className="text-3xl font-bold mb-6">Event unavailable</h1>
        <ErrorNotice message={error ?? "We couldn't load this event."} onRetry={() => void load()} />
      </div>
    );
  }

  const { event, occurrences, canManage, canSetVisibility } = page;
  const title = event.title || (event.hostedBy ? "Church event" : event.groupName || "Event");
  // A single date reads as one fact in the header, with no list around it (DEC-92).
  const single = !event.recurrenceRule && occurrences.length === 1;
  const next = occurrences[0];
  const line = whenLine(event.recurrenceRule, occurrences);
  // A single date's confirmed room is its place; a series header keeps the typed location (M2.1b).
  const headerPlace = placeLine(single ? next.room : undefined, event.location);

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
      {/* Edit series and More sit beside the title as secondary actions; an event page has no primary one (DEC-63, DEC-65). */}
      <div className="sm:flex sm:items-start sm:justify-between sm:gap-4">
        <div className="min-w-0">
          <h1 className="text-3xl font-bold mb-2 wrap-anywhere">{title}</h1>
          <EventIntro hostedBy={event.hostedBy} groupName={event.groupName} groupId={event.groupId} churchWide={event.churchWide} />
        </div>
        {canManage && (
          <div className="mt-3 flex gap-2 sm:mt-0 sm:shrink-0">
            <Button variant="secondary" layout="flex-1 sm:flex-none" aria-expanded={editing} onClick={() => setEditing((v) => !v)}>
              {event.recurrenceRule ? "Edit series" : "Edit event"}
            </Button>
            <Menu label="More event actions" items={[{ label: "Delete event", onSelect: () => void deleteEvent(), danger: true }]} />
          </div>
        )}
      </div>
      {line && <p className="mt-1 font-medium wrap-anywhere">{line}</p>}
      {single && heldIn(next.startsAt, next.tzid) && <p className="text-sm text-text-muted">{heldIn(next.startsAt, next.tzid)}</p>}
      {headerPlace && <p className="text-text-muted mt-1 wrap-anywhere">{headerPlace}</p>}
      {single && line && <DateActions eventId={event.id} date={next.date} sharePath={`/events/${event.id}`} title={title} when={line} />}
      {event.childcareAvailable && <div className="mt-3 text-text-muted">
        <p>Childcare available{event.childcareAges && ` · ${event.childcareAges}`}</p>
        {event.childcareNotes && <p className="whitespace-pre-wrap">{event.childcareNotes}</p>}
      </div>}
      {event.description && <p className="text-text-muted whitespace-pre-wrap mt-3">{event.description}</p>}
      {error && <div className="text-danger mt-3">{error}</div>}


      {isAdmin && event.churchWide && (
        <HostEditor eventId={event.id} hosts={event.hosts ?? []} onChanged={() => void load()} />
      )}

      {canManage && editing && (
        <div ref={editorRef} className="mt-4">
          <SeriesEditor
            series={{ ...event, eventId: event.id }}
            idPrefix={`ev${event.id}`}
            roomDate={single ? next.date : undefined}
            onRoomChanged={() => void load(true)}
            onSaved={() => { setEditing(false); void load(); }}
            onClose={() => setEditing(false)}
          />
          {canSetVisibility && (
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

      {!single && <h2 className="text-xl font-bold mt-8 mb-3">Upcoming dates</h2>}
      {householdError && <ErrorNotice message={householdError} onRetry={retryHousehold} />}
      {occurrences.length === 0 && (
        <EmptyState title="No upcoming dates." body="This event has no more dates on the calendar." />
      )}
      <div className={single ? "mt-6 space-y-3" : "space-y-3"}>
        {occurrences.map((m) => (
          <DateRow
            key={`${m.eventId}-${m.date}`}
            instance={m}
            single={single}
            eventTitle={title}
            headerLocation={event.location}
            loggedIn={!!user}
            canManage={canManage}
            manageRequested={canManage && manageDate === m.date}
            onRsvp={handleRsvp}
            people={people}
            seriesFeedback={seriesFeedback}
            mutationGate={rsvpMutationGate}
            onSeriesFeedback={reportSeriesFeedback}
            onTerminalRefresh={() => void load(true, true)}
            onChanged={() => void load(true)}
            onNeedLogin={() => navigate("/login", { state: { from: `/events/${id}` } })}
          />
        ))}
      </div>
    </div>
  );
}

function DateRow({
  instance,
  single,
  eventTitle,
  headerLocation,
  loggedIn,
  canManage,
  manageRequested,
  onRsvp,
  people,
  seriesFeedback,
  mutationGate,
  onSeriesFeedback,
  onTerminalRefresh,
  onChanged,
  onNeedLogin,
}: {
  instance: EventOccurrence;
  // The page header already says this date's title, place and time.
  single: boolean;
  eventTitle: string;
  headerLocation: string;
  loggedIn: boolean;
  canManage: boolean;
  // ?manage=<this date> asked for the Manage sign-ups panel to open (DEC-99).
  manageRequested: boolean;
  onRsvp: () => void;
  people: HouseholdPerson[];
  seriesFeedback: SeriesFeedbackSignal | null;
  mutationGate: RsvpMutationGate;
  onSeriesFeedback: (eventId: number, date: string, actorResults: ActorRsvpResult[]) => void;
  onTerminalRefresh: () => void;
  onChanged: () => void;
  onNeedLogin: () => void;
}) {
  const [editingDate, setEditingDate] = useState(false);
  // Manage sign-ups opens from the date's More menu (PR #66 review F1).
  const [managing, setManaging] = useState(manageRequested);
  const manageRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!manageRequested) return;
    setManaging(true);
    manageRef.current?.scrollIntoView?.({ block: "start" });
  }, [manageRequested]);
  const [cancelling, setCancelling] = useState(false);
  const time = formatTime(instance.startsAt);
  const held = heldIn(instance.startsAt, instance.tzid);
  const place = placeLine(instance.room, instance.location);

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
    <OccurrenceCard at={instance.startsAt} title={instance.title || "Church event"} compact={single}
      notice={canManage && instance.visibilityOverride && <p className="text-xs text-text-muted">Visibility changed for this date</p>}
      meta={single ? (
        // Only what this date changed from the header.
        (instance.title && instance.title !== eventTitle) || (instance.location && instance.location !== headerLocation) ? <>
          {instance.title && instance.title !== eventTitle && <div className="font-semibold">{instance.title}</div>}
          {instance.location && instance.location !== headerLocation && <div className="text-sm text-text-muted">{instance.location}</div>}
        </> : null
      ) : <>
        <div className="text-sm text-text-muted">{time}{place ? ` · ${place}` : ""}</div>
        {held && <div className="text-sm text-text-muted">{held}</div>}
      </>}
      menu={canManage && <Menu
        tone="quiet"
        label={`More for ${instance.date}`}
        items={[
          { label: "Edit this date", onSelect: () => setEditingDate((v) => !v) },
          { label: "Manage sign-ups", onSelect: () => setManaging((v) => !v) },
          { label: cancelling ? "Cancelling…" : "Cancel this date", onSelect: () => void cancel(), danger: true, disabled: cancelling },
        ]}
      />}
    >
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
    <div>
        <div>
          {loggedIn ? (
            <RsvpButtons instance={instance} people={people} onChange={onRsvp} seriesFeedback={seriesFeedback}
              mutationGate={mutationGate} onSeriesFeedback={onSeriesFeedback} onTerminalRefresh={onTerminalRefresh} />
          ) : instance.visibility === "public" ? (
            <div className="space-y-2">
              <GuestRegistrationForm instance={instance} />
              <p className="text-sm text-text-muted">Already have an account?</p>
              <Button variant="secondary" onClick={onNeedLogin} layout="w-full sm:w-auto">
                Log in to register
              </Button>
            </div>
          ) : (
            <Button variant="secondary" onClick={onNeedLogin} layout="w-full sm:w-auto">
              Log in to register
            </Button>
          )}
        </div>
        {editingDate && (
          <DateEditor
            instance={instance}
            onRoomChanged={onChanged}
            onSaved={() => {
              setEditingDate(false);
              onChanged();
            }}
            onClose={() => setEditingDate(false)}
          />
        )}
        {canManage && managing && <div ref={manageRef} className="scroll-mt-4"><Suspense fallback={<Skeleton kind="lines" />}><ManageRoster instance={instance} onChanged={onChanged} onClose={() => setManaging(false)} /></Suspense></div>}
    </div>
        {!single && (
          <DateActions eventId={instance.eventId} date={instance.date} sharePath={`/events/${instance.eventId}/dates/${instance.date}`}
            title={instance.title || eventTitle} when={dateLine(instance.startsAt, instance.endsAt)} nameDate />
        )}
    </OccurrenceCard>
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
      <Form noValidate onSubmit={add} className="flex flex-col gap-3 sm:flex-row sm:items-end">
        <div className="min-w-0 flex-1">
          <Field label="Host email" hint="They must already have an account.">
            <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </Field>
        </div>
        <Button variant="secondary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          Add host
        </Button>
      </Form>
    </div>
  );
}

