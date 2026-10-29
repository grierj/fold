import { Form } from "../components/ui/Form";
import { DateEditor } from "../components/DateEditor";
import { AddRequestForm } from "../components/AddRequestForm";
import { PeoplePicker } from "../components/ui/PeoplePicker";
import { RequestContactButton } from "../components/RequestContactButton";
import { ContactDetails } from "../components/ContactDetails";
import { Badge } from "../components/ui/Badge";
import { useHousehold } from "../lib/useHousehold";
import { SeriesEditor } from "../components/SeriesEditor";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, Events, GroupCover, Groups, Invitations } from "../api";
import type { DepartureConfirmation, PersonSearchResult, EventOccurrence, GroupDetailResponse, OpenedDecision, HouseholdPerson, Visibility, VisibilityImpact } from "../types";
import { useAuth } from "../auth";
import { OccurrenceCard } from "../components/OccurrenceCard";
import { Attendance } from "../components/Attendance";
import { Menu } from "../components/ui/Menu";
import { EventVisibilityControl, visibilityRank } from "../components/EventVisibility";
import { GroupVisibilityReview } from "../components/GroupVisibilityReview";
import { GroupDepartureDialog } from "../components/GroupDepartureDialog";
import { RsvpButtons } from "../components/RsvpButtons";
import { GuestRegistrationForm } from "../components/GuestRegistrationForm";
import { Avatar } from "../components/Avatar";
import { ImageUpload } from "../components/ImageUpload";
import { coverSrc } from "../lib/covers";
import { CalendarIcon } from "../components/icons";
import { ErrorNotice } from "../components/ErrorNotice";
import { Missing } from "./NotFound";
import { EmptyState } from "../components/ui/EmptyState";
import { Illustration } from "../components/ui/Illustration";
import { Skeleton } from "../components/ui/Skeleton";
import { focalPosition, prepareUpload } from "../lib/image";
import { formatTime, formatWhen } from "../lib/when";
import { errorCode, errorText } from "../lib/errors";
import { Checkbox, Choices, Field, FormError, HiddenFileInput, Input, Radio, Textarea } from "../components/ui/Field";
import { FormActions } from "../components/ui/FormActions";
import { Button, ButtonLink } from "../components/ui/Button";
import { applyActorRsvpResults, type ActorRsvpResult, type SeriesFeedbackSignal } from "../lib/rsvpFeedback";
import { useRsvpMutationGate, type RsvpMutationGate } from "../lib/useRsvpMutationGate";

type Tab = "events" | "about" | "members";


function EventRow({
  instance,
  groupName,
  groupVisibility,
  canManage,
  showDescription,
  moreDates,
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
  moreDates: number;
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
  const [cancelling, setCancelling] = useState(false);
  const [editor, setEditor] = useState<null | "date" | "series">(null);
  const time = formatTime(instance.startsAt);
  const loc = instance.location || "";
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
          { label: instance.recurrenceRule ? "Edit series" : "Edit event", onSelect: () => setEditor(editor === "series" ? null : "series") },
          { label: cancelling ? "Cancelling…" : "Cancel this date", onSelect: () => void cancel(), danger: true, disabled: cancelling },
        ]}
      />}
    >
        {showDescription && instance.description && <p className="text-sm text-text-muted whitespace-pre-wrap">{instance.description}</p>}
        {instance.overridden && <div className="text-sm text-text-muted">This date was edited on its own, so series changes leave it alone.</div>}
        <Attendance instance={instance} />
        {moreDates > 0 && (
          <Link to={`/events/${instance.eventId}`} className="inline-flex min-h-11 items-center text-sm font-medium text-primary">
            {moreDates} more {moreDates === 1 ? "date" : "dates"} loaded
          </Link>
        )}
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
        {editor === "date" && <DateEditor instance={instance} onSaved={onChanged} onClose={() => setEditor(null)} />}
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
    visibility?: Visibility;
    memberLimit?: number | null;
  };
  saveLabel: string;
  onSaved: (data: GroupFormData) => Promise<void>;
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
  const [memberLimit, setMemberLimit] = useState(initial.memberLimit ? String(initial.memberLimit) : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  return (
    <Form noValidate
      className="bg-surface rounded-lg border border-border p-6 space-y-6"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError(null);
        try {
          await onSaved({
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
        } catch (err) {
          setError(errorText(err, "We couldn't save your changes."));
        } finally {
          setBusy(false);
        }
      }}
    >
      {error && <FormError>{error}</FormError>}
      <section className="space-y-4">
        <h2 className="text-base font-semibold">About the group</h2>
        <Field id="group-name" label="Group name">
          <Input value={name} onChange={(e) => setName(e.target.value)} required />
        </Field>
        <Field id="group-description" label="Description" optional>
          <Textarea rows={4} value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <Field id="group-location" label="Location" optional hint="Where it usually meets, for example Room 201, Main Campus.">
          <Input value={location} onChange={(e) => setLocation(e.target.value)} />
        </Field>
        <Field id="group-tags" label="Tags" optional hint="Separate tags with commas, for example young-adults, bible-study.">
          <Input value={tags} onChange={(e) => setTags(e.target.value)} />
        </Field>
      </section>
      <section className="space-y-4">
        <h2 className="text-base font-semibold">Who can join</h2>
        <Field id="member-limit" label="Member limit" optional hint="For a room with a fixed capacity. When it's full, people can't join on their own; leaders can still add someone. Leave it blank for no limit.">
          <Input type="number" min={1} step={1} value={memberLimit} onChange={(e) => setMemberLimit(e.target.value)} layout="sm:w-44" />
        </Field>
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
      <section>
        <ImageUpload
          label="Cover image"
          initialUrl={initial.imageUrl || undefined}
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
        coverFocalX: 50, coverFocalY: 50, visibility: "public",
      }}
      saveLabel="Create group"
      onSaved={onSaved}
      onCancel={onCancel}
    />
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
  const { user, loading: authLoading, isAdmin } = useAuth();
  const { people, error: householdError, retry: retryHousehold } = useHousehold(user);
  const [detail, setDetail] = useState<GroupDetailResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab>("events");
  const [editing, setEditing] = useState(false);
  const [review, setReview] = useState<{ data: GroupFormData; impact: VisibilityImpact } | null>(null);
  const [reviewBusy, setReviewBusy] = useState(false);
  const [reviewError, setReviewError] = useState<string | null>(null);
  const [joinBusy, setJoinBusy] = useState(false);
  const [coverBusy, setCoverBusy] = useState(false);
  const [memberEmail, setMemberEmail] = useState("");
  const [memberSelection, setMemberSelection] = useState<PersonSearchResult | null>(null);
  const [askMember, setAskMember] = useState<PersonSearchResult | null>(null);
  const [memberBusy, setMemberBusy] = useState(false);
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteBusy, setInviteBusy] = useState(false);
  const [inviteOverLimit, setInviteOverLimit] = useState(false);
  const [inviteStatus, setInviteStatus] = useState<string | null>(null);
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
      setDetail({ ...next, upcomingEvents: [...dates.values()] });
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
      <div className="max-w-6xl mx-auto px-4 py-8">
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
      <div className="max-w-xl mx-auto px-4 py-12 sm:py-16">
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

  async function saveEdit(data: GroupFormData) {
    // Making the group more private can change its events (DEC-48): show
    // the leader the list and get a decision before anything is saved.
    if (visibilityRank(data.visibility) < visibilityRank(group.visibility)) {
      const impact = await Groups.visibilityImpact(id!, data.visibility);
      if (impact.tightened.length > 0 || impact.leaderOpened.length > 0) {
        setReview({ data, impact });
        return;
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

  async function addMember(e: React.FormEvent) {
    e.preventDefault();
    const email = memberEmail.trim();
    if (memberSelection?.teen) { setAskMember(memberSelection); return; }
    if (!email && !memberSelection) return;
    const target = memberSelection ? { selectionId: memberSelection.selectionId } : email;
    setMemberBusy(true);
    setMemberError(null);
    try {
      try {
        await Groups.addMember(id!, target);
      } catch (err) {
        // Full group: going over the limit is an explicit, audited choice (DEC-39).
        if (errorCode(err) !== "group_full") throw err;
        if (!confirm(`“${group.name}” is full (${group.memberCount} of ${group.memberLimit}). Add ${memberSelection?.displayName ?? email} anyway?`)) return;
        await Groups.addMember(id!, target, true);
      }
      setMemberEmail("");
      setMemberSelection(null);
      await load();
    } catch (e) {
      setMemberError(errorCode(e) === "teen_add_approval_required" ? "Select this person by name, then choose Ask to add. An email address cannot be used to send an ask." : errorText(e, "We couldn't add that person."));
    } finally {
      setMemberBusy(false);
    }
  }

  // Invite someone to Fold and into this group (M1.2, 5.10). Leaders
  // invite only into groups they lead; the person sets a password on
  // acceptance. Going over the member limit is an explicit, audited
  // choice (DEC-39).
  async function inviteMember(e: React.FormEvent) {
    e.preventDefault();
    const email = inviteEmail.trim();
    if (!email) return;
    setInviteBusy(true);
    setMemberError(null);
    setInviteStatus(null);
    try {
      await Invitations.create({ email, groupId: Number(id), overLimit: inviteOverLimit });
      setInviteEmail("");
      setInviteOverLimit(false);
      setInviteStatus(`Invitation sent to ${email}.`);
    } catch (err) {
      setMemberError(errorText(err, "We couldn't send the invitation."));
    } finally {
      setInviteBusy(false);
    }
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
    <div>
      {/* Cover header */}
      {/* No overflow-hidden: the More menu opens below the cover. */}
      <div className="relative h-56 sm:h-72 bg-inverse">
        <img
          src={coverSrc(group)}
          alt=""
          className="w-full h-full object-cover"
          style={{ objectPosition: focalPosition(group.coverFocalX, group.coverFocalY) }}
        />
        <div className="absolute inset-0 bg-linear-to-t from-black/60 to-transparent" />
        <div className="absolute bottom-0 left-0 right-0">
          <div className="max-w-6xl mx-auto px-4 pb-5 flex items-end gap-4 flex-wrap">
            <div className="grow min-w-0">
              {(group.status === "pending" || group.visibility !== "public") && (
                <div className="flex flex-wrap gap-2 mb-2">
                  {group.status === "pending" && (
                    <span className="inline-block text-xs font-bold uppercase tracking-wide bg-warning-subtle text-warning-on-subtle rounded-full px-3 py-1">
                      Pending approval
                    </span>
                  )}
                  {group.visibility === "private" && (
                    <span className="inline-block text-xs font-bold uppercase tracking-wide bg-surface/90 text-text rounded-full px-3 py-1">
                      Private
                    </span>
                  )}
                  {group.visibility === "invite_only" && (
                    <span className="inline-block text-xs font-bold uppercase tracking-wide bg-inverse text-inverse-foreground rounded-full px-3 py-1">
                      Invite only
                    </span>
                  )}
                </div>
              )}
              <h1 className="text-3xl sm:text-4xl font-bold text-white">{group.name}</h1>
              <div className="text-white/80 text-sm mt-1">
                {canManage && group.memberLimit
                  ? `${group.memberCount} of ${group.memberLimit} members`
                  : `${group.memberCount} ${group.memberCount === 1 ? "member" : "members"}`}
                {group.leaderName ? ` · Organized by ${group.leaderName}` : ""}
                {group.location ? ` · ${group.location}` : ""}
              </div>
            </div>
            <div className="flex flex-wrap gap-2">
              {canManage && (
                <>
                  <Link
                    to={`/groups/${group.id}/events/new`}
                    className="btn-primary bg-surface! text-primary! hover:bg-sunken!"
                  >
                    Schedule event
                  </Link>
                  {/* One primary action (Schedule event); the rest live in More (DEC-63). */}
                  <Menu
                    label="More group actions"
                    items={[
                      { label: "Edit group", onSelect: () => setEditing(true) },
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
              {(group.visibility !== "invite_only" || isMember) && !(canManage && isMember) && (
                <button
                  onClick={toggleJoin}
                  disabled={joinBusy || !!departureTarget}
                  className={isMember ? "btn-secondary" : "btn-primary"}
                >
                  {joinBusy ? "…" : isMember ? "Leave group" : "Join this group"}
                </button>
              )}
            </div>
          </div>
        </div>
      </div>

      <div className="max-w-6xl mx-auto px-4 py-6">
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
              visibility: group.visibility,
              memberLimit: group.memberLimit,
            }}
            saveLabel="Save group"
            onSaved={saveEdit}
            onCancel={() => setEditing(false)}
          />
        ) : (
          <>
            {/* Tabs */}
            <div className="border-b border-border mb-6">
              <nav aria-label="Group sections" className="flex gap-6">
                {(["events", "about", "members"] as Tab[]).map((t) => (
                  <button
                    key={t}
                    onClick={() => setTab(t)}
                    className={
                      tab === t
                        ? "min-h-11 pb-3 border-b-2 border-primary text-primary font-semibold"
                        : "min-h-11 pb-3 text-text-muted hover:text-text font-medium"
                    }
                  >
                    {t === "events" ? "Upcoming events" : t === "about" ? "About" : "Members"}
                  </button>
                ))}
              </nav>
            </div>

            {tab === "events" && (
              <div className="space-y-3">
                <h2 className="sr-only">Upcoming events</h2>
                {group.visibility === "public" && group.status === "active" && (
                  <div className="flex flex-wrap items-center gap-x-2">
                    <ButtonLink variant="link" to={`/cal/groups/${group.id}.ics`} reloadDocument>
                      Subscribe in your calendar
                    </ButtonLink>
                    <span className="text-sm text-text-muted">Adds this group&rsquo;s public events to your calendar app.</span>
                  </div>
                )}
                {events.length === 0 && (
                  <EmptyState
                    illustration={<Illustration name="no-events" />}
                    title="No upcoming events"
                    body={
                      canManage
                        ? "Schedule one so members can RSVP."
                        : "When a leader schedules something, it will show up here."
                    }
                    action={
                      canManage ? (
                        <ButtonLink to={`/groups/${group.id}/events/new`} variant="secondary" layout="w-full sm:w-auto">
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
              </div>
            )}

            {tab === "about" && (
              <div className="bg-surface rounded-lg border border-border p-6 max-w-3xl space-y-4">
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
                          className="text-sm bg-sunken text-text-muted hover:bg-border rounded-full px-3 py-1"
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
              <div className="bg-surface rounded-lg border border-border p-6 max-w-3xl">
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
                      <ul className="divide-y divide-border">
                        {members.map((m) => (
                          <li key={m.id} className="py-3">
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
                      <>
                      <Form noValidate onSubmit={inviteMember} className="mt-4 flex gap-2 flex-wrap items-end">
                        <div className="grow min-w-48">
                          <Field id="invite-member" label="Invite someone to Fold">
                            <Input
                              value={inviteEmail}
                              onChange={(e) => setInviteEmail(e.target.value)}
                              type="email"
                              placeholder="name@example.com"
                              autoComplete="off"
                            />
                          </Field>
                        </div>
                        <Button variant="secondary" type="submit" disabled={inviteBusy || !inviteEmail.trim()}>
                          {inviteBusy ? "Sending…" : "Send invite"}
                        </Button>
                        {group.memberLimit != null && group.memberCount >= group.memberLimit && (
                          <div className="w-full">
                            <Checkbox
                              label="Invite over the limit anyway"
                              description="This group is full. Going over the limit is logged."
                              checked={inviteOverLimit}
                              onChange={(e) => setInviteOverLimit(e.target.checked)}
                            />
                          </div>
                        )}
                      </Form>
                      {inviteStatus && <p className="text-success text-sm mt-2">{inviteStatus}</p>}
                      <Form noValidate onSubmit={addMember} className="mt-4 space-y-3">
                        {user?.status === "active" && user.accountType === "adult" && <Field label="Find a person by name"><PeoplePicker value={memberSelection} disabled={memberBusy} onChange={(person) => { setMemberSelection(person); setAskMember(null); setMemberEmail(""); setMemberError(null); }} /></Field>}
                        <div className="grow min-w-48">
                          <Field id="add-member" label="Add a member by email">
                            <Input
                              value={memberEmail}
                              onChange={(e) => { setMemberEmail(e.target.value); setMemberSelection(null); setAskMember(null); }}
                              type="email"
                              placeholder="name@example.com"
                              autoComplete="off"
                            />
                          </Field>
                        </div>
                        <Button variant="secondary" type="submit" disabled={memberBusy || (!memberSelection && !memberEmail.trim())}>
                          {memberBusy ? "Adding…" : memberSelection?.teen ? "Ask to add" : "Add member"}
                        </Button>
                      </Form>
                      {askMember && user?.status === "active" && user.accountType === "adult" && <AddRequestForm key={askMember.selectionId} person={askMember} scope={{ contextType: "group", contextId: Number(id) }} full={group.memberLimit !== null && group.memberCount >= group.memberLimit} onClose={() => setAskMember(null)} />}
                      </>
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
