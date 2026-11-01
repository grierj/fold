import { Tabs } from "../components/ui/Tabs";
import { Form } from "../components/ui/Form";
import { DateEditor } from "../components/DateEditor";
import { AddPeople } from "../components/AddPeople";
import { RequestContactButton } from "../components/RequestContactButton";
import { ContactDetails } from "../components/ContactDetails";
import { Badge } from "../components/ui/Badge";
import { useHousehold } from "../lib/useHousehold";
import { SeriesEditor } from "../components/SeriesEditor";
import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, Events, GroupCover, Groups } from "../api";
import type { DepartureConfirmation, EventOccurrence, GroupDetailResponse, OpenedDecision, HouseholdPerson, Visibility, VisibilityImpact } from "../types";
import { useAuth } from "../auth";
import { OccurrenceCard } from "../components/OccurrenceCard";
import { Attendance } from "../components/Attendance";
import { Menu } from "../components/ui/Menu";
import { composePath, EMAIL_GOING, EMAIL_MEMBERS } from "../components/messages/composeLinks";
import { placeLine } from "../lib/place";
import { EventVisibilityControl, visibilityRank } from "../components/EventVisibility";
import { GroupVisibilityReview } from "../components/GroupVisibilityReview";
import { GroupDepartureDialog } from "../components/GroupDepartureDialog";
import { RsvpButtons } from "../components/RsvpButtons";
import { GuestRegistrationForm } from "../components/GuestRegistrationForm";
import { Avatar } from "../components/Avatar";
import { ImageUpload } from "../components/ImageUpload";
import { coverSrc } from "../lib/covers";
import { CalendarIcon, ChevronDownIcon } from "../components/icons";
import { ErrorNotice } from "../components/ErrorNotice";
import { Missing } from "./NotFound";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";
import { focalPosition, prepareUpload } from "../lib/image";
import { formatTime, formatWhen } from "../lib/when";
import { errorText } from "../lib/errors";
import { Checkbox, Choices, Field, FormError, HiddenFileInput, Input, Radio, Textarea } from "../components/ui/Field";
import { FormActions } from "../components/ui/FormActions";
import { Button, ButtonLink } from "../components/ui/Button";
import { applyActorRsvpResults, type ActorRsvpResult, type SeriesFeedbackSignal } from "../lib/rsvpFeedback";
import { useUnsavedChanges } from "../lib/unsavedChanges";
import { useRsvpMutationGate, type RsvpMutationGate } from "../lib/useRsvpMutationGate";

type Tab = "events" | "about" | "members";


function EventRow({
  instance,
  groupName,
  groupVisibility,
  canManage,
  showDescription,
  moreDates,
  totalKnown,
  loggedIn,
  onRsvp,
  people,
  seriesFeedback,
  mutationGate,
  onSeriesFeedback,
  onTerminalRefresh,
  onCancelled,
  onChanged,
}: {
  instance: EventOccurrence;
  groupName: string;
  groupVisibility: Visibility;
  canManage: boolean;
  showDescription: boolean;
  // A series shows its next date only, plus a link to the rest (DEC-63).
  // moreDates counts the loaded rest; totalKnown says the loaded dates are all of them.
  moreDates: number;
  totalKnown: boolean;
  loggedIn: boolean;
  onRsvp: () => void;
  people: HouseholdPerson[];
  seriesFeedback: SeriesFeedbackSignal | null;
  mutationGate: RsvpMutationGate;
  onSeriesFeedback: (eventId: number, date: string, actorResults: ActorRsvpResult[]) => void;
  onTerminalRefresh: () => void;
  onCancelled: (eventId: number, date: string) => void;
  onChanged: () => void;
}) {
  const navigate = useNavigate();
  const { canCompose } = useAuth();
  const [cancelling, setCancelling] = useState(false);
  const [editor, setEditor] = useState<null | "date" | "series">(null);
  const time = formatTime(instance.startsAt);
  const loc = placeLine(instance.room, instance.location);
  const name = instance.title || groupName;

  async function cancel() {
    if (!confirm(`Cancel this date on ${instance.date}?`)) return;
    setCancelling(true);
    try {
      await Events.cancelOccurrence(instance.eventId, instance.date);
      onCancelled(instance.eventId, instance.date);
    } catch (e) {
      alert(errorText(e, "We couldn't cancel that date."));
    } finally {
      setCancelling(false);
    }
  }

  return (
    <OccurrenceCard at={instance.startsAt} title={name}
      notice={canManage && instance.visibilityOverride && <p className="text-xs text-text-muted">Visibility changed for this date</p>}
      meta={<div className="text-sm text-text-muted">{time}{loc ? ` · ${loc}` : ""}</div>}
      menu={canManage && <Menu
        tone="quiet"
        label={`More for ${name} on ${instance.date}`}
        items={[
          { label: "Edit this date", onSelect: () => setEditor(editor === "date" ? null : "date") },
          // The event page opens its Manage sign-ups panel for this date (DEC-99).
          { label: "Manage sign-ups", onSelect: () => navigate(`/events/${instance.eventId}?manage=${instance.date}`) },
          ...(canCompose ? [{ label: EMAIL_GOING, onSelect: () => navigate(composePath({ occurrence: instance.id })) }] : []),
          { label: instance.recurrenceRule ? "Edit series" : "Edit event", onSelect: () => setEditor(editor === "series" ? null : "series") },
          { label: cancelling ? "Cancelling…" : "Cancel this date", onSelect: () => void cancel(), danger: true, disabled: cancelling },
        ]}
      />}
    >
        {showDescription && instance.description && <p className="text-sm text-text-muted whitespace-pre-wrap">{instance.description}</p>}
        {instance.overridden && <div className="text-sm text-text-muted">This date was edited on its own, so series changes leave it alone.</div>}
        <Attendance instance={instance} />
        {loggedIn && (
          <div className="mt-2">
            <RsvpButtons instance={instance} people={people} onChange={onRsvp} seriesFeedback={seriesFeedback}
              mutationGate={mutationGate} onSeriesFeedback={onSeriesFeedback} onTerminalRefresh={onTerminalRefresh} />
          </div>
        )}
        {!loggedIn && instance.visibility === "public" && (
          <div className="mt-2">
            <GuestRegistrationForm instance={instance} />
          </div>
        )}
        {/* Below the answer controls (DEC-99). "See all N" only when the page holds every
            date; a truncated list or an open-ended series says "See more dates". */}
        {moreDates > 0 && (
          <Link to={`/events/${instance.eventId}`} className="inline-flex min-h-11 items-center text-sm font-medium text-primary">
            {totalKnown ? `See all ${moreDates + 1} dates` : "See more dates"}
          </Link>
        )}
        {editor === "date" && <DateEditor instance={instance} onSaved={onChanged} onRoomChanged={onChanged} onClose={() => setEditor(null)} />}
        {editor === "series" && (
          <>
            <SeriesEditor
              series={{
                eventId: instance.eventId,
                startsLocal: instance.seriesStartsLocal,
                durationMinutes: instance.seriesDurationMinutes,
                tzid: instance.tzid,
                title: instance.seriesTitle,
                description: instance.description,
                location: instance.seriesLocation,
                recurrenceRule: instance.recurrenceRule,
                rruleUntil: instance.rruleUntil,
                capacity: instance.capacity,
                waitlistEnabled: instance.waitlistEnabled,
                guestsAllowed: instance.guestsAllowed,
                reminderEmailMinutes: instance.reminderEmailMinutes,
                reminderPush: instance.reminderPush,
                childcareAvailable: instance.childcareAvailable, childcareCapacity: instance.childcareCapacity,
                childcareAges: instance.childcareAges, childcareNotes: instance.childcareNotes,
                remoteAvailable: instance.remoteAvailable,
                remoteCapacity: instance.remoteCapacity,
                videoProvider: instance.videoProvider,
                videoUrl: instance.videoUrl,
                videoReveal: instance.videoReveal,
              }}
              idPrefix={`${instance.id}`}
              locationHint="Leave it blank to use the group's location."
              roomDate={instance.recurrenceRule ? undefined : instance.date}
              onRoomChanged={onChanged}
              onSaved={onChanged}
              onClose={() => setEditor(null)}
            />
            <EventVisibilityControl
              key={`${instance.eventId}-${instance.visibility}-${instance.leaderOpened}`}
              eventId={instance.eventId}
              groupName={groupName}
              groupVisibility={groupVisibility}
              visibility={instance.seriesVisibility ?? instance.visibility ?? groupVisibility}
              leaderOpened={instance.leaderOpened}
              onSaved={onChanged}
            />
          </>
        )}
    </OccurrenceCard>
  );
}

export interface GroupFormData {
  name: string;
  description: string;
  location: string;
  tags: string[];
  coverFile?: File | null;
  removeCover?: boolean;
  coverFocal?: { x: number; y: number };
  visibility: Visibility;
  memberLimit: number | null;
}

function GroupForm({
  initial,
  saveLabel,
  onSaved,
  onCancel,
}: {
  initial: {
    name: string; description: string; location: string; tags: string[];
    imageUrl: string; coverFocalX: number; coverFocalY: number;
    // The id the no-photo pattern comes from (coverSrc); 0 for a group with no id yet.
    coverId: number;
    visibility?: Visibility;
    memberLimit?: number | null;
    // Tags, member limit and cover start open when editing, closed on create (DEC-100).
    moreOpen: boolean;
  };
  saveLabel: string;
  // Resolve false to say nothing was saved (the leader still has a review to confirm).
  onSaved: (data: GroupFormData) => Promise<void | false>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(initial.name);
  const [description, setDescription] = useState(initial.description);
  const [location, setLocation] = useState(initial.location);
  const [tags, setTags] = useState(initial.tags.join(", "));
  const [removeCover, setRemoveCover] = useState(false);
  const [coverFile, setCoverFile] = useState<File | null>(null);
  const [coverFocal, setCoverFocal] = useState<{ x: number; y: number } | null>(null);
  const [visibility, setVisibility] = useState<Visibility>(initial.visibility ?? "public");
  const initialLimit = initial.memberLimit ? String(initial.memberLimit) : "";
  const [memberLimit, setMemberLimit] = useState(initialLimit);
  const [moreOpen, setMoreOpen] = useState(initial.moreOpen);
  const limitRef = useRef<HTMLInputElement>(null);
  const moreId = useId();
  const summaryId = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const dirty = name !== initial.name || description !== initial.description || location !== initial.location
    || tags !== initial.tags.join(", ") || visibility !== (initial.visibility ?? "public") || memberLimit !== initialLimit
    || coverFile !== null || coverFocal !== null || removeCover;
  const { guard, markSaved } = useUnsavedChanges(dirty);
  // What a closed "More options" still tells you, so nothing in it is a surprise.
  const tagList = tags.split(",").map((t) => t.trim()).filter(Boolean);
  const hasPhoto = !!coverFile || (!!initial.imageUrl && !removeCover);
  const moreSummary = [
    tagList.length ? `Tags: ${tagList.join(", ")}` : "No tags",
    memberLimit.trim() ? `Limit ${memberLimit.trim()}` : "No member limit",
    hasPhoto ? "Photo cover" : "Pattern cover",
  ].join(" · ");

  return (
    <Form noValidate
      className="fold-form space-y-8"
      // A bad member limit inside the closed section opens it before the form
      // checks its fields, so the error shows and focus can land on the field.
      onSubmitCapture={() => {
        if (!moreOpen && limitRef.current && !limitRef.current.validity.valid) flushSync(() => setMoreOpen(true));
      }}
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError(null);
        try {
          const saved = await onSaved({
            name,
            description,
            location,
            coverFile,
            removeCover: !coverFile && removeCover,
            coverFocal: coverFocal ?? undefined,
            tags: tags
              .split(",")
              .map((t) => t.trim())
              .filter(Boolean),
            visibility,
            memberLimit: memberLimit.trim() ? Number(memberLimit) : null,
          });
          if (saved !== false) markSaved();
        } catch (err) {
          setError(errorText(err, "We couldn't save your changes."));
        } finally {
          setBusy(false);
        }
      }}
    >
      {guard}
      {error && <FormError>{error}</FormError>}
      <section className="space-y-4">
        <h2 className="text-xl font-semibold">About the group</h2>
        <Field id="group-name" label="Group name">
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </Field>
        <Field id="group-description" label="Description" optional>
          <Textarea rows={4} value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <Field id="group-location" label="Location" optional hint="Where it usually meets, for example Room 201, Main Campus.">
          <Input value={location} onChange={(e) => setLocation(e.target.value)} />
        </Field>
      </section>
      <section className="fold-section">
        <Choices legend="Who can see this group">
          <Radio
            name="visibility"
            label="Public"
            description="Anyone can see it, including people who are logged out."
            checked={visibility === "public"}
            onChange={() => setVisibility("public")}
          />
          <Radio
            name="visibility"
            label="Private"
            description="Only signed-in people can see it."
            checked={visibility === "private"}
            onChange={() => setVisibility("private")}
          />
          <Radio
            name="visibility"
            label="Invite only"
            description="Hidden from everyone except its members and church admins. People join only when a leader adds them."
            checked={visibility === "invite_only"}
            onChange={() => setVisibility("invite_only")}
          />
        </Choices>
      </section>
      <section className="fold-section">
        <Button variant="link" aria-expanded={moreOpen} aria-controls={moreId} aria-describedby={moreOpen ? undefined : summaryId} onClick={() => setMoreOpen(!moreOpen)}>
          More options
          <ChevronDownIcon className={`ml-1 size-5 ${moreOpen ? "rotate-180" : ""}`} />
        </Button>
        {!moreOpen && <p id={summaryId} className="text-sm text-text-muted">{moreSummary}</p>}
        {/* Hidden, not unmounted: what's typed or picked here survives closing it. */}
        <div id={moreId} hidden={!moreOpen} className="mt-4 space-y-6">
          <Field id="group-tags" label="Tags" optional hint="Separate tags with commas, for example young-adults, bible-study.">
            <Input value={tags} onChange={(e) => setTags(e.target.value)} />
          </Field>
          <Field id="member-limit" label="Member limit" optional hint="For a room with a fixed capacity. When it's full, people can't join on their own; leaders can still add someone. Leave it blank for no limit.">
            <Input ref={limitRef} type="number" min={1} step={1} value={memberLimit} onChange={(e) => setMemberLimit(e.target.value)} layout="sm:w-44" />
          </Field>
          <div>
            <ImageUpload
              label="Cover image"
              initialUrl={initial.imageUrl || undefined}
              placeholderSrc={coverSrc({ id: initial.coverId })}
              initialFocal={{ x: initial.coverFocalX, y: initial.coverFocalY }}
              maxDim={1600}
              aspectClass="aspect-video"
              onFile={setCoverFile}
              onFocal={setCoverFocal}
            />
            {initial.imageUrl && !coverFile && (
              <div className="mt-2">
                <Checkbox label="Remove the current cover" checked={removeCover} onChange={(e) => setRemoveCover(e.target.checked)} />
              </div>
            )}
          </div>
        </div>
      </section>
      <FormActions>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          {busy ? "Saving…" : saveLabel}
        </Button>
        <Button variant="secondary" layout="w-full sm:w-auto" onClick={onCancel}>
          Cancel
        </Button>
      </FormActions>
    </Form>
  );
}

// Exported so /groups/new can reuse the same form.
export function CreateGroupForm({ onSaved, onCancel }: {
  onSaved: (data: GroupFormData) => Promise<void>;
  onCancel: () => void;
}) {
  return (
    <GroupForm
      initial={{
        name: "", description: "", location: "", tags: [], imageUrl: "",
        // No id yet, so the cover pattern is the one coverSrc gives id 0.
        coverId: 0, moreOpen: false,
        coverFocalX: 50, coverFocalY: 50, visibility: "public",
      }}
      saveLabel="Create group"
      onSaved={onSaved}
      onCancel={onCancel}
    />
  );
}

// The description above the tabs, clamped to three lines. "Read more" shows
// only when the text really overflows, and is re-checked when the width
// changes (rotating a phone, resizing the window).
function ClampedDescription({ text, onReadMore }: { text: string; onReadMore: () => void }) {
  const ref = useRef<HTMLParagraphElement>(null);
  const [overflows, setOverflows] = useState(false);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const check = () => setOverflows(el.scrollHeight > el.clientHeight);
    check();
    if (typeof ResizeObserver !== "undefined") {
      const observer = new ResizeObserver(check);
      observer.observe(el);
      return () => observer.disconnect();
    }
    window.addEventListener("resize", check);
    return () => window.removeEventListener("resize", check);
  }, [text]);
  return (
    <div className="mb-6 max-w-3xl">
      <p ref={ref} className="text-text-muted whitespace-pre-wrap wrap-anywhere line-clamp-3">{text}</p>
      {overflows && (
        <Button variant="link" onClick={onReadMore}>
          Read more
        </Button>
      )}
    </div>
  );
}

export function GroupDetail() {
  const { id } = useParams<{ id: string }>();
  const { user, loading } = useAuth();
  // A new group or viewer must never inherit dates, cursors or pending loads.
  return <GroupDetailContent key={`${id}:${loading}:${user?.id}:${user?.role}:${user?.status}:${user?.accountType}`} />;
}

function GroupDetailContent() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user, loading: authLoading, isAdmin, canCompose } = useAuth();
  const { people, error: householdError, retry: retryHousehold } = useHousehold(user);
  const [detail, setDetail] = useState<GroupDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("events");
  const sectionsRef = useRef<HTMLDivElement>(null);
  const [editing, setEditing] = useState(false);
  const [review, setReview] = useState<{ data: GroupFormData; impact: VisibilityImpact } | null>(null);
  const [reviewBusy, setReviewBusy] = useState(false);
  const [reviewError, setReviewError] = useState<string | null>(null);
  const [joinBusy, setJoinBusy] = useState(false);
  const [coverBusy, setCoverBusy] = useState(false);
  const [memberBusy, setMemberBusy] = useState(false);
  const [memberError, setMemberError] = useState<string | null>(null);
  const [departureTarget, setDepartureTarget] = useState<{ subjectId?: number; allowNote: boolean } | null>(null);
  const [departureStatus, setDepartureStatus] = useState<string | null>(null);
  const [seriesFeedback, setSeriesFeedback] = useState<SeriesFeedbackSignal | null>(null);
  const seriesFeedbackRevision = useRef(0);
  const coverRef = useRef<HTMLInputElement>(null);
  const loadRevision = useRef(0);
  const detailRef = useRef<GroupDetailResponse | null>(null);
  detailRef.current = detail;
  const [loadingMore, setLoadingMore] = useState(false);
  const rsvpMutationGate = useRsvpMutationGate();

  // Quiet refreshes keep ordinary RSVP drafts mounted. Authority-changing
  // refreshes invalidate protected data immediately, and only the newest
  // request may commit detail, error, not-found, loading or navigation state.
  const load = useCallback(async ({ quiet = false, failClosed = false, append = false, onLoaded, onFailure }: {
    quiet?: boolean;
    append?: boolean;
    failClosed?: boolean;
    onLoaded?: () => void;
    onFailure?: (error: unknown) => void;
  } = {}) => {
    if (!id) return;
    const previous = detailRef.current;
    if (append && (!previous?.hasMore || !previous.nextCursor)) return;
    const revision = ++loadRevision.current;
    setLoadingMore(append);
    if (failClosed) {
      setDetail(null);
      setLoading(true);
    } else if (!quiet) {
      setLoading(true);
    }
    setError(null);
    setNotFound(false);
    try {
      let next = append
        ? await Groups.get(id, { cursor: previous!.nextCursor })
        : await Groups.get(id);
      if (loadRevision.current !== revision) return;
      const dates = new Map((append ? previous!.upcomingEvents : []).map((date) => [date.id, date]));
      for (const date of next.upcomingEvents) dates.set(date.id, date);
      // Refresh every loaded page, not just the first 25 dates, after an RSVP
      // or edit. Commit atomically so stale authority cannot reappear.
      const target = append ? 0 : previous?.upcomingEvents.length ?? 0;
      while (next.hasMore && next.nextCursor && dates.size < target) {
        next = await Groups.get(id, { cursor: next.nextCursor });
        if (loadRevision.current !== revision) return;
        for (const date of next.upcomingEvents) dates.set(date.id, date);
      }
      // Continuation metadata belongs to the last fetched page, while the
      // visible rows and counts describe all loaded dates, not a series total.
      setDetail({ ...next, upcomingEvents: [...dates.values()].sort((a, b) => Date.parse(a.startsAt) - Date.parse(b.startsAt) || a.id - b.id) });
      onLoaded?.();
    } catch (e) {
      if (loadRevision.current !== revision) return;
      if (onFailure) onFailure(e);
      else if (e instanceof ApiError && e.status === 404) setNotFound(true);
      else setError(errorText(e, "We couldn't load this group."));
    } finally {
      if (loadRevision.current === revision) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  }, [id]);

  useEffect(() => {
    void load();
    return () => { ++loadRevision.current; };
  }, [load]);

  if (loading || authLoading) {
    return (
      <div className="fold-page">
        <Skeleton kind="cover" />
        <div className="mt-6">
          <Skeleton kind="lines" />
        </div>
      </div>
    );
  }
  if (notFound) {
    return (
      <Missing
        title="Group not found"
        body="It may have been removed, or it may be private. If someone invited you, ask them for the link again."
      />
    );
  }
  if (!detail) {
    return (
      <div className="fold-page-narrow">
        <h1 className="text-3xl font-bold mb-6">Group unavailable</h1>
        <ErrorNotice message={error ?? "We couldn't load this group."} onRetry={() => void load()} />
      </div>
    );
  }

  const { group, members, upcomingEvents, canManage, isMember } = detail;
  const events: EventOccurrence[] = upcomingEvents ?? [];
  // Count only loaded, visible dates: a capped page is not a series total.
  const nextDates: EventOccurrence[] = [];
  const datesPerEvent = new Map<number, number>();
  for (const m of events) {
    if (!datesPerEvent.has(m.eventId)) nextDates.push(m);
    datesPerEvent.set(m.eventId, (datesPerEvent.get(m.eventId) ?? 0) + 1);
  }
  // Leadership comes from membership roles (several leaders allowed).
  const leaderCount = (members ?? []).filter((m) => m.role === "leader").length;
  const isLeader = !!user && (members ?? []).some((m) => m.id === user.id && m.role === "leader");

  // "Read more" under the clamped description opens About and moves focus to
  // that tab, so a keyboard or screen-reader user lands where the text is.
  function showAbout() {
    setTab("about");
    requestAnimationFrame(() => sectionsRef.current?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')?.focus());
  }

  async function toggleJoin() {
    if (!user) {
      navigate("/login", { state: { from: `/groups/${id}` } });
      return;
    }
    if (isMember) {
      setDepartureStatus(null);
      setDepartureTarget({ allowNote: false });
      return;
    }
    setJoinBusy(true);
    try {
      await Groups.join(id!);
      await load();
    } catch (e) {
      alert(errorText(e, "That didn't work."));
    } finally {
      setJoinBusy(false);
    }
  }

  function departureResult(result: DepartureConfirmation) {
    return `${result.keptCount} ${result.keptCount === 1 ? "date" : "dates"} kept · ${result.endedCount} ${result.endedCount === 1 ? "date" : "dates"} ended.`;
  }

  async function completedDeparture(result: DepartureConfirmation) {
    setDepartureTarget(null);
    const summary = departureResult(result);
    if (result.action === "remove") {
      setDepartureStatus(summary);
      await load({ quiet: true });
      return;
    }
    await load({
      failClosed: true,
      onLoaded: () => setDepartureStatus(summary),
      onFailure: () => navigate("/my-groups", { replace: true, state: { departureStatus: summary } }),
    });
  }

  // Refetch the whole date after a batch; counts and mixed outcomes come from the server.
  function handleRsvp() { void load({ quiet: true }); }
  function reportSeriesFeedback(eventId: number, date: string, actorResults: ActorRsvpResult[]) {
    const signal = { eventId, date, actorResults, revision: ++seriesFeedbackRevision.current };
    setDetail((current) => current ? {
      ...current,
      upcomingEvents: (current.upcomingEvents ?? []).map((instance) => applyActorRsvpResults(instance, eventId, actorResults)),
    } : current);
    setSeriesFeedback(signal);
  }

  function handleCancelled(eventId: number, date: string) {
    setDetail((d) =>
      d
        ? {
            ...d,
            upcomingEvents: (d.upcomingEvents ?? []).filter((m) => !(m.eventId === eventId && m.date === date)),
          }
        : d
    );
  }

  async function saveEdit(data: GroupFormData): Promise<void | false> {
    // Making the group more private can change its events (DEC-48): show
    // the leader the list and get a decision before anything is saved.
    if (visibilityRank(data.visibility) < visibilityRank(group.visibility)) {
      const impact = await Groups.visibilityImpact(id!, data.visibility);
      if (impact.tightened.length > 0 || impact.leaderOpened.length > 0) {
        setReview({ data, impact });
        return false;
      }
    }
    await commitEdit(data);
  }

  async function confirmReview(decisions: Record<string, OpenedDecision>) {
    if (!review) return;
    setReviewBusy(true);
    setReviewError(null);
    try {
      await commitEdit(review.data, decisions);
      setReview(null);
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // The events changed since the preview; show the new list.
        setReview({ data: review.data, impact: await Groups.visibilityImpact(id!, review.data.visibility) });
        setReviewError("The group's events changed. Please review them again.");
      } else {
        setReviewError(errorText(e, "We couldn't save your changes."));
      }
    } finally {
      setReviewBusy(false);
    }
  }

  async function commitEdit(data: GroupFormData, openedEvents?: Record<string, OpenedDecision>) {
    if (data.coverFile) {
      await GroupCover.upload(id!, data.coverFile);
    } else if (data.removeCover) {
      await GroupCover.remove(id!);
    }
    await Groups.update(id!, {
      name: data.name,
      description: data.description,
      tags: data.tags,
      location: data.location,
      ...(data.coverFocal ? { coverFocal: data.coverFocal } : {}),
      visibility: data.visibility,
      memberLimit: data.memberLimit,
      ...(openedEvents ? { openedEvents } : {}),
    });
    setEditing(false);
    await load();
  }

  async function setRole(memberId: number, name: string, role: "leader" | "member") {
    const question = role === "leader"
      ? `Make ${name} a leader of “${group.name}”? Church admins will be notified.`
      : `Remove ${name} as a leader of “${group.name}”?`;
    if (!confirm(question)) return;
    setMemberBusy(true);
    setMemberError(null);
    try {
      await Groups.setRole(id!, memberId, role);
      await load();
    } catch (e) {
      setMemberError(errorText(e, "We couldn't change that role."));
    } finally {
      setMemberBusy(false);
    }
  }

  function removeMember(memberId: number) {
    setMemberError(null);
    setDepartureStatus(null);
    setDepartureTarget({ subjectId: memberId, allowNote: true });
  }

  async function requestDeletion() {
    if (!confirm("Ask an admin to delete this group? It stays up until they approve.")) return;
    setMemberBusy(true);
    setMemberError(null);
    try {
      await Groups.requestDeletion(id!);
      await load();
    } catch (e) {
      setMemberError(errorText(e, "We couldn't send the deletion request."));
    } finally {
      setMemberBusy(false);
    }
  }

  async function cancelDeletion() {
    setMemberBusy(true);
    setMemberError(null);
    try {
      await Groups.cancelDeletion(id!);
      await load();
    } catch (e) {
      setMemberError(errorText(e, "We couldn't cancel the request."));
    } finally {
      setMemberBusy(false);
    }
  }

  async function uploadCover(file: File) {
    setCoverBusy(true);
    try {
      const resized = await prepareUpload(file, 1600);
      await GroupCover.upload(id!, resized);
      await load();
    } catch (e) {
      alert(errorText(e, "We couldn't upload the cover photo."));
    } finally {
      setCoverBusy(false);
    }
  }

  return (
    <div className="fold-page">
      {/* No overflow-hidden: group menus can extend beyond the photo header. */}
      <div className="fold-cover relative flex min-h-48 items-end rounded-lg bg-inverse sm:min-h-56">
        <img
          src={coverSrc(group)}
          alt=""
          className="absolute inset-0 w-full h-full rounded-lg object-cover"
          style={{ objectPosition: focalPosition(group.coverFocalX, group.coverFocalY) }}
        />
        {/* A uniform 75% inverse scrim keeps even white covers legible behind every line. */}
        <div className="absolute inset-0 rounded-lg bg-inverse/75" />
        <div className="relative w-full p-4 sm:p-6">
          <div className="flex flex-col gap-6 lg:flex-row lg:items-end lg:justify-between">
            <div className="grow min-w-0">
              {(group.status === "pending" || group.visibility !== "public") && (
                <div className="flex flex-wrap gap-2 mb-2">
                  {group.status === "pending" && (
                    <span className="inline-block text-sm font-semibold bg-warning-subtle text-warning-on-subtle rounded-full px-3 py-1">
                      Pending approval
                    </span>
                  )}
                  {group.visibility === "private" && (
                    <span className="inline-block text-sm font-semibold bg-surface/90 text-text rounded-full px-3 py-1">
                      Private
                    </span>
                  )}
                  {group.visibility === "invite_only" && (
                    <span className="inline-block text-sm font-semibold bg-inverse text-inverse-foreground rounded-full px-3 py-1">
                      Invite only
                    </span>
                  )}
                </div>
              )}
              <h1 className="text-3xl font-bold text-inverse-foreground wrap-anywhere">{group.name}</h1>
              <div className="mt-3 flex flex-wrap gap-x-6 gap-y-1 text-sm text-inverse-foreground">
                <span>{canManage && group.memberLimit
                  ? `${group.memberCount} of ${group.memberLimit} members`
                  : `${group.memberCount} ${group.memberCount === 1 ? "member" : "members"}`}</span>
                {group.leaderName && <span className="text-inverse-foreground">Organized by {group.leaderName}</span>}
                {group.location && <span>{group.location}</span>}
              </div>
            </div>
            <div className="flex shrink-0 flex-wrap gap-2">
              {canManage && (
                <>
                  <ButtonLink to={`/groups/${group.id}/events/new`} variant="primary-inverse">
                    Schedule event
                  </ButtonLink>
                  {/* One primary action (Schedule event); the rest live in More (DEC-63). */}
                  <Menu
                    label="More group actions"
                    items={[
                      { label: "Edit group", onSelect: () => setEditing(true) },
                      ...(canCompose && group.status === "active" ? [{ label: EMAIL_MEMBERS, onSelect: () => navigate(composePath({ group: group.id })) }] : []),
                      { label: coverBusy ? "Uploading cover…" : "Change cover photo", onSelect: () => coverRef.current?.click(), disabled: coverBusy },
                      ...(isMember ? [{ label: "Leave group", onSelect: () => void toggleJoin(), disabled: !!departureTarget }] : []),
                    ]}
                  />
                  <HiddenFileInput
                    ref={coverRef}
                    accept="image/jpeg,image/png,image/gif,image/webp"
                    onChange={(e) => {
                      const f = e.target.files?.[0];
                      if (f) uploadCover(f);
                      e.target.value = "";
                    }}
                  />
                </>
              )}
              {isMember && !canManage && (
                /* A plain member's cover offers "You're a member" with Leave group inside it,
                   the menu leaders use, so a stray tap can't start leaving (DEC-92; the
                   DEC-74 confirmation is unchanged). */
                <Menu
                  label="You're a member"
                  text="✓ You're a member"
                  items={[{ label: "Leave group", onSelect: () => void toggleJoin(), disabled: joinBusy || !!departureTarget }]}
                />
              )}
              {group.visibility !== "invite_only" && !isMember && (
                <Button variant="primary" onClick={toggleJoin} disabled={joinBusy || !!departureTarget}>
                  {joinBusy ? "…" : user ? "Join this group" : "Log in to join"}
                </Button>
              )}
            </div>
          </div>
        </div>
      </div>

      <div className="pt-6">
        {departureStatus && <p role="status" className="mb-4 rounded-lg border border-border bg-sunken px-4 py-3 text-sm">{departureStatus}</p>}
        {editing && review && (
          <>
            {reviewError && <FormError layout="mb-2">{reviewError}</FormError>}
            <GroupVisibilityReview
              key={JSON.stringify(review.impact)}
              impact={review.impact}
              busy={reviewBusy}
              onConfirm={(d) => void confirmReview(d)}
              onCancel={() => {
                setReview(null);
                setReviewError(null);
              }}
            />
          </>
        )}
        {editing ? (
          <GroupForm
            initial={{
              name: group.name,
              description: group.description,
              location: group.location,
              tags: group.tags,
              imageUrl: group.imageUrl,
              coverFocalX: group.coverFocalX,
              coverFocalY: group.coverFocalY,
              coverId: group.id,
              moreOpen: true,
              visibility: group.visibility,
              memberLimit: group.memberLimit,
            }}
            saveLabel="Save group"
            onSaved={saveEdit}
            onCancel={() => setEditing(false)}
          />
        ) : (
          <>
            {group.description.trim() && tab !== "about" && (
              <ClampedDescription text={group.description} onReadMore={showAbout} />
            )}
            <div ref={sectionsRef}>
            <Tabs label="Group sections" value={tab} onChange={setTab} options={[
              { id: "events", label: "Upcoming events" },
              { id: "about", label: "About" },
              { id: "members", label: "Members" },
            ]}>
            {tab === "events" && (
              <div className="space-y-4">
                <h2 className="sr-only">Upcoming events</h2>
                {events.length === 0 && (
                  <EmptyState
                    illustration={<Illustration name="no-events" />}
                    title={canManage ? "Schedule your group's first meeting" : "No upcoming events"}
                    body={
                      canManage
                        ? "It takes about a minute."
                        : "When a leader schedules something, it will show up here."
                    }
                    action={
                      canManage ? (
                        <ButtonLink to={`/groups/${group.id}/events/new`} variant="primary" layout="w-full sm:w-auto">
                          Schedule event
                        </ButtonLink>
                      ) : undefined
                    }
                  />
                )}
                {detail.hasMore && (
                  <Button variant="secondary" disabled={loadingMore} onClick={() => void load({ quiet: true, append: true })}>
                    {loadingMore ? "Loading dates…" : "Load more dates"}
                  </Button>
                )}
                {detail.truncated && (
                  <div className="flex flex-wrap items-center gap-x-2">
                    <span className="text-sm text-text-muted">Showing the next {events.length} dates.</span>
                    <ButtonLink variant="link" to={`/calendar?group=${group.id}`}>
                      See all dates in the calendar
                    </ButtonLink>
                  </div>
                )}
                {error && <ErrorNotice message={error} onRetry={() => void load({ quiet: true })} />}
                {householdError && <ErrorNotice message={householdError} onRetry={retryHousehold} />}
          {nextDates.map((m) => (
                  <EventRow
                    key={`${m.eventId}-${m.date}`}
                    instance={m}
                    groupName={group.name}
                    groupVisibility={group.visibility}
                    canManage={canManage}
                    showDescription
                    moreDates={(datesPerEvent.get(m.eventId) ?? 1) - 1}
                    totalKnown={!detail.hasMore && !detail.truncated && (!m.recurrenceRule || !!m.rruleUntil || /(^|;)COUNT=/i.test(m.recurrenceRule))}
                    loggedIn={!!user}
                    onRsvp={handleRsvp}
                    people={people}
                    seriesFeedback={seriesFeedback}
                    mutationGate={rsvpMutationGate}
                    onSeriesFeedback={reportSeriesFeedback}
                    onTerminalRefresh={() => void load({ quiet: true, failClosed: true })}
                    onCancelled={handleCancelled}
                    onChanged={() => void load({ quiet: true })}
                  />
                ))}
                {group.visibility === "public" && group.status === "active" && (
                  <div className="flex flex-wrap items-center gap-x-2">
                    <ButtonLink variant="link" to={`/cal/groups/${group.id}.ics`} reloadDocument>
                      Subscribe in your calendar
                    </ButtonLink>
                    <span className="text-sm text-text-muted">Adds this group&rsquo;s public events to your calendar app.</span>
                  </div>
                )}
              </div>
            )}

            {tab === "about" && (
              <div className="grid max-w-3xl gap-8 sm:grid-cols-2">
                <div>
                  <h3 className="font-bold mb-1">About this group</h3>
                  <p className="text-text-muted whitespace-pre-wrap">
                    {group.description || "No description yet."}
                  </p>
                </div>
                {group.location && (
                  <div>
                    <h3 className="font-bold mb-1">Location</h3>
                    <p className="text-text-muted">{group.location}</p>
                  </div>
                )}
                {group.tags.length > 0 && (
                  <div>
                    <h3 className="font-bold mb-1">Tags</h3>
                    <div className="flex flex-wrap gap-2">
                      {group.tags.map((t) => (
                        <Link
                          key={t}
                          to={`/?tag=${encodeURIComponent(t)}`}
                          className="inline-flex min-h-11 items-center rounded-lg bg-sunken px-3 py-2 text-sm text-text-muted hover:bg-border"
                        >
                          {t}
                        </Link>
                      ))}
                    </div>
                  </div>
                )}
                <div>
                  <h3 className="font-bold mb-1">Next event</h3>
                  <p className="text-text-muted">
                    {group.nextEventAt ? (
                      formatWhen(group.nextEventAt)
                    ) : (
                      <span className="inline-flex items-center gap-1">
                        <CalendarIcon className="h-4 w-4" />
                        No upcoming events
                      </span>
                    )}
                  </p>
                </div>
              </div>
            )}

            {tab === "members" && (
              <div className="max-w-3xl">
                {!user ? (
                  <p className="text-text-muted">
                    <Link to="/login" className="text-primary font-semibold">
                      Log in
                    </Link>{" "}
                    to see the member roster.
                  </p>
                ) : (
                  <>
                    {memberError && <FormError layout="mb-3">{memberError}</FormError>}
                    {members && members.length > 0 ? (
                      <ul className="fold-list">
                        {members.map((m) => (
                          <li key={m.id} className="fold-list-row">
                            <div className="flex items-center gap-3">
                              <Avatar
                                url={m.avatarUrl || undefined}
                                name={m.name}
                                focalX={m.avatarFocalX}
                                focalY={m.avatarFocalY}
                              />
                              <div className="grow min-w-0">
                                <div className="font-semibold">{m.name}
                                  {canManage && m.accountType === "teen" && <Badge>Teen</Badge>}
                                  {m.admin && (
                                    <span className="ml-2 inline-block rounded-full bg-primary-subtle px-2 py-0.5 text-xs font-semibold text-primary-on-subtle">
                                      Admin
                                    </span>
                                  )}
                                </div>
                                <div className="text-sm text-text-muted capitalize">{m.role}</div>
                                <ContactDetails email={m.email} phone={m.phone} />
                              </div>
                              {canManage && (
                                <Menu
                                  tone="quiet"
                                  label={`More for ${m.name}`}
                                  items={[
                                    ...(m.role !== "leader" && m.accountType !== "teen"
                                      ? [{ label: "Make leader", onSelect: () => void setRole(m.id, m.name, "leader"), disabled: memberBusy }]
                                      : []),
                                    ...(m.role === "leader" && leaderCount > 1 && (!m.admin || isAdmin)
                                      ? [{ label: "Remove as leader", onSelect: () => void setRole(m.id, m.name, "member"), disabled: memberBusy }]
                                      : []),
                                    ...(m.id !== user?.id && (m.role !== "leader" || leaderCount > 1) && (!m.admin || isAdmin)
                                      ? [{ label: "Remove from group", onSelect: () => removeMember(m.id), danger: true, disabled: memberBusy || !!departureTarget }]
                                      : []),
                                  ]}
                                />
                              )}
                            </div>
                            {canManage && isLeader && group.status === "active" && user?.status === "active" && user.accountType === "adult" && m.id !== user.id && m.email === undefined && m.phone === undefined && <div className="w-full"><RequestContactButton subjectUserId={m.id} subjectName={m.name} contextType="group" contextId={group.id} /></div>}
                          </li>
                        ))}
                      </ul>
                    ) : (
                      <p className="text-text-muted">No members yet.</p>
                    )}
                    {canManage && (
                      <AddPeople
                        groupId={group.id}
                        groupName={group.name}
                        memberCount={group.memberCount}
                        memberLimit={group.memberLimit}
                        canSearch={user?.status === "active" && user.accountType === "adult"}
                        onAdded={() => load({ quiet: true })}
                      />
                    )}
                    {isLeader && (
                      <div className="mt-6 border-t border-border pt-4">
                        {group.memberCount <= 1 ? (
                          group.deletionRequested ? (
                            <div className="space-y-2">
                              <p className="text-sm text-text-muted">
                                An admin still has to approve deleting this group.
                              </p>
                              <Button variant="secondary" onClick={cancelDeletion} disabled={memberBusy}>
                                Cancel deletion request
                              </Button>
                            </div>
                          ) : (
                            <Button variant="danger-outline" onClick={requestDeletion} disabled={memberBusy}>
                              Request deletion
                            </Button>
                          )
                        ) : (
                          <p className="text-sm text-text-muted">
                            Hand leadership to someone else. You can ask an admin to delete the group once you are the only member.
                          </p>
                        )}
                      </div>
                    )}
                  </>
                )}
              </div>
            )}
            </Tabs>
            </div>
          </>
        )}
      </div>
      {departureTarget && (
        <GroupDepartureDialog
          groupId={id!}
          subjectId={departureTarget.subjectId}
          allowNote={departureTarget.allowNote}
          onCancel={() => setDepartureTarget(null)}
          onUnavailable={() => load({ quiet: true })}
          onCompleted={completedDeparture}
        />
      )}
    </div>
  );
}
